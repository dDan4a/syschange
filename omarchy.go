package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Omarchy's own actions (updates, theme and font switches) rewrite many config
// files at once. Hooks record them as one entry by "omarchy" instead of
// flooding the bar with changes the user did not make.

var omarchyEvents = map[string]string{ // hook name -> the command that fires it
	"post-update": "omarchy-update",
	"theme-set":   "omarchy-theme-set",
	"font-set":    "omarchy-font-set",
}

func (s *Store) omarchyHookPath(event string) string {
	return filepath.Join(s.ConfigDir, "omarchy", "hooks", event+".d", "syschange.hook")
}

// installOmarchyHooks adds syschange to Omarchy's hook directories. It returns
// the files it created, relative to ConfigDir; none when Omarchy is absent.
func (s *Store) installOmarchyHooks(exe string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "omarchy", "hooks")); err != nil {
		return nil, nil
	}
	var created []string
	for event := range omarchyEvents {
		path := s.omarchyHookPath(event)
		body := "#!/bin/bash\n# Installed by syschange: records what this Omarchy action changed as one entry.\n" +
			"[[ -x " + shellQuote(exe) + " ]] && " + shellQuote(exe) + " hook omarchy " + event + " \"$@\" || true\n"
		if old, err := os.ReadFile(path); err == nil && string(old) == body {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return created, err
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			return created, err
		}
		rel, _ := filepath.Rel(s.ConfigDir, path)
		created = append(created, rel)
	}
	return created, nil
}

// OmarchyHook records the changes made by the Omarchy command that fired the
// hook: config files modified and packages installed since that command started.
func (s *Store) OmarchyHook(event string, args []string) error {
	command, ok := omarchyEvents[event]
	if !ok || !s.Initialized() {
		return nil
	}
	started, ok := ancestorStart(command)
	if !ok {
		started = time.Now().Add(-2 * time.Minute)
	}
	pending, err := s.Pending()
	if err != nil {
		return err
	}
	var ids []string
	for _, c := range pending {
		if changedSince(s, c, started) {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = s.Record(ids, omarchyNote(event, args), "omarchy")
	return err
}

func omarchyNote(event string, args []string) string {
	arg := strings.Join(args, " ")
	switch event {
	case "theme-set":
		return "Theme: " + arg
	case "font-set":
		return "Font: " + arg
	}
	if out, err := exec.Command("omarchy", "version").Output(); err == nil {
		return "Omarchy update to " + strings.TrimSpace(string(out))
	}
	return "Omarchy update"
}

// ancestorStart finds the oldest ancestor process running the named script
// and returns when it started. omarchy-update re-executes itself, so the
// oldest match is where the update began.
func ancestorStart(name string) (time.Time, bool) {
	var found time.Time
	ok := false
	for pid := os.Getppid(); pid > 1; {
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			break
		}
		for _, arg := range strings.Split(string(cmdline), "\x00") {
			if filepath.Base(arg) == name {
				if t, err := processStart(pid); err == nil {
					found, ok = t, true
				}
				break
			}
		}
		pid, err = parentPID(pid)
		if err != nil {
			break
		}
	}
	return found, ok
}

func parentPID(pid int) (int, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// The command name may contain spaces and parentheses: fields start after the last ')'.
	i := strings.LastIndexByte(string(stat), ')')
	fields := strings.Fields(string(stat)[i+1:])
	if len(fields) < 2 {
		return 0, errors.New("short /proc stat")
	}
	return strconv.Atoi(fields[1])
}

func processStart(pid int) (time.Time, error) {
	out, err := exec.Command("ps", "-o", "etimes=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, err
	}
	secs, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return time.Time{}, err
	}
	// etimes is whole seconds: round the start down, not up.
	return time.Now().Add(-time.Duration(secs+1) * time.Second), nil
}
