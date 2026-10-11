package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cli runs the command line against a sandbox store.
type cli struct {
	t *testing.T
	s *Store
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	s, pkgs := newTestStore(t)
	_ = pkgs
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDECODE", "")
	t.Setenv("GEMINI_CLI", "")
	old := newStore
	newStore = func() (*Store, error) { return s, nil }
	t.Cleanup(func() { newStore = old })
	return &cli{t: t, s: s}
}

func (c *cli) run(stdin string, args ...string) (string, error) {
	var out bytes.Buffer
	err := run(args, strings.NewReader(stdin), &out)
	return out.String(), err
}

func (c *cli) ok(args ...string) string {
	c.t.Helper()
	out, err := c.run("", args...)
	if err != nil {
		c.t.Fatalf("syschange %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (c *cli) fails(want string, args ...string) {
	c.t.Helper()
	_, err := c.run("", args...)
	if err == nil || !strings.Contains(err.Error(), want) {
		c.t.Fatalf("syschange %s: err = %v, want it to mention %q", strings.Join(args, " "), err, want)
	}
}

func must(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Fatalf("output lacks %q:\n%s", want, out)
	}
}

func TestCLIBasics(t *testing.T) {
	c := newCLI(t)
	must(t, c.ok(), "Usage:")
	must(t, c.ok("help"), "syschange init")
	must(t, c.ok("version"), version)
	c.fails("unknown command", "nope")
	c.fails("flag provided but not defined", "status", "--bogus")
	must(t, c.ok("init"), "Tracking ")
	must(t, c.ok("status"), "Nothing new")

	var st statusJSON
	if err := json.Unmarshal([]byte(c.ok("status", "--json")), &st); err != nil || !st.Initialized || st.Version != version {
		t.Fatalf("status json %+v %v", st, err)
	}
}

func TestCLIRecordFlow(t *testing.T) {
	c := newCLI(t)
	write(t, c.s.ConfigDir, "hypr/bindings.conf", "bind = SUPER, R, exec, voxtype\n")
	write(t, c.s.ConfigDir, "kitty/kitty.conf", "font_size 12\n")

	out := c.ok("status")
	must(t, out, "Hyprland  [hypr]")
	must(t, out, "~ Key bindings  +1 -1")
	must(t, c.ok("diff", "hypr"), "+bind = SUPER, R, exec, voxtype")
	must(t, c.ok("diff", "--color", "kitty"), "\x1b[")

	c.fails("note needs a text", "note")
	c.fails("nothing to record", "note", "why", "nothing-here")
	must(t, c.ok("note", "--by", "codex", "Voice typing", "hypr"), "Recorded 1 change(s)")
	must(t, c.ok("skip", "kitty"), "without a description")
	c.fails("nothing to record", "skip")

	log := c.ok("log")
	must(t, log, "Voice typing  [codex]")
	must(t, log, "(no description)")

	// search: by description, by author, by a line of the diff (any case), as JSON
	if out := c.ok("log", "--grep", "voice"); !strings.Contains(out, "Voice typing") || strings.Contains(out, "no description") {
		t.Fatalf("grep voice:\n%s", out)
	}
	must(t, c.ok("log", "--grep", "codex"), "Voice typing")
	must(t, c.ok("log", "--grep", "VOXTYPE"), "Voice typing")
	if out := c.ok("log", "--grep", "font_size"); !strings.Contains(out, "no description") || strings.Contains(out, "Voice") {
		t.Fatalf("grep diff line:\n%s", out)
	}
	if out := c.ok("log", "--json", "--grep", "nothing like it"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("no match must be []: %q", out)
	}

	// undo by commit, then the log shows it undone
	var entry Entry
	h, _ := c.s.History()
	for _, e := range h {
		if e.Note == "Voice typing" {
			entry = e
		}
	}
	c.fails("undo takes one", "undo")
	must(t, c.ok("undo", entry.Commit), "Undid 1 config change(s)")
	must(t, c.ok("log"), "(undone)")
	c.fails("no history entry", "undo", "ffffffff")

	// revert and ignore
	write(t, c.s.ConfigDir, "hypr/bindings.conf", "changed again\n")
	c.fails("revert needs", "revert")
	if out, err := c.run("", "revert", "hypr/bindings.conf"); err != nil || out != "" {
		t.Fatalf("revert: %q %v", out, err)
	}
	write(t, c.s.ConfigDir, "noisy/state.json", "{}\n")
	c.fails("ignore takes one", "ignore")
	c.ok("ignore", "noisy/")
	must(t, c.ok("status"), "Nothing new")
}

func TestCLICheckpointsAndExport(t *testing.T) {
	c := newCLI(t)
	write(t, c.s.ConfigDir, "hypr/bindings.conf", "at checkpoint\n")
	must(t, c.ok("checkpoint", "before", "anim"), `Saved checkpoint "before anim"`)
	must(t, c.ok("checkpoints"), "before anim")
	write(t, c.s.ConfigDir, "hypr/bindings.conf", "experiment\n")
	c.fails("restore takes", "restore")
	c.fails("no checkpoint", "restore", "missing")
	must(t, c.ok("restore", "before", "anim"), "Config files restored")
	if b, _ := os.ReadFile(filepath.Join(c.s.ConfigDir, "hypr/bindings.conf")); string(b) != "at checkpoint\n" {
		t.Fatalf("restore: %q", b)
	}

	must(t, c.ok("export"), "# System changes")
	file := filepath.Join(t.TempDir(), "out.md")
	must(t, c.ok("export", "--to", file), "Exported to")
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("a new export must be private: %v %v", info, err)
	}
	os.Chmod(file, 0o640)
	must(t, c.ok("export", "--to", file), "Exported to")
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o640 {
		t.Fatalf("export changed the file's mode to %v", info.Mode())
	}
	must(t, c.ok("export", "--auto", file), "kept up to date")
	must(t, c.ok("export", "--auto", "off"), "Auto export is off")
	c.fails("inside", "export", "--auto", filepath.Join(c.s.ConfigDir, "x.md"))
}

