package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the review findings.

// evilBackup builds a backup whose config branch holds the given tree entries.
func evilBackup(t *testing.T, entries map[string]string, state map[string]string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "evil.git")
	exec.Command("git", "init", "--bare", "--quiet", repo).Run()
	g := func(stdin string, args ...string) string {
		cmd := exec.Command("git", append([]string{"--git-dir=" + repo}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=x", "GIT_AUTHOR_EMAIL=x@x", "GIT_COMMITTER_NAME=x", "GIT_COMMITTER_EMAIL=x@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	var tree func(files map[string]string) string
	tree = func(files map[string]string) string {
		var b strings.Builder
		dirs := map[string]map[string]string{}
		for name, body := range files {
			if dir, rest, nested := strings.Cut(name, "/"); nested {
				if dirs[dir] == nil {
					dirs[dir] = map[string]string{}
				}
				dirs[dir][rest] = body
				continue
			}
			blob := g(body, "hash-object", "-w", "--stdin")
			b.WriteString("100644 blob " + blob + "\t" + name + "\n")
		}
		for dir, sub := range dirs {
			b.WriteString("040000 tree " + tree(sub) + "\t" + dir + "\n")
		}
		return g(b.String(), "mktree", "--missing")
	}
	empty := g("", "mktree")
	root := g("", "commit-tree", empty, "-m", "init")
	tip := g("", "commit-tree", tree(entries), "-p", root, "-m", "evil")
	g("", "update-ref", "refs/heads/config", tip)
	if state == nil {
		state = map[string]string{"packages.txt": "", "packages.init.txt": ""}
	}
	g("", "update-ref", "refs/heads/state", g("", "commit-tree", tree(state), "-m", "s"))
	return repo
}

func TestApplyRefusesPathsOutsideConfig(t *testing.T) {
	s, _ := newTestStore(t)
	for _, name := range []string{"a/../../escape.txt", "a\x1b[2Kb"} {
		repo := evilBackup(t, map[string]string{name: "pwned\n"}, nil)
		if _, err := s.PlanApply(repo); err == nil || !strings.Contains(err.Error(), "unsafe path") {
			t.Errorf("%q: err = %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.ConfigDir), "escape.txt")); err == nil {
		t.Fatal("a file was written outside the config directory")
	}
}

func TestApplyRefusesOptionLikeNames(t *testing.T) {
	s, _ := newTestStore(t)
	for list, name := range map[string]string{
		"packages.txt": "--hookdir=/tmp/x",
		"units.txt":    "system --root=/tmp",
	} {
		repo := evilBackup(t, map[string]string{"k/a.conf": "x\n"}, map[string]string{list: name + "\n"})
		if _, err := s.PlanApply(repo); err == nil || !strings.Contains(err.Error(), "invalid name") {
			t.Errorf("%s %q: err = %v", list, name, err)
		}
	}
}

func TestApplyWillNotOverwriteAFileWithASecret(t *testing.T) {
	s, _ := newTestStore(t)
	write(t, s.ConfigDir, "app/conf.ini", "token = ghp_abcdefghijklmnopqrstuvwxyz0123\n")
	repo := evilBackup(t, map[string]string{"app/conf.ini": "plain\n"}, nil)
	if _, err := s.PlanApply(repo); err == nil || !strings.Contains(err.Error(), "cannot keep") {
		t.Fatalf("err = %v", err)
	}
}

func TestWritesNeverFollowSymlinks(t *testing.T) {
	s, _ := newTestStore(t)
	outside := filepath.Join(t.TempDir(), "dotfiles.conf")
	os.WriteFile(outside, []byte("mine\n"), 0o644)
	// A config file that is a link to a dotfiles copy is replaced, not written through.
	link := filepath.Join(s.ConfigDir, "app", "linked.conf")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink(outside, link)
	if err := s.writeEntry("app/linked.conf", []byte("restored\n"), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "mine\n" {
		t.Fatalf("wrote through the link: %q", b)
	}
	// A symlinked directory leading outside is refused.
	os.Symlink(filepath.Dir(outside), filepath.Join(s.ConfigDir, "out"))
	if err := s.writeEntry("out/x.conf", []byte("x"), false); err == nil {
		t.Fatal("wrote through a symlinked directory")
	}
	if err := s.writeEntry("../x", nil, false); err == nil || s.removeEntry("../x") == nil {
		t.Fatal("unsafe path accepted")
	}
}

func TestRestoreRefusesToLoseUnsavedChanges(t *testing.T) {
	s, _ := newTestStore(t)
	s.Checkpoint("clean")
	write(t, s.ConfigDir, "git/config", "[user]\n\tpassword = hunter2hunter2\n")
	if _, err := s.Restore("clean", ""); err == nil || !strings.Contains(err.Error(), "git/config") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.ConfigDir, "git/config")); !strings.Contains(string(b), "hunter2") {
		t.Fatal("the unsaved change was lost")
	}
}

func TestAgentConfigsSymlinkedAndForeignHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	dotfiles := filepath.Join(t.TempDir(), "settings.json")
	// A prompt-type hook has no "command": it must not break anything.
	os.WriteFile(dotfiles, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"prompt","prompt":"check"}]}]}}`), 0o644)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	settings := filepath.Join(home, ".claude", "settings.json")
	os.Symlink(dotfiles, settings)
	g := &Agents{Home: home, Exe: "/usr/bin/syschange"}
	if _, err := g.Install(); err != nil {
		t.Fatal(err)
	}
	if !isLink(settings) {
		t.Fatal("the symlinked settings.json became a plain file")
	}
	if b, _ := os.ReadFile(dotfiles); !strings.Contains(string(b), "hook claude") || !strings.Contains(string(b), `"prompt"`) {
		t.Fatalf("dotfiles copy: %s", b)
	}
	if bak, _ := os.ReadFile(dotfiles + ".syschange.bak"); strings.Contains(string(bak), "syschange") {
		t.Fatal("the backup is not the original")
	}
	if st, err := g.Status(); err != nil || !st[0].Connected {
		t.Fatalf("status %+v %v", st, err)
	}
	if err := g.Remove(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dotfiles); strings.Contains(string(b), "syschange") || !strings.Contains(string(b), `"prompt"`) {
		t.Fatalf("after remove: %s", b)
	}
}

func TestIgnoreGlobUntracksTrackedFiles(t *testing.T) {
	s, _ := newTestStore(t)
	write(t, s.ConfigDir, "hypr/extra.conf", "x\n")
	s.Record(nil, "", "")
	if err := s.Ignore("hypr/*.conf"); err != nil {
		t.Fatal(err)
	}
	write(t, s.ConfigDir, "hypr/bindings.conf", "changed\n")
	if p, _ := s.Pending(); len(p) != 0 {
		t.Fatalf("still pending: %v", ids(p))
	}
}

func TestSecretShapes(t *testing.T) {
	leaks := []string{
		"aws_secret_access_key = abcdefghijklmnop",
		"OPENAI_KEY=sk-abcdefghijklmnopqrstuvwxyz",
		"GH_PAT=ghp_abcdefghijklmnopqrstuvwxyz0123",
		"Authorization: Bearer abcdef123456",
		`{"auth":"dXNlcjpwYXNzd29yZA=="}`,
		"[url \"https://me:hunter2@github.com/\"]",
		"DATABASE_URL=postgres://u:pass@db/x",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"key = AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"xoxb-1234567890-abcdef",
	}
	for _, l := range leaks {
		if !looksSecret([]byte(l)) {
			t.Errorf("not caught: %s", l)
		}
	}
	for _, ok := range []string{
		"bind = SUPER, Q, killactive",
		"font_size 12",
		"auth = true",
		"theme = tokyo-night",
		"https://github.com/dDan4a/syschange",
		"--password-store=gnome-libsecret",
	} {
		if looksSecret([]byte(ok)) {
			t.Errorf("false positive: %s", ok)
		}
	}
}
