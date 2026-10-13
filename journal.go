package main

import (
	"bufio"
	"bytes"
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
	Time   time.Time `json:"time"`
	Note   string    `json:"note,omitempty"` // empty when the user skipped describing it
	By     string    `json:"by,omitempty"`   // who recorded it: an agent name, empty for the user
	Commit string    `json:"commit,omitempty"`
	Title  string    `json:"title"`            // the apps it touched: "Hyprland, Packages"
	Undoes string    `json:"undoes,omitempty"` // ID of the entry this one undid
	Undone bool      `json:"undone,omitempty"` // computed: a later entry undid this one
	// Touched (computed, Omarchy entries only) lists the files this entry
	// changed that you had edited before, and have not taken back since.
	Touched []string `json:"touched,omitempty"`
	Changes []Change `json:"changes"`
}

// Record accepts the selected pending changes into the history.
func (s *Store) Record(items []string, note, by string) (Entry, error) {
	return s.record(items, note, by, "")
}

func (s *Store) record(items []string, note, by, undoes string) (Entry, error) {
	pending, err := s.Pending()
	if err != nil {
		return Entry{}, err
	}
	chosen := Select(pending, s.canon(items))
	if len(chosen) == 0 {
		return Entry{}, errors.New("nothing to record: no pending change matches")
	}
	s.Attribute(chosen) // the history keeps which agent made each change
	entry := Entry{Time: time.Now(), Note: strings.TrimSpace(note), By: by, Title: groupTitles(chosen), Undoes: undoes, Changes: chosen}

	var paths, home []string
	for _, c := range chosen {
		switch {
		case c.Kind == "config" && isHome(c.Name):
			home = append(home, c.Name)
		case c.Kind == "config":
			paths = append(paths, c.Name)
		}
	}
	if len(paths) > 0 {
		if _, err := s.git(nulJoin(paths), "add", "--all", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return Entry{}, err
		}
	}
	if err := s.stageHome(nil, home); err != nil {
		return Entry{}, err
	}
	if len(paths)+len(home) > 0 {
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
	if err := s.appendJournal(entry); err != nil {
		return entry, err
	}
	s.afterChange()
	return entry, nil
}

// afterChange keeps the Markdown export and the backup remote in step with
// the history. The change is recorded either way; these only warn.
func (s *Store) afterChange() {
	if path := s.autoExportPath(); path != "" {
		if err := s.ExportTo(path); err != nil {
			fmt.Fprintln(os.Stderr, "syschange: auto export:", err)
		}
	}
	if s.SpawnPush != nil && s.RemoteURL() != "" {
		s.SpawnPush()
	}
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
	undoneFiles := map[string][]string{} // entry ID -> its files undone later
	for _, e := range entries {
		if e.Undoes != "" {
			undoneFiles[e.Undoes] = append(undoneFiles[e.Undoes], e.configFiles()...)
		}
	}
	edited := map[string]bool{} // files you (or your agents) changed so far
	for i, e := range entries {
		files, back := e.configFiles(), undoneFiles[e.ID()]
		entries[i].Undone = len(files) > 0 && !slices.ContainsFunc(files, func(f string) bool { return !slices.Contains(back, f) })
		for _, f := range files {
			switch {
			case e.By != "omarchy":
				edited[f] = true
			case edited[f] && !slices.Contains(back, f):
				entries[i].Touched = append(entries[i].Touched, f)
			}
		}
	}
	slices.Reverse(entries)
	return entries, sc.Err()
}

// Search returns the entries whose description, apps, author or changed
// names contain q, or whose config diff adds or removes it. Case is ignored.
func (s *Store) Search(q string) ([]Entry, error) {
	history, err := s.History()
	if err != nil || q == "" {
		return history, err
	}
	var commits []string
	if out, err := s.git(nil, "log", "--format=%H", "-i", "-S"+q); err == nil {
		commits = strings.Fields(string(out))
	}
	q = strings.ToLower(q)
	var found []Entry
	for _, e := range history {
		inDiff := e.Commit != "" && slices.ContainsFunc(commits, func(h string) bool { return strings.HasPrefix(h, e.Commit) })
		if inDiff || e.mentions(q) {
			found = append(found, e)
		}
	}
	return found, nil
}

func (e Entry) mentions(q string) bool {
	text := []string{e.Note, e.Title, e.By, e.Commit}
	for _, c := range e.Changes {
		text = append(text, c.ID, c.Label, c.By)
	}
	return strings.Contains(strings.ToLower(strings.Join(text, "\n")), q)
}

func (e Entry) configFiles() []string {
	var files []string
	for _, c := range e.Changes {
		if c.Kind == "config" {
			files = append(files, c.Name)
		}
	}
	return files
}

// ID identifies an entry: its time, which is unique in practice and also what
// the JSON shows.
func (e Entry) ID() string { return e.Time.Format(time.RFC3339Nano) }

// Undo reverses a recorded entry: its config changes are taken back out of the
// files and the result is recorded as a new entry. Later edits to the same
// lines make it fail rather than overwrite them. Packages and services need
// root, so their commands come back as hint. With only, just those files of
// the entry are taken back: say, the ones an Omarchy update changed under you.
func (s *Store) Undo(id, by string, only ...string) (Entry, string, error) {
	only = s.canon(only)
	history, err := s.History()
	if err != nil {
		return Entry{}, "", err
	}
	var target *Entry
	for i, e := range history {
		if e.ID() == id || (e.Commit != "" && len(id) >= 4 && strings.HasPrefix(e.Commit, id)) {
			target = &history[i]
			break
		}
	}
	if target == nil {
		return Entry{}, "", fmt.Errorf("no history entry %q", id)
	}
	if target.Undone {
		return Entry{}, "", errors.New("this entry is already undone")
	}
	var manual []string
	for _, c := range target.Changes {
		if c.Kind != "config" && len(only) == 0 {
			manual = append(manual, undoCommand(c))
		}
	}
	hint := ""
	if len(manual) > 0 {
		hint = "Run these yourself, they need root:\n  " + strings.Join(manual, "\n  ")
	}
	if target.Commit == "" {
		return Entry{}, hint, nil
	}
	names, err := s.git(nil, "show", "--name-only", "-z", "--no-renames", "--format=", target.Commit)
	if err != nil {
		return Entry{}, "", err
	}
	paths := splitNul(names)
	if len(only) > 0 {
		for _, p := range only {
			if !slices.Contains(paths, p) {
				return Entry{}, "", fmt.Errorf("%s is not part of this entry", p)
			}
		}
		paths = only
	}
	// Merge every file first and write only when all of them merge cleanly.
	merged := map[string][]byte{} // nil means delete the file
	links := map[string]bool{}    // restore as a symlink with this target
	for _, path := range paths {
		out, err := s.unmerge(target.Commit, path)
		if err != nil {
			return Entry{}, "", fmt.Errorf("%s changed again since, undo it by hand: %w", path, err)
		}
		merged[path] = out
		if mode, _ := s.git(nil, "ls-tree", target.Commit+"^", "--", path); strings.HasPrefix(string(mode), "120000") {
			links[path] = true
		}
	}
	for path, body := range merged {
		var err error
		if body == nil {
			err = s.removeEntry(path)
		} else {
			err = s.writeEntry(path, body, links[path])
		}
		if err != nil {
			return Entry{}, "", fmt.Errorf("%s: %w (files before it in this undo are already changed; see syschange status)", path, err)
		}
	}
	note := target.Note
	if note == "" {
		note = target.Title
	}
	if len(only) > 0 {
		note += " (" + strings.Join(only, ", ") + ")"
	}
	e, err := s.record(paths, "Undo: "+note, by, target.ID())
	return e, hint, err
}

// readEntry reads a file, or a symlink's target, the way git stores them.
func readEntry(path string) ([]byte, bool) {
	if isLink(path) {
		target, err := os.Readlink(path)
		return []byte(target), err == nil
	}
	b, err := os.ReadFile(path)
	return b, err == nil
}

func isLink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// unmerge takes one file's change in commit back out of its current content
// with a three-way merge, so later edits elsewhere in the file survive. It
// returns the new content, or nil when the file should be removed.
func (s *Store) unmerge(commit, path string) ([]byte, error) {
	blob := func(rev string) ([]byte, bool) {
		out, err := s.git(nil, "show", rev+":"+path)
		return out, err == nil
	}
	after, existedAfter := blob(commit)
	before, existedBefore := blob(commit + "^")
	current, exists := s.currentEntry(path)
	switch {
	case !existedBefore: // the entry created the file: remove it if untouched since
		if exists && !bytes.Equal(current, after) {
			return nil, errors.New("it was edited after it was created")
		}
		return nil, nil
	case !existedAfter: // the entry deleted the file: bring it back if still gone
		if exists {
			return nil, errors.New("it exists again")
		}
		return before, nil
	case !exists:
		return nil, errors.New("it was deleted")
	case isLink(s.realPath(path)):
		if !bytes.Equal(current, after) {
			return nil, errors.New("the link points elsewhere now")
		}
		return before, nil
	}
	tmp, err := os.MkdirTemp("", "syschange-undo-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	files := map[string][]byte{"current": current, "after": after, "before": before}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), body, 0o600); err != nil {
			return nil, err
		}
	}
	// Merge "before" into "current" over the common base "after": the entry's
	// own lines go back to how they were, everything else stays.
	cmd := exec.Command("git", "merge-file", "-p", "--quiet", filepath.Join(tmp, "current"), filepath.Join(tmp, "after"), filepath.Join(tmp, "before"))
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("later edits touch the same lines")
	}
	return out, nil
}

