package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Agents report every tool call through Pre/PostToolUse hooks. syschange keeps
// the time windows of those calls; a change whose timestamp falls inside one
// was made by that agent, whether or not the agent ever describes it.

type window struct {
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

type toolInput struct {
	hookInput
	ToolUseID      string `json:"tool_use_id"`
	ToolUseIDCamel string `json:"toolUseId"`
}

func (s *Store) callFile(agent string, in toolInput) string {
	return s.sessionFile(agent, in.session()+"."+firstNonEmpty(in.ToolUseID, in.ToolUseIDCamel)) + ".call"
}

// ToolHook handles PreToolUse (start=true) and PostToolUse. It prints nothing:
// some agents read a PostToolUse hook's output as feedback.
func (s *Store) ToolHook(agent string, start bool, stdin io.Reader) error {
	var in toolInput
	if json.NewDecoder(stdin).Decode(&in) != nil || in.session() == "" || !s.Initialized() || suggesting() {
		return nil // fail open: bad input never breaks the agent
	}
	if err := os.MkdirAll(s.path("sessions"), 0o700); err != nil {
		return err
	}
	file := s.callFile(agent, in)
	if start {
		return os.WriteFile(file, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil // no matching start: nothing to record
	}
	os.Remove(file)
	began, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
	if err != nil {
		return nil
	}
	line, err := json.Marshal(window{Agent: agent, Session: in.session(), Start: began, End: time.Now().UTC()})
	if err != nil {
		return err
	}
	s.pruneWindows()
	return appendLines(s.path("agent-windows.jsonl"), []string{string(line)})
}

// pruneWindows keeps the log small: windows older than 30 days go.
func (s *Store) pruneWindows() {
	path := s.path("agent-windows.jsonl")
	if info, err := os.Stat(path); err != nil || info.Size() < 512<<10 {
		return
	}
	var keep []string
	for _, w := range s.windows() {
		if time.Since(w.End) < 30*24*time.Hour {
			b, _ := json.Marshal(w)
			keep = append(keep, string(b))
		}
	}
	os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o600)
}

func (s *Store) windows() []window {
	f, err := os.Open(s.path("agent-windows.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []window
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var w window
		if json.Unmarshal(sc.Bytes(), &w) == nil {
			out = append(out, w)
		}
	}
	return out
}

// Attribute marks each change with the agent whose tool call made it.
func (s *Store) Attribute(changes []Change) {
	ws := s.windows()
	if len(ws) == 0 {
		return
	}
	for i := range changes {
		t, ok := changeTime(s, changes[i])
		if !ok {
			continue
		}
		// File times come from a coarse clock and pacman stores whole seconds.
		for j := len(ws) - 1; j >= 0; j-- {
			w := ws[j]
			if !t.Before(w.Start.Add(-time.Second)) && !t.After(w.End.Add(time.Second)) {
				changes[i].By = w.Agent
				changes[i].Session = w.Session
				break
			}
		}
	}
}

// changeTime is when a change happened: a config file's modification time or
// a package's install time. Removals carry no time.
func changeTime(s *Store, c Change) (time.Time, bool) {
	switch {
	case c.Kind == "config" && c.Op != "removed":
		info, err := os.Lstat(filepath.Join(s.ConfigDir, c.Name))
		if err != nil {
			return time.Time{}, false
		}
		return info.ModTime(), true
	case c.Kind == "package" && c.Op == "added":
		return packageInstallTime(c.Name)
	}
	return time.Time{}, false
}