func TestCLIRemoteAndApply(t *testing.T) {
	c := newCLI(t)
	must(t, c.ok("remote"), "No backup remote")
	c.fails("no backup remote", "push")
	c.fails("usage: syschange remote", "remote", "bogus")
	remote := filepath.Join(t.TempDir(), "b.git")
	exec.Command("git", "init", "--bare", "--quiet", remote).Run()
	must(t, c.ok("remote", "set", remote), "Keep that repository private")
	must(t, c.ok("remote"), "last push")
	must(t, c.ok("push"), "Pushed to")
	if out := c.ok("push", "--quiet"); out != "" {
		t.Fatalf("quiet push printed %q", out)
	}
	must(t, c.ok("remote", "off"), "Backup is off")

	c.fails("usage: syschange apply", "apply")
	must(t, c.ok("apply", remote), "nothing to do")
	c.fails("fetch", "apply", t.TempDir())
	empty := filepath.Join(t.TempDir(), "e.git")
	exec.Command("git", "init", "--bare", "--quiet", empty).Run()
	c.fails("not a syschange backup", "apply", empty)
	// A plan with work needs --yes when not on a terminal.
	other := filepath.Join(t.TempDir(), "o.git")
	exec.Command("git", "init", "--bare", "--quiet", other).Run()
	a, _ := newTestStore(t)
	write(t, a.ConfigDir, "kitty/kitty.conf", "x\n")
	a.Record(nil, "kitty", "")
	a.SetRemote(other)
	c.fails("--yes", "apply", other)
	must(t, c.ok("apply", "--yes", other), "Applied.")
}

func TestCLIHooksAndAgents(t *testing.T) {
	c := newCLI(t)
	c.fails("usage: syschange hook", "hook")
	c.fails("usage: syschange hook", "hook", "cursor")
	c.ok("hook", "claude", "start") // empty input: ignored, never an error
	c.ok("hook", "claude")          // same for the stop hook
	c.ok("hook", "claude", "pre")
	if _, err := c.run(`{"session_id":"s"}`, "hook", "claude", "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.run(`{"session_id":"s","tool_use_id":"1"}`, "hook", "claude", "pre"); err != nil {
		t.Fatal(err)
	}
	write(t, c.s.ConfigDir, "hypr/bindings.conf", "by the agent\n")
	if _, err := c.run(`{"session_id":"s","tool_use_id":"1"}`, "hook", "claude", "post"); err != nil {
		t.Fatal(err)
	}
	out, err := c.run(`{"session_id":"s"}`, "hook", "claude")
	if err != nil || !strings.Contains(out, "hypr/bindings.conf") {
		t.Fatalf("stop hook: %q %v", out, err)
	}
	must(t, c.ok("status"), "[claude]")
	if _, err := c.run("", "hook", "omarchy", "theme-set", "x"); err != nil {
		t.Fatal(err)
	}

	home := os.Getenv("HOME")
	must(t, c.ok("agents"), "No supported agent")
	c.fails("no supported agent", "agents", "install")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(home, ".config", "omarchy", "defaults"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "omarchy", "defaults", "agent"), []byte("claude\n"), 0o644)
	must(t, c.ok("agents", "status"), "claude (Omarchy default): not connected")
	must(t, c.ok("agents", "install", "claude"), "Connected: claude")
	must(t, c.ok("agents"), "claude (Omarchy default): connected")
	var st statusJSON
	json.Unmarshal([]byte(c.ok("status", "--json")), &st)
	if len(st.Agents) != 1 || !st.Agents[0].Connected || !st.Agents[0].Default {
		t.Fatalf("agents in status %+v", st.Agents)
	}
	must(t, c.ok("agents", "remove"), "Disconnected")
	c.fails("unknown agents action", "agents", "bogus")
}

