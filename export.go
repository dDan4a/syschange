package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Export writes the history as Markdown: what is not recorded yet, every entry
// with its changes, and the steps to repeat the setup on a new machine.
func (s *Store) Export(w io.Writer) error {
	if !s.Initialized() {
		return ErrNotInitialized
	}

	history, err := s.History()
	if err != nil {
		return err
	}

	pending, err := s.Pending()
	if err != nil {
		return err
	}

	s.Attribute(pending)

	fmt.Fprintln(w, "# System changes")
	fmt.Fprintln(w)

	since := ""

	if out, err := s.git(nil, "log", "--reverse", "--format=%cI", "--max-parents=0", "HEAD"); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(out))); err == nil {
			since = ", tracked since " + t.Local().Format("2006-01-02") //nolint:gosmopolitan // the export is read in the user's own time zone
		}
	}

	fmt.Fprintf(w, "_Exported by syschange on %s%s._\n", time.Now().Format("2006-01-02 15:04"), since)

	if len(pending) > 0 {
		fmt.Fprintln(w, "\n## Not recorded yet")
		fmt.Fprintln(w)

		for _, g := range Groups(pending) {
			for _, c := range g.Changes {
				fmt.Fprintf(w, "- %s\n", changeLine(g.Title, c))
			}
		}
	}

	fmt.Fprintln(w, "\n## History")

	day := ""
	for _, e := range history {
		if d := e.Time.Local().Format("2006-01-02"); d != day { //nolint:gosmopolitan // the export is read in the user's own time zone
			day = d
			fmt.Fprintf(w, "\n### %s\n\n", d)
		}

		note := e.Note
		if note == "" {
			note = "_no description_"
		}

		if e.Undone {
			note = "~~" + note + "~~ (undone)"
		}

		if who := entryAuthors(e); who != "" {
			note += " · _" + who + "_"
		}

		fmt.Fprintf(w, "- **%s** %s\n", e.Time.Local().Format("15:04"), note) //nolint:gosmopolitan // the export is read in the user's own time zone

		for _, g := range Groups(e.Changes) {
			for _, c := range g.Changes {
				fmt.Fprintf(w, "  - %s\n", changeLine(g.Title, c))
			}
		}
	}

	return s.exportRepeat(w)
}

// changeLine: "Hyprland: Key bindings (`hypr/bindings.lua`), modified, by claude".
func changeLine(app string, c Change) string {
	line := app + ": " + c.Label
	switch c.Kind {
	case kindPackage:
		line = map[string]string{opAdded: "Installed ", opRemoved: "Removed "}[c.Op] + c.Label
	case kindUnit:
		line = map[string]string{opAdded: "Enabled ", opRemoved: "Disabled "}[c.Op] + c.Label
	default:
		line += " (`" + c.Name + "`), " + c.Op
	}

	if c.By != "" {
		line += ", by " + c.By
	}

	return line
}

func entryAuthors(e Entry) string {
	if e.By != "" {
		return e.By
	}

	var agents []string
	for _, c := range e.Changes {
		if c.By != "" && !slices.Contains(agents, c.By) {
			agents = append(agents, c.By)
		}
	}

	return strings.Join(agents, ", ")
}

