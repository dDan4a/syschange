package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// apply brings a setup from a backup (syschange remote) to this machine: the
// config files changed there, the packages installed and the services
// enabled since tracking started there. It shows the plan first, saves a
// checkpoint, and records the result as one entry.

const cannotKeep = "holds something syschange cannot keep (a secret, binary or large file); move it away first"

const (
	actionAdd       = "add"
	actionOverwrite = "overwrite"
	actionDelete    = "delete"
)

type FileAction struct {
	Path   string `json:"path"`
	Action string `json:"action"` // add, overwrite, delete
	body   []byte
	link   bool
}

type Plan struct {
	Source   string       `json:"source"`
	Files    []FileAction `json:"files"`
	Repo     []string     `json:"repo"` // packages from the repositories
	AUR      []string     `json:"aur"`
	Units    []string     `json:"units"`    // "system x.service" / "user y.service"
	Removals []string     `json:"removals"` // packages removed there: shown, never removed here
	Skipped  int          `json:"skipped"`  // files already identical
	gitDir   string
}

func (p *Plan) Empty() bool {
	return len(p.Files) == 0 && len(p.Repo) == 0 && len(p.AUR) == 0 && len(p.Units) == 0
}

// Names from a backup end up as arguments of sudo pacman and systemctl: only
// plain names, never something that could pass for an option.
var (
	packageName = regexp.MustCompile(`^[a-zA-Z0-9@_+][a-zA-Z0-9@._+-]*$`)
	unitLine    = regexp.MustCompile(`^(system|user) [a-zA-Z0-9@_:\\][a-zA-Z0-9@._:\\-]*` +
		`\.(service|socket|timer|path|target|mount|automount|swap|slice)$`)
)

// fetchBackup clones the backup's branches into a temporary bare repository.
func fetchBackup(source string) (string, error) {
	dir, err := os.MkdirTemp("", "syschange-apply-")
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(context.Background(), "git", "clone", "--bare", "--quiet", "--", source, dir)

	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("fetch %s: %w: %s", source, err, strings.TrimSpace(string(out)))
	}

	return dir, nil
}

func gitAt(dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"--git-dir=" + dir, "--literal-pathspecs"}, args...)...)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}

	return out, nil
}

// stateLines reads a list file from the backup's state branch.
func stateLines(dir, name string) []string {
	out, err := gitAt(dir, "show", "state:"+name)
	if err != nil {
		return nil
	}

	var lines []string

	for l := range strings.SplitSeq(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}

	return lines
}

// PlanApply compares a backup with this machine. Call Cleanup when done.
func (s *Store) PlanApply(source string) (*Plan, error) {
	dir, err := fetchBackup(source)
	if err != nil {
		return nil, err
	}

	p := &Plan{Source: source, gitDir: dir}
	if err := s.fillPlan(p); err != nil {
		p.Cleanup()
		return nil, err
	}

	return p, nil
}

func (s *Store) fillPlan(p *Plan) error {
	if _, err := gitAt(p.gitDir, "rev-parse", "--verify", "--quiet", backupConfigBranch); err != nil {
		return errors.New(p.Source + " is not a syschange backup (no config branch)")
	}

	if err := s.planFiles(p); err != nil {
		return err
	}

	if err := s.planPackages(p); err != nil {
		return err
	}

	return s.planUnits(p)
}

