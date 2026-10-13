package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func newHomeStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, _ := newTestStore(t)
	s.HomeDir = filepath.Join(filepath.Dir(s.ConfigDir), "home")
	write(t, s.HomeDir, ".gitconfig", "[user]\nname = me\n")
	write(t, s.HomeDir, ".claude/settings.json", "{\"theme\": \"dark\"}\n")
	return s, s.HomeDir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTrackHomeFiles(t *testing.T) {
	s, home := newHomeStore(t)
	// A shell expands ~/x into an absolute path; both name the same file.
	if err := s.Track([]string{filepath.Join(home, ".gitconfig"), "~/.claude/settings.json"}); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.trackedFiles(); !slices.Equal(names, []string{"~/.claude/settings.json", "~/.gitconfig"}) {
		t.Fatalf("tracked %v", names)
	}
	if p, _ := s.Pending(); len(p) != 0 {
		t.Fatalf("tracking alone is not a change: %v", ids(p))
	}

	write(t, home, ".gitconfig", "[user]\nname = you\n")
	p, _ := s.Pending()
	if got := ids(p); !slices.Equal(got, []string{"modified ~/.gitconfig"}) || p[0].Added != 1 || p[0].Removed != 1 {
		t.Fatalf("pending %v %+v", got, p)
	}
	if g := Groups(p); g[0].Key != "~/.gitconfig" || p[0].Label != ".gitconfig" {
		t.Fatalf("group %+v", g)
	}
	var diff bytes.Buffer
	if err := s.Diff(&diff, []string{filepath.Join(home, ".gitconfig")}, false); err != nil {
		t.Fatal(err)
	}
	must(t, diff.String(), "--- a/~/.gitconfig")
	must(t, diff.String(), "+name = you")

	entry, err := s.Record([]string{filepath.Join(home, ".gitconfig")}, "rename me", "")
	if err != nil || entry.Commit == "" {
		t.Fatal(entry, err)
	}
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "~")); err == nil {
		t.Fatal("home files leaked into the config dir")
	}
	if p, _ := s.Pending(); len(p) != 0 {
		t.Fatalf("still pending %v", ids(p))
	}
	if found, _ := s.Search("name = you"); len(found) != 1 {
		t.Fatalf("search by diff found %d", len(found))
	}

	// Undo merges the entry back out, keeping later edits elsewhere.
	write(t, home, ".gitconfig", "[core]\neditor = nvim\n[user]\nname = you\n")
	if _, _, err := s.Undo(entry.Commit, ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(home, ".gitconfig")); got != "[core]\neditor = nvim\n[user]\nname = me\n" {
		t.Fatalf("undo left %q", got)
	}

	// Revert, removal and coming back.
	write(t, home, ".gitconfig", "oops\n")
	if err := s.Revert([]string{"~/.gitconfig"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(home, ".gitconfig")); got != "[core]\neditor = nvim\n[user]\nname = me\n" {
		t.Fatalf("revert left %q", got)
	}
	os.Chmod(filepath.Join(home, ".claude/settings.json"), 0o600)
	os.Remove(filepath.Join(home, ".claude/settings.json"))
	if p, _ := s.Pending(); !slices.Equal(ids(p), []string{"removed ~/.claude/settings.json"}) {
		t.Fatalf("pending %v", ids(p))
	}
	removal, _ := s.Record([]string{"~/.claude"}, "drop it", "")
	// Brought back by undo, a file that may have been private is created 0600.
	if _, _, err := s.Undo(removal.Commit, ""); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(home, ".claude/settings.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored as %v %v", info, err)
	}
	os.Remove(filepath.Join(home, ".claude/settings.json"))
	s.Record([]string{"~/.claude"}, "drop it again", "")
	write(t, home, ".claude/settings.json", "{}\n")
	if p, _ := s.Pending(); !slices.Equal(ids(p), []string{"added ~/.claude/settings.json"}) {
		t.Fatalf("pending %v", ids(p))
	}

	// A file that starts to hold a secret is left out, like in the config dir.
	write(t, home, ".gitconfig", "[user]\npassword = hunter2hunter2\n")
	if p, _ := s.Pending(); slices.Contains(ids(p), "modified ~/.gitconfig") {
		t.Fatal("secret recorded")
	}

	if err := s.Ignore("noisy/"); err != nil {
		t.Fatal(err)
	}
	if out, _ := s.git(nil, "ls-files", "~"); !strings.Contains(string(out), "~/.gitconfig") {
		t.Fatalf("ignore dropped tracked home files: %q", out)
	}
	if err := s.Ignore("~/.claude/settings.json"); err != nil { // the panel's ignore button
		t.Fatal(err)
	}
	if p, _ := s.Pending(); len(p) != 0 {
		t.Fatalf("untracked file still pending %v", ids(p))
	}
	if err := s.Untrack([]string{"~/.claude/settings.json"}); err == nil {
		t.Fatal("untracked twice")
	}
}

