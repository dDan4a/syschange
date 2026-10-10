package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StopHook implements the Stop hook of Claude Code and Codex (same protocol). When the agent changed
// config files or installed packages during its session and is about to
// finish, it is asked once to record them with a one-line reason.
func (s *Store) StopHook(agent string, stdin io.Reader, stdout io.Writer) error {
	var in struct {
		StopHookActive bool   `json:"stop_hook_active"`
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return err
	}
	// Asked once already: never trap the agent in a loop.
	if in.StopHookActive || !s.Initialized() {
		return nil
	}
	started, ok := sessionStart(in.TranscriptPath)
	if !ok {
		return nil
	}
	pending, err := s.Pending()
	if err != nil {
		return err
	}
	var mine []Change
	for _, c := range pending {
		if changedSince(s, c, started) {
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
	return json.NewEncoder(stdout).Encode(map[string]string{"decision": "block", "reason": b.String()})
}

// sessionStart reads the first timestamp in a Claude Code transcript.
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
		return err == nil && time.Unix(sec, 0).After(t)
	}
	return false
}

func field(lines []string, key string) string {
	for i, l := range lines {
		if l == key && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}
