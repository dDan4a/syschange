package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A checkpoint is the whole setup at one moment, unrecorded edits included:
// config files as a commit outside the history, plus package and service lists.
// Restoring brings the config files back and prints what to (un)install.
type Checkpoint struct {
	Name     string    `json:"name"`
	Time     time.Time `json:"time"`
	Commit   string    `json:"commit"`
	Packages []string  `json:"packages"`
	Units    []string  `json:"units"`
}

func (s *Store) Checkpoint(name string) (Checkpoint, error) {
	if !s.Initialized() {
		return Checkpoint{}, ErrNotInitialized
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = time.Now().Format("2006-01-02 15:04")
	}
	pending, err := s.Pending()
	if err != nil {
		return Checkpoint{}, err
	}
	var paths, home []string
	for _, c := range pending {
		switch {
		case c.Kind == "config" && isHome(c.Name):
			home = append(home, c.Name)
		case c.Kind == "config":
			paths = append(paths, c.Name)
		}
	}
	// A private index: the real one stays equal to HEAD, which status relies on.
	index := s.path("checkpoint.index")
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := s.gitEnv(env, nil, "read-tree", "HEAD"); err != nil {
		return Checkpoint{}, err
	}
	if len(paths) > 0 {
		if _, err := s.gitEnv(env, nulJoin(paths), "add", "--all", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return Checkpoint{}, err
		}
	}
	if err := s.stageHome(env, home); err != nil {
		return Checkpoint{}, err
	}
	tree, err := s.gitEnv(env, nil, "write-tree")
	if err != nil {
		return Checkpoint{}, err
	}
	commit, err := s.git(nil, "commit-tree", strings.TrimSpace(string(tree)), "-p", "HEAD", "-m", "checkpoint: "+name)
	if err != nil {
		return Checkpoint{}, err
	}
	cp := Checkpoint{Name: name, Time: time.Now(), Commit: strings.TrimSpace(string(commit))}
	// A ref keeps the commit from being garbage-collected.
	if _, err := s.git(nil, "update-ref", "refs/checkpoints/"+strconv.FormatInt(cp.Time.UnixNano(), 10), cp.Commit); err != nil {
		return Checkpoint{}, err
	}
	if cp.Packages, err = s.ListPackages(); err != nil {
		return Checkpoint{}, err
	}
	if cp.Units, err = s.ListUnits(); err != nil {
		return Checkpoint{}, err
	}
	b, err := json.Marshal(cp)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := appendLines(s.path("checkpoints.jsonl"), []string{string(b)}); err != nil {
		return cp, err
	}
	s.afterChange()
	return cp, nil
}

// Checkpoints returns saved checkpoints, newest first.
func (s *Store) Checkpoints() ([]Checkpoint, error) {
	f, err := os.Open(s.path("checkpoints.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Checkpoint
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var cp Checkpoint
		if err := json.Unmarshal(sc.Bytes(), &cp); err != nil {
			return nil, fmt.Errorf("checkpoints.jsonl: %w", err)
		}
		out = append(out, cp)
	}
	slices.Reverse(out)
	return out, sc.Err()
}

// Restore makes the config files what they were at the checkpoint and records
// that as one entry. The current state is saved as a checkpoint first, so a
// restore can itself be undone. Packages and services need root: the commands
// come back as hint.
func (s *Store) Restore(name, by string) (hint string, err error) {
	all, err := s.Checkpoints()
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(all, func(cp Checkpoint) bool { return cp.Name == name || cp.Commit == name })
	if i < 0 {
		return "", fmt.Errorf("no checkpoint %q", name)
	}
	target := all[i]
	if risky, err := s.unsaved(); err != nil {
		return "", err
	} else if len(risky) > 0 {
		return "", fmt.Errorf("these files have changes a checkpoint cannot keep (secrets, binary or large files), so a restore would lose them: %s. Revert them or stop tracking them first", strings.Join(risky, ", "))
	}
	if _, err := s.Checkpoint("Before restoring " + target.Name); err != nil {
		return "", err
	}

	// Files of the checkpoint come back; files it did not have go away. The
	// paths are listed, as "." would also bring "~/" files into the config dir.
	inTree, err := s.git(nil, "ls-tree", "-r", "--name-only", "-z", target.Commit)
	if err != nil {
		return "", err
	}
	keep := splitNul(inTree)
	inIndex, err := s.git(nil, "ls-files", "-z")
	if err != nil {
		return "", err
	}
	var restore []string
	seen := map[string]bool{}
	for _, f := range append(slices.Clone(keep), splitNul(inIndex)...) {
		if !isHome(f) && !seen[f] {
			seen[f] = true
			restore = append(restore, f)
		}
	}
	if len(restore) > 0 {
		if _, err := s.git(nulJoin(restore), "restore", "--source="+target.Commit, "--worktree", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return "", err
		}
	}
	// Tracked files under $HOME come back too; one the checkpoint did not have
	// stays, as it may predate tracking it.
	home, err := s.recorded(target.Commit)
	if err != nil {
		return "", err
	}
	tracked, err := s.trackedFiles()
	if err != nil {
		return "", err
	}
	for name, body := range home {
		if current, ok := s.currentEntry(name); slices.Contains(tracked, name) && (!ok || !bytes.Equal(current, body)) {
			if err := s.writeEntry(name, body, false); err != nil {
				return "", err
			}
		}
	}
	extra, err := s.untracked()
	if err != nil {
		return "", err
	}
	for _, f := range extra {
		if !slices.Contains(keep, f) {
			if err := s.removeEntry(f); err != nil {
				return "", err
			}
		}
	}

	pending, err := s.Pending()
	if err != nil {
		return "", err
	}
	var ids []string
	for _, c := range pending {
		if c.Kind == "config" {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) > 0 {
		if _, err := s.Record(ids, "Restore checkpoint: "+target.Name, by); err != nil {
			return "", err
		}
	}

	nowPkgs, err := s.ListPackages()
	if err != nil {
		return "", err
	}
	nowUnits, err := s.ListUnits()
	if err != nil {
		return "", err
	}
	var manual []string
	// setChanges(now→checkpoint) lists what to install or enable to get back.
	for _, c := range append(setChanges("package", nowPkgs, target.Packages), setChanges("unit", nowUnits, target.Units)...) {
		manual = append(manual, redo(c))
	}
	if len(manual) > 0 {
		hint = "To match the checkpoint, run as root:\n  " + strings.Join(manual, "\n  ")
	}
	return hint, nil
}
