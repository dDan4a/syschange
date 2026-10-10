package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// hookInput is what Claude Code, Codex and Gemini CLI send (snake_case) and
// what Grok sends (camelCase).
type hookInput struct {
	SessionID      string `json:"session_id"`
	SessionIDCamel string `json:"sessionId"`
	StopActive     bool   `json:"stop_hook_active"`
	StopActiveCaml bool   `json:"stopHookActive"`
	Transcript     string `json:"transcript_path"`
	TranscriptCaml string `json:"transcriptPath"`
}

func (in hookInput) session() string    { return firstNonEmpty(in.SessionID, in.SessionIDCamel) }
func (in hookInput) transcript() string { return firstNonEmpty(in.Transcript, in.TranscriptCaml) }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// SessionStartHook remembers when an agent session began, so the stop hook
// can tell the agent's changes from the user's.
func (s *Store) SessionStartHook(agent string, stdin io.Reader) error {
	var in hookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil || in.session() == "" || !s.Initialized() || suggesting() {
		return err
	}
	dir := s.path("sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Resumed and compacted sessions fire SessionStart again: keep the first time.
	file := s.sessionFile(agent, in.session())
	if _, err := os.Stat(file); err == nil {
		return nil
	}
	pruneSessions(dir)
	return os.WriteFile(file, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
}

func (s *Store) sessionFile(agent, id string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == os.PathSeparator || r == 0 {
			return '_'
		}
		return r
	}, id)
	return filepath.Join(s.path("sessions"), agent+"-"+safe)
}

func pruneSessions(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 30*24*time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// StopHook runs when an agent is about to finish its turn. If it changed config
// files or installed packages during its session, it is asked once to record
// them with a one-line reason.
func (s *Store) StopHook(agent string, stdin io.Reader, stdout io.Writer) error {
	var in hookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return err
	}
	// Asked once already: never trap the agent in a loop.
	if in.StopActive || in.StopActiveCaml || !s.Initialized() || suggesting() {
		return nil
	}
	started, ok := s.sessionStarted(agent, in)
	if !ok {
		return nil
	}
	pending, err := s.Pending()
	if err != nil {
		return err
	}
	// Tool-call windows say exactly which changes this session made; without
	// them (older setups), fall back to everything changed since it started.
	s.Attribute(pending)
	tracked := slices.ContainsFunc(s.windows(), func(w window) bool { return w.Agent == agent && w.Session == in.session() })
	var mine []Change
	for _, c := range pending {
		if tracked {
			if c.By == agent && c.Session == in.session() {
				mine = append(mine, c)
			}
		} else if changedSince(s, c, started) {
			mine = append(mine, c)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("You changed this system during the session. Before finishing, record why, one short line per logical change:\n")
	for _, c := range mine {
		fmt.Fprintf(&b, "- %s %s (%s)\n", c.Op, c.ID, c.Label)
	}
	fmt.Fprintf(&b, "\nRun: syschange note --by %s \"<why, in the user's language>\" <id>...\n", agent)
	b.WriteString("If one of these is not yours, leave it out. To keep a path out of the history for good: syschange ignore <path>.")
	decision := "block"
	if agent == "gemini" {
		decision = "deny" // Gemini's AfterAgent retries the turn with the reason as the prompt
	}
	return json.NewEncoder(stdout).Encode(map[string]string{"decision": decision, "reason": b.String()})
}

// suggesting is true inside the agent that syschange suggest runs: its own
// hooks have nothing to report.
func suggesting() bool { return os.Getenv("SYSCHANGE_SUGGEST") == "1" }

// sessionStarted prefers the time recorded by the SessionStart hook and falls
// back to the first timestamp in the transcript.
func (s *Store) sessionStarted(agent string, in hookInput) (time.Time, bool) {
	if in.session() != "" {
		if b, err := os.ReadFile(s.sessionFile(agent, in.session())); err == nil {
			if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b))); err == nil {
				return t, true
			}
		}
	}
	return sessionStart(in.transcript())
}

// sessionStart reads the first timestamp in a JSONL transcript (Claude Code, Codex).
func sessionStart(path string) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var line struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && !line.Timestamp.IsZero() {
			return line.Timestamp, true
		}
	}
	return time.Time{}, false
}

// changedSince tells whether a change happened after t. Deleted files and
// services carry no timestamp, so they are left to the user.
func changedSince(s *Store, c Change, t time.Time) bool {
	// File times come from a coarse clock and pacman stores whole seconds.
	t = t.Add(-time.Second)
	switch c.Kind {
	case "config":
		info, err := os.Lstat(filepath.Join(s.ConfigDir, c.Name))
		return err == nil && info.ModTime().After(t)
	case "package":
		return c.Op == "added" && packageInstalledAfter(c.Name, t)
	}
	return false
}

var pacmanDB = "/var/lib/pacman/local"

func packageInstalledAfter(name string, t time.Time) bool {
	at, ok := packageInstallTime(name)
	return ok && at.After(t)
}

func packageInstallTime(name string) (time.Time, bool) {
	dirs, _ := filepath.Glob(filepath.Join(pacmanDB, name+"-*"))
	for _, dir := range dirs {
		desc, err := os.ReadFile(filepath.Join(dir, "desc"))
		if err != nil {
			continue
		}
		fields := strings.Split(string(desc), "\n")
		if field(fields, "%NAME%") != name {
			continue
		}
		sec, err := strconv.ParseInt(field(fields, "%INSTALLDATE%"), 10, 64)
		return time.Unix(sec, 0), err == nil
	}
	return time.Time{}, false
}

func field(lines []string, key string) string {
	for i, l := range lines {
		if l == key && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}
