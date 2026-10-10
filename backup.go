package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// A backup remote is any git URL (a private GitHub repo, a path on a USB
// disk). It receives two branches: "config", the history of ~/.config, and
// "state", syschange's own files (journal, package and service lists,
// checkpoints), plus the checkpoint refs. Secrets never reach it: they are
// never in the history in the first place.

const backupRemote = "backup"

// stateFiles are copied into the "state" branch when they exist.
var stateFiles = []string{"journal.jsonl", "packages.txt", "packages.init.txt", "packages.aur.txt", "units.txt", "units.init.txt", "checkpoints.jsonl"}

type PushStatus struct {
	Remote string    `json:"remote"`
	Time   time.Time `json:"time"`
	Error  string    `json:"error,omitempty"`
}

func (s *Store) RemoteURL() string {
	out, err := s.git(nil, "remote", "get-url", backupRemote)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SetRemote sets the backup remote (empty removes it) and pushes once.
func (s *Store) SetRemote(url string) error {
	if !s.Initialized() {
		return ErrNotInitialized
	}
	if s.RemoteURL() != "" {
		if _, err := s.git(nil, "remote", "remove", backupRemote); err != nil {
			return err
		}
	}
	if url == "" {
		os.Remove(s.path("push.json"))
		return nil
	}
	if _, err := s.git(nil, "remote", "add", backupRemote, url); err != nil {
		return err
	}
	return s.Push()
}

// Push sends the history to the backup remote and notes the outcome for the panel.
func (s *Store) Push() error {
	url := s.RemoteURL()
	if url == "" {
		return errors.New("no backup remote, set one with: syschange remote set URL")
	}
	err := s.commitState()
	if err == nil {
		// Never ask for a password: this runs in the background.
		env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -oBatchMode=yes"}
		_, err = s.gitEnv(env, nil, "push", "--quiet", backupRemote,
			"HEAD:refs/heads/config", "refs/heads/state:refs/heads/state", "refs/checkpoints/*:refs/checkpoints/*")
	}
	st := PushStatus{Remote: url, Time: time.Now()}
	if err != nil {
		st.Error = err.Error()
	}
	b, _ := json.Marshal(st)
	if werr := os.WriteFile(s.path("push.json"), b, 0o600); werr != nil && err == nil {
		err = werr
	}
	return err
}

func (s *Store) LastPush() *PushStatus {
	b, err := os.ReadFile(s.path("push.json"))
	if err != nil {
		return nil
	}
	var st PushStatus
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return &st
}

// commitState snapshots syschange's own files into refs/heads/state, adding a
// commit only when something changed.
func (s *Store) commitState() error {
	if s.ListForeign != nil {
		if aur, err := s.ListForeign(); err == nil {
			if err := writeLines(s.path("packages.aur.txt"), aur); err != nil {
				return err
			}
		}
	}
	var tree strings.Builder
	for _, name := range stateFiles {
		body, err := os.ReadFile(s.path(name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		blob, err := s.git(body, "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		fmt.Fprintf(&tree, "100644 blob %s\t%s\n", strings.TrimSpace(string(blob)), name)
	}
	treeID, err := s.git([]byte(tree.String()), "mktree")
	if err != nil {
		return err
	}
	treeSHA := strings.TrimSpace(string(treeID))
	args := []string{"commit-tree", treeSHA, "-m", "syschange state"}
	if parent, err := s.git(nil, "rev-parse", "--verify", "--quiet", "refs/heads/state"); err == nil {
		p := strings.TrimSpace(string(parent))
		if old, err := s.git(nil, "rev-parse", p+"^{tree}"); err == nil && strings.TrimSpace(string(old)) == treeSHA {
			return nil
		}
		args = append(args, "-p", p)
	}
	commit, err := s.git(nil, args...)
	if err != nil {
		return err
	}
	_, err = s.git(nil, "update-ref", "refs/heads/state", strings.TrimSpace(string(commit)))
	return err
}

// spawnPush pushes in a detached process, so recording never waits on the network.
func spawnPush() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "push", "--quiet")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}
