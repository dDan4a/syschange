package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Entry is one recorded set of changes, with the reason behind it.
type Entry struct {
	Time    time.Time `json:"time"`
	Note    string    `json:"note,omitempty"` // empty when the user skipped describing it
	By      string    `json:"by,omitempty"`   // who recorded it: an agent name, empty for the user
	Commit  string    `json:"commit,omitempty"`
	Title   string    `json:"title"` // the apps it touched: "Hyprland, Packages"
	Changes []Change  `json:"changes"`
}

// Record accepts the selected pending changes into the history.
func (s *Store) Record(items []string, note, by string) (Entry, error) {
	pending, err := s.Pending()
	if err != nil {
		return Entry{}, err
	}
	chosen := Select(pending, items)
	if len(chosen) == 0 {
		return Entry{}, errors.New("nothing to record: no pending change matches")
	}
	entry := Entry{Time: time.Now(), Note: strings.TrimSpace(note), By: by, Title: groupTitles(chosen), Changes: chosen}

	var paths []string
	for _, c := range chosen {
		if c.Kind == "config" {
			paths = append(paths, c.Name)
		}
	}
	if len(paths) > 0 {
		if _, err := s.git(nulJoin(paths), "add", "--all", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return Entry{}, err
		}
		msg := entry.Note
		if msg == "" {
			msg = "(no description)"
		}
		if _, err := s.git(nil, "commit", "--quiet", "-m", msg); err != nil {
			return Entry{}, err
		}
		head, err := s.git(nil, "rev-parse", "--short", "HEAD")
		if err != nil {
			return Entry{}, err
		}
		entry.Commit = strings.TrimSpace(string(head))
	}
	for kind, file := range map[string]string{"package": "packages.txt", "unit": "units.txt"} {
		if err := s.acceptItems(file, kind, chosen); err != nil {
			return Entry{}, err
		}
	}
	return entry, s.appendJournal(entry)
}

func (s *Store) acceptItems(file, kind string, chosen []Change) error {
	accepted, err := readLines(s.path(file))
	if err != nil {
		return err
	}
	touched := false
	for _, c := range chosen {
		if c.Kind != kind {
			continue
		}
		touched = true
		if c.Op == "added" {
			accepted = append(accepted, c.Name)
		} else {
			accepted = slices.DeleteFunc(accepted, func(n string) bool { return n == c.Name })
		}
	}
	if !touched {
		return nil
	}
	return writeLines(s.path(file), accepted)
}

func (s *Store) appendJournal(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return appendLines(s.path("journal.jsonl"), []string{string(b)})
}

// History returns recorded entries, newest first.
func (s *Store) History() ([]Entry, error) {
	f, err := os.Open(s.path("journal.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("journal.jsonl: %w", err)
		}
		entries = append(entries, e)
	}
	slices.Reverse(entries)
	return entries, sc.Err()
}

// Revert puts the selected config files back to their last recorded state.
// Packages and services need root, so for those it returns the command to run.
func (s *Store) Revert(items []string) error {
	pending, err := s.Pending()
	if err != nil {
		return err
	}
	chosen := Select(pending, items)
	if len(chosen) == 0 {
		return errors.New("nothing to revert: no pending change matches")
	}
	var restore, manual []string
	for _, c := range chosen {
		switch {
		case c.Kind == "config" && c.Op == "added":
			// Paths come from git status, so they are always inside ConfigDir.
			if err := os.Remove(filepath.Join(s.ConfigDir, c.Name)); err != nil {
				return err
			}
		case c.Kind == "config":
			restore = append(restore, c.Name)
		default:
			manual = append(manual, undoCommand(c))
		}
	}
	if len(restore) > 0 {
		if _, err := s.git(nulJoin(restore), "checkout", "HEAD", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return err
		}
	}
	if len(manual) > 0 {
		return fmt.Errorf("these need root, run them yourself:\n  %s", strings.Join(manual, "\n  "))
	}
	return nil
}

func undoCommand(c Change) string {
	if c.Kind == "package" {
		if c.Op == "added" {
			return "sudo pacman -Rns " + c.Name
		}
		return "sudo pacman -S " + c.Name
	}
	scope, unit, _ := strings.Cut(c.Name, " ")
	verb := "disable"
	if c.Op == "removed" {
		verb = "enable"
	}
	if scope == "user" {
		return "systemctl --user " + verb + " " + unit
	}
	return "sudo systemctl " + verb + " " + unit
}