// Revert puts the selected config files back to their last recorded state.
// Packages and services need root, so for those it returns the command to run.
func (s *Store) Revert(items []string) error {
	pending, err := s.Pending()
	if err != nil {
		return err
	}
	chosen := Select(pending, s.canon(items))
	if len(chosen) == 0 {
		return errors.New("nothing to revert: no pending change matches")
	}
	var restore, manual []string
	for _, c := range chosen {
		switch {
		case c.Kind == "config" && c.Op == "added":
			if err := s.removeEntry(c.Name); err != nil {
				return err
			}
		case c.Kind == "config" && isHome(c.Name):
			body, err := s.git(nil, "show", "HEAD:"+c.Name)
			if err != nil {
				return err
			}
			if err := s.writeEntry(c.Name, body, false); err != nil {
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
	if isHome(pattern) { // the panel's ignore button on a tracked home file
		return s.Untrack([]string{pattern})
	}
	if err := appendLines(s.excludeFile(), []string{"/" + strings.TrimPrefix(pattern, "/")}); err != nil {
		return err
	}
	// Excludes do not apply to tracked files: untrack whatever the pattern now
	// matches, globs included.
	matched, err := s.git(nil, "ls-files", "-z", "--cached", "--ignored", "--exclude-standard")
	if err != nil {
		return err
	}
	// Tracked home files go with untrack: their "~" directory matches "*~".
	files := slices.DeleteFunc(splitNul(matched), isHome)
	if len(files) > 0 {
		if _, err := s.git(nulJoin(files), "rm", "--cached", "--quiet", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return err
		}
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
	for _, c := range Select(pending, s.canon(items)) {
		var cmd *exec.Cmd
		switch {
		case c.Kind == "config" && isHome(c.Name):
			var old, now []byte
			if c.Op != "added" {
				if old, err = s.git(nil, "show", "HEAD:"+c.Name); err != nil {
					return err
				}
			}
			if c.Op != "removed" {
				now, _, _ = s.homeContent(c.Name)
			}
			w.Write(diffBlobs(c.Name, old, now, colorFlag))
			continue
		case c.Kind != "config":
			sign := "+"
			if c.Op == "removed" {
				sign = "-"
			}
			fmt.Fprintf(w, "%s %s %s\n", sign, c.Kind, c.Label)
			continue
		case c.Op == "added":
			cmd = exec.Command("git", "diff", "--no-index", "--src-prefix=a/", "--dst-prefix=b/", colorFlag, "--", "/dev/null", c.Name)
			cmd.Dir = s.ConfigDir
		default:
			// Fixed prefixes: the user's diff.mnemonicPrefix or noprefix must not change the output.
			out, err := s.git(nil, "diff", "--src-prefix=a/", "--dst-prefix=b/", colorFlag, "HEAD", "--", c.Name)
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

func groupTitles(changes []Change) string {
	var titles []string
	for _, g := range Groups(changes) {
		titles = append(titles, g.Title)
	}
	return strings.Join(titles, ", ")
}