func TestTrackRefuses(t *testing.T) {
	s, home := newHomeStore(t)
	outside := t.TempDir()
	write(t, outside, "f", "x\n")
	write(t, home, ".ssh/config", "Host x\n")
	write(t, home, ".netrc-token", "x\n")
	write(t, home, ".npmrc", "_authToken=abcdefghijkl\n")
	write(t, home, "bin.dat", "a\x00b")
	os.Symlink(filepath.Join(outside, "f"), filepath.Join(home, "link"))
	os.Symlink(outside, filepath.Join(home, "dirlink"))
	os.Symlink(filepath.Join(home, ".gitconfig"), filepath.Join(home, "inner"))
	write(t, home, ".netrc", "machine x login me password hunter2\n")
	write(t, home, ".kube/config", "client-key-data: LS0tLS1\n")
	write(t, home, ".bash_history", "ls\n")
	syscall.Mkfifo(filepath.Join(home, "fifo"), 0o600)
	for _, p := range []string{
		filepath.Join(outside, "f"),   // outside $HOME
		"~/../f", "~/link", "~/inner", // escapes and symlinks
		"~/dirlink/f",                            // a symlinked parent leading outside
		"~/.claude",                              // a directory
		filepath.Join(s.ConfigDir, "git/config"), // already tracked as config
		filepath.Join(s.StateDir, "journal.jsonl"),
		"~/.ssh/config", "~/.netrc-token", "~/.npmrc", "~/bin.dat", "~/missing",
		"~/.netrc", "~/.kube/config", "~/.bash_history", "~/fifo",
	} {
		if err := s.Track([]string{p}); err == nil {
			t.Errorf("tracked %s", p)
		}
	}
	if names, _ := s.trackedFiles(); len(names) != 0 {
		t.Fatalf("tracked %v", names)
	}
	// Writes refuse the same paths, whatever a history or backup says.
	for _, p := range []string{"~/.ssh/authorized_keys", "~/dirlink/f", "~/../x"} {
		if err := s.writeEntry(p, []byte("x"), false); err == nil {
			t.Errorf("wrote %s", p)
		}
	}
	if err := s.writeEntry("~/.gitconfig", []byte("/etc/passwd"), true); err == nil {
		t.Error("made a home file a symlink")
	}
	if got := readFile(t, filepath.Join(outside, "f")); got != "x\n" {
		t.Fatalf("wrote outside home: %q", got)
	}
}

func TestCheckpointRestoreHomeFiles(t *testing.T) {
	s, home := newHomeStore(t)
	s.Track([]string{"~/.gitconfig"})
	write(t, home, ".gitconfig", "at checkpoint\n")
	cp, err := s.Checkpoint("cp")
	if err != nil {
		t.Fatal(err)
	}
	write(t, home, ".gitconfig", "later\n")
	s.Record(nil, "later", "")
	// Tracked after the checkpoint: restoring it must not delete the file.
	s.Track([]string{"~/.claude/settings.json"})
	if _, err := s.Restore(cp.Name, ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(home, ".gitconfig")); got != "at checkpoint\n" {
		t.Fatalf("restored %q", got)
	}
	readFile(t, filepath.Join(home, ".claude/settings.json"))

	// Untracked since: the checkpoint still has it, the config dir must not get it.
	s.Untrack([]string{"~/.gitconfig"})
	if _, err := s.Restore(cp.Name, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "~")); err == nil {
		t.Fatal("restore wrote home files into the config dir")
	}
}

func TestApplyHomeFiles(t *testing.T) {
	a, aHome := newHomeStore(t)
	a.Track([]string{"~/.gitconfig", "~/.claude/settings.json"})
	write(t, aHome, ".gitconfig", "[user]\nname = there\n")
	a.Record(nil, "setup", "")
	a.Untrack([]string{"~/.claude/settings.json"})
	remote := filepath.Join(t.TempDir(), "r.git")
	exec.Command("git", "init", "--bare", "--quiet", remote).Run()
	if err := a.SetRemote(remote); err != nil {
		t.Fatal(err)
	}

	b, bHome := newHomeStore(t)
	plan, err := b.PlanApply(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	var printed bytes.Buffer
	plan.Print(&printed)
	must(t, printed.String(), "overwrite ~/.gitconfig\n")
	if strings.Contains(printed.String(), "settings.json") {
		t.Fatalf("untracked there means delete here:\n%s", printed.String())
	}
	if err := b.Apply(plan, func(...string) error { return nil }, ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(bHome, ".gitconfig")); got != "[user]\nname = there\n" {
		t.Fatalf("applied %q", got)
	}
	if names, _ := b.trackedFiles(); !slices.Equal(names, []string{"~/.gitconfig"}) {
		t.Fatalf("tracked here %v", names)
	}
	if p, _ := b.Pending(); len(p) != 0 {
		t.Fatalf("pending after apply %v", ids(p))
	}
}