// exportRepeat lists what changed since syschange was set up, as commands.
func (s *Store) exportRepeat(w io.Writer) error {
	var cmds []string

	for _, kind := range []struct{ name, file string }{{kindPackage, packagesList}, {kindUnit, unitsList}} {
		base, err := readLines(s.path(kind.file + ".init.txt"))
		if err != nil {
			return err
		}

		now, err := readLines(s.path(kind.file + ".txt"))
		if err != nil {
			return err
		}

		cmds = append(cmds, repeatCommands(setChanges(kind.name, base, now), foreignPackages())...)
	}

	files, err := s.git(nil, "diff", "--name-status", "--no-renames", "-z", rootCommit(s), "HEAD")
	if err != nil {
		return err
	}

	fields := splitNul(files)
	if len(cmds) == 0 && len(fields) == 0 {
		return nil
	}

	fmt.Fprintln(w, "\n## Repeat on a new machine")

	if len(cmds) > 0 {
		fmt.Fprintln(w, "\n```bash")

		for _, c := range cmds {
			fmt.Fprintln(w, c)
		}

		fmt.Fprintln(w, "```")
	}

	if len(fields) > 0 {
		fmt.Fprintln(w, "\nConfig files changed in `~/.config` since tracking started:")
		fmt.Fprintln(w)

		for i := 0; i+1 < len(fields); i += 2 {
			status := map[string]string{"A": "added", "M": "changed", "D": "removed", "T": "changed"}[fields[i]]
			fmt.Fprintf(w, "- %s `%s`\n", status, fields[i+1])
		}
	}

	return nil
}

func rootCommit(s *Store) string {
	out, err := s.git(nil, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return "HEAD"
	}

	return strings.Fields(string(out))[0]
}

// repeatCommands turns accepted changes into one command per kind of action.
func repeatCommands(changes []Change, aur []string) []string {
	var repo, fromAUR, remove, cmds []string

	for _, c := range changes {
		switch {
		case c.Kind == kindPackage && c.Op == opAdded && slices.Contains(aur, c.Name):
			fromAUR = append(fromAUR, c.Name)
		case c.Kind == kindPackage && c.Op == opAdded:
			repo = append(repo, c.Name)
		case c.Kind == kindPackage:
			remove = append(remove, c.Name)
		default:
			cmds = append(cmds, redo(c))
		}
	}

	if len(repo) > 0 {
		cmds = append([]string{"sudo pacman -S --needed " + strings.Join(repo, " ")}, cmds...)
	}

	if len(fromAUR) > 0 {
		cmds = append(cmds, "yay -S --needed "+strings.Join(fromAUR, " "))
	}

	if len(remove) > 0 {
		cmds = append(cmds, "sudo pacman -Rns "+strings.Join(remove, " "))
	}

	return cmds
}

// foreignPackages are installed packages not from a repository (AUR).
func foreignPackages() []string {
	out, err := exec.CommandContext(context.Background(), "pacman", "-Qqm").Output()
	if err != nil {
		return nil
	}

	return strings.Fields(string(out))
}

func redo(c Change) string {
	if c.Kind == kindPackage {
		if c.Op == opAdded {
			return "sudo pacman -S --needed " + c.Name
		}

		return "sudo pacman -Rns " + c.Name
	}

	return unitCommand(c.Name, c.Op != opRemoved)
}

// ExportTo writes the Markdown to a file, replacing it atomically.
func (s *Store) ExportTo(path string) error {
	var b bytes.Buffer
	if err := s.Export(&b); err != nil {
		return err
	}

	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved // a linked note stays linked
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// The history is private: keep the file's own mode, and start new ones private.
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	return writeAtomic(path, b.Bytes(), perm)
}

// SetAutoExport keeps a Markdown file in sync with the history: it is
// rewritten after every record. An empty path turns it off.
func (s *Store) SetAutoExport(path string) error {
	if !s.Initialized() {
		return ErrNotInitialized
	}

	if path == "" {
		if err := os.Remove(s.path("export-to")); err != nil && !os.IsNotExist(err) {
			return err
		}

		return nil
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Inside the tracked directory, every export would be a new change to record.
	if rel, err := filepath.Rel(s.ConfigDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is inside %s, which syschange tracks: pick a file elsewhere", abs, s.ConfigDir)
	}

	if err := os.WriteFile(s.path("export-to"), []byte(abs+"\n"), 0o600); err != nil {
		return err
	}

	return s.ExportTo(abs)
}

func (s *Store) autoExportPath() string {
	b, err := os.ReadFile(s.path("export-to"))
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(b))
}