func (s *Store) planFiles(p *Plan) error {
	root, err := gitAt(p.gitDir, "rev-list", "--max-parents=0", backupConfigBranch)
	if err != nil {
		return err
	}

	diff, err := gitAt(p.gitDir, "diff", "--name-status", "--no-renames", "-z", strings.Fields(string(root))[0], backupConfigBranch)
	if err != nil {
		return err
	}

	fields := splitNul(diff)
	for i := 0; i+1 < len(fields); i += 2 {
		if err := s.planFile(p, fields[i], fields[i+1]); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) planFile(p *Plan, status, path string) error {
	if _, _, err := s.rootFor(path); err != nil {
		return fmt.Errorf("the backup has an unsafe path %q: refusing it (%w)", path, err)
	}

	current, exists := s.currentEntry(path)
	if status == "D" {
		return s.planDelete(p, path, exists)
	}

	body, err := gitAt(p.gitDir, "show", backupConfigBranch+":"+path)
	if err != nil {
		return err
	}

	mode, err := gitAt(p.gitDir, "ls-tree", backupConfigBranch, "--", path)
	if err != nil {
		return err
	}

	fa := FileAction{Path: path, Action: actionAdd, body: body, link: strings.HasPrefix(string(mode), "120000")}
	if isHome(path) {
		if fa.link || !plainText(body) {
			return fmt.Errorf("the backup has %s as a symlink or with a secret: refusing it", path)
		}

		_, ok, there := s.homeContent(path)

		exists = exists || there // a file readHome refuses (a symlink, say) is still there
		if there && !ok && !bytes.Equal(current, body) {
			return fmt.Errorf("%s would be overwritten, but it %s", path, cannotKeep)
		}
	}

	if exists {
		if bytes.Equal(current, body) {
			p.Skipped++
			return nil
		}

		if !isHome(path) && !isLink(s.realPath(path)) && !s.trackable(path) {
			return fmt.Errorf("~/.config/%s would be overwritten, but it %s", path, cannotKeep)
		}

		fa.Action = actionOverwrite
	}

	p.Files = append(p.Files, fa)

	return nil
}

// Untracking there drops a home file from the history too: never delete it here.
func (s *Store) planDelete(p *Plan, path string, exists bool) error {
	if !exists || isHome(path) {
		return nil
	}

	if !isLink(s.realPath(path)) && !s.trackable(path) {
		return fmt.Errorf("~/.config/%s would be deleted, but it %s", path, cannotKeep)
	}

	p.Files = append(p.Files, FileAction{Path: path, Action: actionDelete})

	return nil
}

func (s *Store) planPackages(p *Plan) error {
	have, err := s.ListPackages()
	if err != nil {
		return err
	}

	if err := checkStateNames(p.gitDir); err != nil {
		return err
	}

	aur := stateLines(p.gitDir, "packages.aur.txt")
	for _, c := range setChanges(kindPackage, stateLines(p.gitDir, packagesList+".init.txt"), stateLines(p.gitDir, packagesList+".txt")) {
		switch {
		case c.Op == opRemoved:
			p.Removals = append(p.Removals, c.Name)
		case slices.Contains(have, c.Name):
		case slices.Contains(aur, c.Name):
			p.AUR = append(p.AUR, c.Name)
		default:
			p.Repo = append(p.Repo, c.Name)
		}
	}

	return nil
}

func checkStateNames(dir string) error {
	for _, list := range []string{"packages.txt", "packages.init.txt", "packages.aur.txt", "units.txt", "units.init.txt"} {
		valid := packageName
		if strings.HasPrefix(list, "units") {
			valid = unitLine
		}

		for _, name := range stateLines(dir, list) {
			if !valid.MatchString(name) {
				return fmt.Errorf("the backup lists an invalid name %q in %s: refusing it", name, list)
			}
		}
	}

	return nil
}

func (s *Store) planUnits(p *Plan) error {
	enabled, err := s.ListUnits()
	if err != nil {
		return err
	}

	for _, c := range setChanges(kindUnit, stateLines(p.gitDir, unitsList+".init.txt"), stateLines(p.gitDir, unitsList+".txt")) {
		if c.Op == opAdded && !slices.Contains(enabled, c.Name) {
			p.Units = append(p.Units, c.Name)
		}
	}

	return nil
}

func (p *Plan) Cleanup() {
	if p.gitDir != "" {
		_ = os.RemoveAll(p.gitDir)
	}
}

func (p *Plan) Print(w io.Writer) {
	fmt.Fprintf(w, "From %s:\n", p.Source)

	if p.Empty() {
		fmt.Fprintf(w, "  nothing to do, this machine already matches (%d files identical)\n", p.Skipped)
	}

	for _, f := range p.Files {
		path := f.Path
		if !isHome(path) {
			path = "~/.config/" + path
		}

		fmt.Fprintf(w, "  %-9s %s\n", f.Action, path)
	}

	if len(p.Repo) > 0 {
		fmt.Fprintf(w, "  install   %s\n", strings.Join(p.Repo, " "))
	}

	if len(p.AUR) > 0 {
		fmt.Fprintf(w, "  install   %s (AUR)\n", strings.Join(p.AUR, " "))
	}

	for _, u := range p.Units {
		fmt.Fprintf(w, "  enable    %s\n", u)
	}

	if len(p.Removals) > 0 {
		fmt.Fprintf(w, "  (removed there, left alone here: %s)\n", strings.Join(p.Removals, " "))
	}
}

// Apply runs a plan: checkpoint, files, packages, services, then one entry
// in the history. Commands that need root go through run with the terminal
// attached, so sudo can ask for the password.
func (s *Store) Apply(p *Plan, run func(args ...string) error, by string) error {
	if !s.Initialized() {
		if err := s.Init(); err != nil {
			return err
		}
	}

	if _, err := s.Checkpoint("Before applying " + p.Source); err != nil {
		return err
	}
	// Files under $HOME stay tracked here, as they were there.
	var home []string

	for _, f := range p.Files {
		if isHome(f.Path) && f.Action != actionDelete {
			home = append(home, f.Path)
		}
	}

	if len(home) > 0 {
		names, err := s.trackedFiles()
		if err != nil {
			return err
		}

		for _, name := range home {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}

		if err := writeLines(s.trackedFile(), names); err != nil {
			return err
		}
	}

	var ids []string

	for _, f := range p.Files {
		var err error
		if f.Action == actionDelete {
			err = s.removeEntry(f.Path)
		} else {
			err = s.writeEntry(f.Path, f.body, f.link)
		}

		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}

		ids = append(ids, f.Path)
	}

	if len(p.Repo) > 0 {
		if err := run(append([]string{"sudo", "pacman", "-S", "--needed", "--"}, p.Repo...)...); err != nil {
			return fmt.Errorf("installing packages: %w (config files are applied; run apply again after fixing)", err)
		}
	}

	if len(p.AUR) > 0 {
		if err := run(append([]string{"yay", "-S", "--needed", "--"}, p.AUR...)...); err != nil {
			return fmt.Errorf("installing AUR packages: %w", err)
		}
	}

	for _, u := range p.Units {
		scope, unit, _ := strings.Cut(u, " ")

		args := []string{"systemctl", "enable", "--now", "--", unit}
		if scope == scopeUser {
			args = slices.Insert(args, 1, "--user")
		} else {
			args = slices.Insert(args, 0, "sudo")
		}

		if err := run(args...); err != nil {
			return fmt.Errorf("enabling %s: %w", unit, err)
		}
	}

	for _, name := range append(append([]string{}, p.Repo...), p.AUR...) {
		ids = append(ids, "pkg:"+name)
	}

	for _, u := range p.Units {
		ids = append(ids, "unit:"+u)
	}

	pending, err := s.Pending()
	if err != nil {
		return err
	}
	// Record what actually changed; a package that was only a dependency here, say, is not pending.
	var present []string

	for _, id := range ids {
		if slices.ContainsFunc(pending, func(c Change) bool { return c.ID == id }) {
			present = append(present, id)
		}
	}

	if len(present) == 0 {
		return nil
	}

	_, err = s.Record(present, "Applied setup from "+p.Source, by)

	return err
}

// runAttached runs a command with the terminal, for sudo prompts.
func runAttached(args ...string) error {
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	return cmd.Run()
}