// Ignore stops tracking a path for good: it is excluded and dropped from the history's index.
func (s *Store) Ignore(pattern string) error {
	if !s.Initialized() {
		return ErrNotInitialized
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "#") {
		return errors.New("empty pattern")
	}
	if err := appendLines(s.excludeFile(), []string{"/" + strings.TrimPrefix(pattern, "/")}); err != nil {
		return err
	}
	if _, err := s.git(nil, "rm", "-r", "--cached", "--quiet", "--ignore-unmatch", "--", strings.TrimPrefix(pattern, "/")); err != nil {
		return err
	}
	staged, err := s.git(nil, "diff", "--cached", "--name-only")
	if err != nil || len(staged) == 0 {
		return err
	}
	_, err = s.git(nil, "commit", "--quiet", "-m", "ignore "+pattern)
	return err
}

// Diff writes a unified diff of the selected config changes.
func (s *Store) Diff(w io.Writer, items []string, color bool) error {
	pending, err := s.Pending()
	if err != nil {
		return err
	}
	colorFlag := "--color=never"
	if color {
		colorFlag = "--color=always"
	}
	for _, c := range Select(pending, items) {
		var cmd *exec.Cmd
		switch {
		case c.Kind != "config":
			sign := "+"
			if c.Op == "removed" {
				sign = "-"
			}
			fmt.Fprintf(w, "%s %s %s\n", sign, c.Kind, c.Label)
			continue
		case c.Op == "added":
			cmd = exec.Command("git", "diff", "--no-index", colorFlag, "--", "/dev/null", filepath.Join(s.ConfigDir, c.Name))
		default:
			out, err := s.git(nil, "diff", colorFlag, "HEAD", "--", c.Name)
			if err != nil {
				return err
			}
			w.Write(out)
			continue
		}
		cmd.Stdout = w
		_ = cmd.Run() // exit status 1 just means "files differ"
	}
	return nil
}

// Export writes Markdown: what to repeat on a new machine, then the history.
func (s *Store) Export(w io.Writer) error {
	if !s.Initialized() {
		return ErrNotInitialized
	}
	history, err := s.History()
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "# System changes")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Repeat on a new machine")
	fmt.Fprintln(w)
	for _, kind := range []struct{ name, file string }{{"package", "packages"}, {"unit", "units"}} {
		base, err := readLines(s.path(kind.file + ".init.txt"))
		if err != nil {
			return err
		}
		now, err := readLines(s.path(kind.file + ".txt"))
		if err != nil {
			return err
		}
		for _, c := range setChanges(kind.name, base, now) {
			fmt.Fprintf(w, "- `%s`\n", redo(c))
		}
	}
	files, err := s.git(nil, "diff", "--name-status", "--no-renames", rootCommitRange(s))
	if err != nil {
		return err
	}
	if lines := strings.TrimSpace(string(files)); lines != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Config files changed (in `~/.config`):")
		fmt.Fprintln(w)
		for _, line := range strings.Split(lines, "\n") {
			status, path, _ := strings.Cut(line, "\t")
			fmt.Fprintf(w, "- %s `%s`\n", map[string]string{"A": "added", "M": "changed", "D": "removed"}[status], path)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## History")
	day := ""
	for _, e := range history {
		if d := e.Time.Format("2006-01-02"); d != day {
			day = d
			fmt.Fprintf(w, "\n### %s\n\n", d)
		}
		note := e.Note
		if note == "" {
			note = "_no description_"
		}
		if e.By != "" {
			note += " (by " + e.By + ")"
		}
		fmt.Fprintf(w, "- %s %s: %s\n", e.Time.Format("15:04"), note, groupTitles(e.Changes))
	}
	return nil
}

func groupTitles(changes []Change) string {
	var titles []string
	for _, g := range Groups(changes) {
		titles = append(titles, g.Title)
	}
	return strings.Join(titles, ", ")
}

func rootCommitRange(s *Store) string {
	out, err := s.git(nil, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return "HEAD"
	}
	return strings.Fields(string(out))[0] + "..HEAD"
}

func redo(c Change) string {
	if c.Kind == "package" {
		if c.Op == "added" {
			return "sudo pacman -S --needed " + c.Name
		}
		return "sudo pacman -Rns " + c.Name
	}
	scope, unit, _ := strings.Cut(c.Name, " ")
	verb := "enable"
	if c.Op == "removed" {
		verb = "disable"
	}
	if scope == "user" {
		return "systemctl --user " + verb + " " + unit
	}
	return "sudo systemctl " + verb + " " + unit
}
