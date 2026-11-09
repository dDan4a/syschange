package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

var (
	goldenTime   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})?`)
	goldenCommit = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	goldenSecs   = regexp.MustCompile(`\b1\d{9}\b`)
)

// TestCLIGolden runs every subcommand, its errors included, and compares the whole output byte for byte
// after masking temp paths, times and commit hashes. Regenerate with: go test -run TestCLIGolden -update
func TestCLIGolden(t *testing.T) {
	c := newCLI(t)
	home := os.Getenv("HOME")
	c.s.HomeDir = home
	pkgs := []string{"base", "git"}
	c.s.ListPackages = func() ([]string, error) { return slices.Clone(pkgs), nil }
	bin := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(bin, "grok"), []byte("#!/bin/sh\ngrep -q Waybar \"$2\" && exit 1\necho 'AI line'\n"), 0o755))
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	old := sendNotification
	sendNotification = func(Notification) error { return nil }

	t.Cleanup(func() { sendNotification = old })

	remote := filepath.Join(t.TempDir(), "b.git")
	bareRepo(t, remote)

	other := filepath.Join(t.TempDir(), "o.git")
	bareRepo(t, other)

	a, _ := newTestStore(t)
	write(t, a.ConfigDir, "kitty/kitty.conf", "from backup\n")
	write(t, a.ConfigDir, "mpv/mpv.conf", "new app\n")

	_, err := a.Record(nil, "backup", "")
	noErr(t, err)
	noErr(t, a.SetRemote(other))

	var log strings.Builder

	step := func(stdin string, args ...string) {
		out, err := c.run(stdin, args...)
		fmt.Fprintf(&log, "$ syschange %s\n%s", strings.Join(args, " "), out)

		if err != nil {
			fmt.Fprintf(&log, "error: %v\n", err)
		}

		log.WriteString("\n")
	}

	step("")
	step("", "--help")
	step("", "version")
	step("", "nope")
	step("", "status", "--bogus")
	step("", "init")
	step("", "status")
	step("", "status", "--json")

	write(t, c.s.ConfigDir, "hypr/bindings.conf", "bind = SUPER, R, exec, voxtype\n")
	write(t, c.s.ConfigDir, "kitty/kitty.conf", "font_size 12\n")
	write(t, c.s.ConfigDir, "waybar/config", "x\n")
	write(t, home, ".gitconfig", "[user]\n")

	step("", "status")
	step("", "status", "--json")
	step("", "diff", "hypr")
	step("", "diff", "--color", "kitty")
	step("", "suggest", "--agent", "grok", "hypr")
	step("", "suggest", "--agent", "opencode")
	step("", "note")
	step("", "note", "why", "nothing-here")
	step("", "note", "--by", "codex", "Voice typing", "hypr")
	step("", "skip", "kitty")
	step("", "track")
	step("", "track", "~/.gitconfig")
	step("", "track")
	step("", "status")
	write(t, home, ".gitconfig", "[user]\nname = me\n")
	step("", "diff", "~/.gitconfig")
	step("", "note", "Git name", "~/.gitconfig")
	step("", "log")
	step("", "log", "--grep", "voice")
	step("", "log", "--json", "--grep", "nothing like it")
	step("", "log", "--json")
	step("", "undo")
	step("", "undo", "ffffffff")

	h, err := c.s.History()
	noErr(t, err)
	step("", "undo", h[0].Commit)
	step("", "log")
	step("", "digest")
	step("", "digest", "--days", "3")
	step("", "notify", "--no-digest", "--remind-days", "0")
	step("", "notify", "--send")
	step("", "revert")
	step("", "revert", "waybar")
	write(t, c.s.ConfigDir, "waybar/config", "x\n")
	write(t, c.s.ConfigDir, "mako/config", "x\n")
	step("", "suggest", "--all", "--send", "--agent", "grok")
	step("", "suggest", "--all", "--agent", "grok")
	step("", "ignore")
	step("", "ignore", "waybar/")
	step("", "status")
	step("", "checkpoint", "before", "anim")
	step("", "checkpoints")
	write(t, c.s.ConfigDir, "hypr/bindings.conf", "experiment\n")
	write(t, home, ".gitconfig", "[user]\nname = experiment\n")
	write(t, c.s.ConfigDir, "foot/foot.ini", "new\n")

	pkgs = []string{"base", "git", "voxtype"}

	step("", "note", "Voxtype", "packages")
	step("", "restore")
	step("", "restore", "missing")
	step("", "restore", "before", "anim")
	step("", "status")
	step("", "log")
	step("", "untrack")
	step("", "untrack", "~/.gitconfig")
	step("", "track")
	step("", "export", "--to", filepath.Join(t.TempDir(), "out.md"))
	step("", "export", "--auto", filepath.Join(home, "auto.md"))
	step("", "export", "--auto", "off")
	step("", "export", "--auto", filepath.Join(c.s.ConfigDir, "x.md"))
	step("", "remote")
	step("", "push")
	step("", "remote", "bogus")
	step("", "remote", "set", remote)
	step("", "remote")
	step("", "push")
	step("", "push", "--quiet")
	step("", "remote", "off")
	step("", "apply")
	step("", "apply", remote)
	step("", "apply", other)
	step("", "apply", "--yes", other)
	step("", "hook")
	step("", "hook", "cursor")
	step("", "hook", "claude", "start")
	step(`{"session_id":"s"}`, "hook", "claude", "start")
	step(`{"session_id":"s","tool_use_id":"1"}`, "hook", "claude", "pre")
	write(t, c.s.ConfigDir, "hypr/bindings.conf", "by the agent\n")
	step(`{"session_id":"s","tool_use_id":"1"}`, "hook", "claude", "post")
	step(`{"session_id":"s"}`, "hook", "claude")
	step("", "hook", "omarchy", "theme-set", "x")
	step("", "agents")
	step("", "agents", "install")
	noErr(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	noErr(t, os.MkdirAll(filepath.Join(home, ".config", "omarchy", "defaults"), 0o755))
	noErr(t, os.WriteFile(filepath.Join(home, ".config", "omarchy", "defaults", "agent"), []byte("claude\n"), 0o644))
	step("", "agents", "status")
	step("", "agents", "install", "claude")
	step("", "status", "--json")
	step("", "agents", "remove")
	step("", "agents", "bogus")

	got := strings.ReplaceAll(log.String(), filepath.Dir(home), "<tmp>")
	got = goldenTime.ReplaceAllString(got, "<time>")
	got = goldenCommit.ReplaceAllString(got, "<commit>")
	got = goldenSecs.ReplaceAllString(got, "<unix>")

	path := filepath.Join("testdata", "cli.golden")

	if *update {
		noErr(t, os.MkdirAll("testdata", 0o755))
		noErr(t, os.WriteFile(path, []byte(got), 0o644))
	}

	want, err := os.ReadFile(path)
	noErr(t, err)

	if got != string(want) {
		gotPath := filepath.Join(t.TempDir(), "cli.golden")
		noErr(t, os.WriteFile(gotPath, []byte(got), 0o644))
		t.Fatalf("CLI output differs from %s, diff it with %s", path, gotPath)
	}
}