func TestCLISuggest(t *testing.T) {
	c := newCLI(t)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && out=$2; shift; done\necho 'Short line' > \"$out\"\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "grok"), []byte("#!/bin/sh\necho 'from grok'\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "gemini"), []byte("#!/bin/sh\necho oops >&2\nexit 3\n"), 0o755)
	// An Omarchy stub that would install the agent on first run must never be run.
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/bash\nmise use -g --quiet \"claude\" || exit 1\nexec mise x claude -- claude \"$@\"\n"), 0o755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	write(t, c.s.ConfigDir, "hypr/bindings.conf", "x\n")
	must(t, c.ok("suggest", "--agent", "codex", "hypr"), "Short line")
	must(t, c.ok("suggest", "--agent", "grok"), "from grok")
	c.fails("gemini failed", "suggest", "--agent", "gemini")
	c.fails("not supported", "suggest", "--agent", "opencode") // it only takes the prompt as an argument
	c.fails("not installed", "suggest", "--agent", "claude")
	c.fails("not supported", "suggest", "--agent", "pi")
	c.fails("nothing to describe", "suggest", "--agent", "grok", "nope")
}

func TestCLINotInitialized(t *testing.T) {
	dir := t.TempDir()
	s := &Store{ConfigDir: dir, StateDir: filepath.Join(dir, "state"),
		ListPackages: func() ([]string, error) { return nil, nil }, ListUnits: func() ([]string, error) { return nil, nil }}
	old := newStore
	newStore = func() (*Store, error) { return s, nil }
	t.Cleanup(func() { newStore = old })
	var out bytes.Buffer
	if err := run([]string{"status"}, strings.NewReader(""), &out); err != ErrNotInitialized {
		t.Fatalf("status: %v", err)
	}
	out.Reset()
	if err := run([]string{"status", "--json"}, strings.NewReader(""), &out); err != nil || !strings.Contains(out.String(), `"initialized":false`) {
		t.Fatalf("status --json: %q %v", out.String(), err)
	}
	for _, args := range [][]string{{"export"}, {"ignore", "x"}, {"remote", "set", "x"}, {"checkpoint"}, {"export", "--auto", "f"}} {
		if err := run(args, strings.NewReader(""), &bytes.Buffer{}); err != ErrNotInitialized {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestPackageInstallTime(t *testing.T) {
	db := t.TempDir()
	old := pacmanDB
	pacmanDB = db
	t.Cleanup(func() { pacmanDB = old })
	when := time.Now().Add(-time.Hour).Unix()
	write(t, db, "voxtype-1.0-1/desc", "%NAME%\nvoxtype\n\n%INSTALLDATE%\n"+itoa(when)+"\n")
	write(t, db, "voxtype-extra-2.0-1/desc", "%NAME%\nvoxtype-extra\n\n%INSTALLDATE%\n1\n")
	if at, ok := packageInstallTime("voxtype"); !ok || at.Unix() != when {
		t.Fatalf("install time %v %v", at, ok)
	}
	if _, ok := packageInstallTime("missing"); ok {
		t.Fatal("missing package has an install time")
	}
	if !packageInstalledAfter("voxtype", time.Now().Add(-2*time.Hour)) || packageInstalledAfter("voxtype", time.Now()) {
		t.Fatal("packageInstalledAfter")
	}
}

func itoa(n int64) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + func() string { b, _ := json.Marshal(n); return string(b) }())
}

func TestCommandsForUndoAndRepeat(t *testing.T) {
	pkg := func(op, name string) Change { return itemChange("package", op, name) }
	unit := func(op, name string) Change { return itemChange("unit", op, name) }
	for _, tc := range []struct {
		c          Change
		undo, redo string
	}{
		{pkg("added", "vim"), "sudo pacman -Rns vim", "sudo pacman -S --needed vim"},
		{pkg("removed", "vim"), "sudo pacman -S vim", "sudo pacman -Rns vim"},
		{unit("added", "system sshd.service"), "sudo systemctl disable sshd.service", "sudo systemctl enable sshd.service"},
		{unit("removed", "user a.service"), "systemctl --user enable a.service", "systemctl --user disable a.service"},
	} {
		if got := undoCommand(tc.c); got != tc.undo {
			t.Errorf("undo %s: %q", tc.c.ID, got)
		}
		if got := redo(tc.c); got != tc.redo {
			t.Errorf("redo %s: %q", tc.c.ID, got)
		}
	}
	got := repeatCommands([]Change{pkg("added", "a"), pkg("added", "b-bin"), pkg("removed", "c"), unit("added", "user d.service")}, []string{"b-bin"})
	want := []string{"sudo pacman -S --needed a", "systemctl --user enable d.service", "yay -S --needed b-bin", "sudo pacman -Rns c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("repeat %q", got)
	}
}
