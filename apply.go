package main

import (
	"bytes"
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
	unitLine    = regexp.MustCompile(`^(system|user) [a-zA-Z0-9@_:\\][a-zA-Z0-9@._:\\-]*\.(service|socket|timer|path|target|mount|automount|swap|slice)$`)
)

// fetchBackup clones the backup's branches into a temporary bare repository.
func fetchBackup(source string) (string, error) {
	dir, err := os.MkdirTemp("", "syschange-apply-")
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "clone", "--bare", "--quiet", "--", source, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("fetch %s: %v: %s", source, err, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

func gitAt(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--git-dir=" + dir, "--literal-pathspecs"}, args...)...)
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
	for _, l := range strings.Split(string(out), "\n") {
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
	if _, err := gitAt(dir, "rev-parse", "--verify", "--quiet", "config"); err != nil {
		p.Cleanup()
		return nil, errors.New(source + " is not a syschange backup (no config branch)")
	}

	// Config files: what changed there since tracking started.
	root, err := gitAt(dir, "rev-list", "--max-parents=0", "config")
	if err != nil {
		p.Cleanup()
		return nil, err
	}
	diff, err := gitAt(dir, "diff", "--name-status", "--no-renames", "-z", strings.Fields(string(root))[0], "config")
	if err != nil {
		p.Cleanup()
		return nil, err
	}
	fields := splitNul(diff)
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		if _, _, err := s.rootFor(path); err != nil {
			p.Cleanup()
			return nil, fmt.Errorf("the backup has an unsafe path %q: refusing it (%w)", path, err)
		}
		local := s.realPath(path)
		current, exists := s.currentEntry(path)
		if status == "D" {
			// Untracking there drops a home file from the history too: never delete it here.
			if exists && !isHome(path) {
				p.Files = append(p.Files, FileAction{Path: path, Action: "delete"})
			}
			continue
		}
		body, err := gitAt(dir, "show", "config:"+path)
		if err != nil {
			p.Cleanup()
			return nil, err
		}
		mode, err := gitAt(dir, "ls-tree", "config", "--", path)
		if err != nil {
			p.Cleanup()
			return nil, err
		}
		fa := FileAction{Path: path, Action: "add", body: body, link: strings.HasPrefix(string(mode), "120000")}
		if isHome(path) && (fa.link || !plainText(body)) {
			p.Cleanup()
			return nil, fmt.Errorf("the backup has %s as a symlink or with a secret: refusing it", path)
		}
		if isHome(path) {
			_, ok, there := s.homeContent(path)
			exists = exists || there // a file readHome refuses (a symlink, say) is still there
			if there && !ok && !bytes.Equal(current, body) {
				p.Cleanup()
				return nil, fmt.Errorf("%s would be overwritten, but it holds something syschange cannot keep (a secret, binary or large file); move it away first", path)
			}
		}
		if exists {
			if bytes.Equal(current, body) {
				p.Skipped++
				continue
			}
			if !isHome(path) && !isLink(local) && !s.trackable(path) {
				p.Cleanup()
				return nil, fmt.Errorf("~/.config/%s would be overwritten, but it holds something syschange cannot keep (a secret, binary or large file); move it away first", path)
			}
			fa.Action = "overwrite"
		}
		p.Files = append(p.Files, fa)
	}

	// Packages and services added there, and still missing here.
	have, err := s.ListPackages()
	if err != nil {
		p.Cleanup()
		return nil, err
	}
	for _, list := range []string{"packages.txt", "packages.init.txt", "packages.aur.txt", "units.txt", "units.init.txt"} {
		valid := packageName
		if strings.HasPrefix(list, "units") {
			valid = unitLine
		}
		for _, name := range stateLines(dir, list) {
			if !valid.MatchString(name) {
				p.Cleanup()
				return nil, fmt.Errorf("the backup lists an invalid name %q in %s: refusing it", name, list)
			}
		}
	}
	aur := stateLines(dir, "packages.aur.txt")
	for _, c := range setChanges("package", stateLines(dir, "packages.init.txt"), stateLines(dir, "packages.txt")) {
		switch {
		case c.Op == "removed":
			p.Removals = append(p.Removals, c.Name)
		case slices.Contains(have, c.Name):
		case slices.Contains(aur, c.Name):
			p.AUR = append(p.AUR, c.Name)
		default:
			p.Repo = append(p.Repo, c.Name)
		}
	}
	enabled, err := s.ListUnits()
	if err != nil {
		p.Cleanup()
		return nil, err
	}
	for _, c := range setChanges("unit", stateLines(dir, "units.init.txt"), stateLines(dir, "units.txt")) {
		if c.Op == "added" && !slices.Contains(enabled, c.Name) {
			p.Units = append(p.Units, c.Name)
		}
	}
	return p, nil
}

func (p *Plan) Cleanup() {
	if p.gitDir != "" {
		os.RemoveAll(p.gitDir)
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
		if isHome(f.Path) && f.Action != "delete" {
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
		if f.Action == "delete" {
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
		args := []string{"sudo", "systemctl", "enable", "--now", "--", unit}
		if scope == "user" {
			args = []string{"systemctl", "--user", "enable", "--now", "--", unit}
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
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
