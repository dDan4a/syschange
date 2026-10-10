package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, *[]string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	write(t, cfg, "hypr/bindings.conf", "bind = SUPER, Q, killactive\n")
	write(t, cfg, "git/config", "[user]\nname = me\n")
	write(t, cfg, "chromium/Default/Preferences", "{}")
	write(t, cfg, "app/Profile/Local Storage/x", "state")
	write(t, cfg, "gh/hosts.yml", "oauth_token: abc\n")
	write(t, cfg, "db/config.toml", "password = \"hunter2hunter2\"\n")
	write(t, cfg, "big.bin", "a\x00b")
	pkgs := []string{"base", "git"}
	s := &Store{
		ConfigDir:    cfg,
		StateDir:     filepath.Join(dir, "state"),
		ListPackages: func() ([]string, error) { return slices.Clone(pkgs), nil },
		ListUnits:    func() ([]string, error) { return []string{"system sshd.service"}, nil },
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	return s, &pkgs
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ids(changes []Change) []string {
	var out []string
	for _, c := range changes {
		out = append(out, c.Op+" "+c.ID)
	}
	slices.Sort(out)
	return out
}

func TestInitTracksOnlySetup(t *testing.T) {
	s, _ := newTestStore(t)
	out, err := s.git(nil, "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(out))
	want := []string{"git/config", "hypr/bindings.conf"}
	if !slices.Equal(got, want) {
		t.Fatalf("tracked %v, want %v (profiles, secrets and binaries stay out)", got, want)
	}
	if p, _ := s.Pending(); len(p) != 0 {
		t.Fatalf("fresh init has pending changes: %v", ids(p))
	}
}

func TestRecordRevertIgnore(t *testing.T) {
	s, pkgs := newTestStore(t)
	write(t, s.ConfigDir, "hypr/bindings.conf", "bind = SUPER, Q, killactive\nbind = SUPER, R, exec, voxtype\n")
	write(t, s.ConfigDir, "voxtype/config.toml", "model = \"base\"\n")
	os.Remove(filepath.Join(s.ConfigDir, "git/config"))
	*pkgs = append(*pkgs, "voxtype")

	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"added pkg:voxtype", "added voxtype/config.toml", "modified hypr/bindings.conf", "removed git/config"}
	if got := ids(pending); !slices.Equal(got, want) {
		t.Fatalf("pending %v, want %v", got, want)
	}
	groups := Groups(pending)
	if groups[0].Title != "Git" || groups[1].Title != "Hyprland" || groups[len(groups)-1].Key != "packages" {
		t.Fatalf("unexpected group order: %+v", groups)
	}
	if c := Select(pending, []string{"hypr"})[0]; c.Label != "Key bindings" || c.Added != 1 {
		t.Fatalf("hypr change = %+v", c)
	}

	// Record voxtype only: its package and its config.
	e, err := s.Record([]string{"voxtype", "voxtype/"}, "Voice typing", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(e.Changes); !slices.Equal(got, []string{"added pkg:voxtype", "added voxtype/config.toml"}) {
		t.Fatalf("recorded %v", got)
	}
	pending, _ = s.Pending()
	if got := ids(pending); !slices.Equal(got, []string{"modified hypr/bindings.conf", "removed git/config"}) {
		t.Fatalf("after record %v", got)
	}

	// Revert brings back the edited and the deleted file.
	if err := s.Revert([]string{"hypr/bindings.conf", "git"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.ConfigDir, "hypr/bindings.conf")); string(b) != "bind = SUPER, Q, killactive\n" {
		t.Fatalf("bindings not reverted: %q", b)
	}
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "git/config")); err != nil {
		t.Fatal("git/config not restored")
	}

	// A new file can be reverted (deleted) or ignored for good.
	write(t, s.ConfigDir, "noise/state.json", "{}")
	if err := s.Ignore("noise/"); err != nil {
		t.Fatal(err)
	}
	write(t, s.ConfigDir, "hypr/extra.conf", "x\n")
	if err := s.Revert([]string{"hypr/extra.conf"}); err != nil {
		t.Fatal(err)
	}
	if pending, _ = s.Pending(); len(pending) != 0 {
		t.Fatalf("expected clean state, got %v", ids(pending))
	}

	history, err := s.History()
	if err != nil || len(history) != 1 || history[0].Note != "Voice typing" || history[0].By != "claude" {
		t.Fatalf("history %+v %v", history, err)
	}
	var md bytes.Buffer
	if err := s.Export(&md); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sudo pacman -S --needed voxtype", "added `voxtype/config.toml`", "Voice typing (by claude): Voxtype, Packages"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("export lacks %q:\n%s", want, md.String())
		}
	}
}

func TestRevertPackageNeedsRoot(t *testing.T) {
	s, pkgs := newTestStore(t)
	*pkgs = append(*pkgs, "voxtype")
	err := s.Revert([]string{"packages"})
	if err == nil || !strings.Contains(err.Error(), "sudo pacman -Rns voxtype") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopHook(t *testing.T) {
	s, _ := newTestStore(t)
	old := filepath.Join(s.ConfigDir, "hypr/bindings.conf")
	write(t, s.ConfigDir, "hypr/bindings.conf", "changed before the session\n")
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)

	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	start := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	os.WriteFile(transcript, []byte(`{"type":"summary"}`+"\n"+`{"timestamp":"`+start+`"}`+"\n"), 0o600)
	hook := func(active bool) string {
		in, _ := json.Marshal(map[string]any{"stop_hook_active": active, "transcript_path": transcript})
		var out bytes.Buffer
		if err := s.StopHook("claude", bytes.NewReader(in), &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	if out := hook(false); out != "" {
		t.Fatalf("a change made before the session must not block: %s", out)
	}
	write(t, s.ConfigDir, "voxtype/config.toml", "x\n")
	out := hook(false)
	if !strings.Contains(out, `"decision":"block"`) || !strings.Contains(out, "voxtype/config.toml") || strings.Contains(out, "bindings") {
		t.Fatalf("hook output: %s", out)
	}
	if out := hook(true); out != "" {
		t.Fatalf("must not block twice: %s", out)
	}
}

func TestFileLabel(t *testing.T) {
	for path, want := range map[string]string{
		"hypr/bindings.lua":               "Key bindings",
		"hypr/foo.conf":                   "foo.conf",
		"omarchy/themes/ghost/x.toml":     "Theme ghost: x.toml",
		"systemd/user/voxtype.service":    "User service voxtype.service",
		"omarchy/plugins/dan4a.syschange": "Plugin dan4a.syschange",
		"starship.toml":                   "starship.toml",
	} {
		if got := fileLabel(path); got != want {
			t.Errorf("fileLabel(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestAgentsInstallRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	write(t, home, ".claude/settings.json", `{"model": "opus", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "other"}]}]}}`)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o755)
	os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	g := &Agents{Home: home, Exe: "/opt/my bin/syschange"}

	for range 2 { // installing twice changes nothing
		done, err := g.Install()
		if err != nil || !slices.Equal(done, []string{"claude", "codex", "gemini", "grok"}) {
			t.Fatalf("install: %v %v", done, err)
		}
	}
	states, err := g.Status()
	if err != nil || len(states) != 4 || slices.ContainsFunc(states, func(s AgentState) bool { return !s.Connected }) {
		t.Fatalf("status %+v %v", states, err)
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if !strings.HasPrefix(string(settings), "{\n  \"model\"") || strings.Count(string(settings), "hook claude\"") != 1 ||
		strings.Count(string(settings), "hook claude start") != 1 ||
		!strings.Contains(string(settings), `"'/opt/my bin/syschange' hook claude"`) || !strings.Contains(string(settings), `"other"`) {
		t.Fatalf("settings.json:\n%s", settings)
	}
	for _, p := range []string{".agents/skills/syschange/SKILL.md", ".grok/skills/syschange/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Fatalf("skill not linked: %s", p)
		}
	}
	gemini, _ := os.ReadFile(filepath.Join(home, ".gemini/settings.json"))
	if !strings.Contains(string(gemini), `"AfterAgent"`) || !strings.Contains(string(gemini), "30000") {
		t.Fatalf("gemini settings:\n%s", gemini)
	}
	if _, err := os.Stat(filepath.Join(home, ".grok/hooks/syschange.json")); err != nil {
		t.Fatal("grok hook file missing")
	}

	if err := g.Remove(); err != nil {
		t.Fatal(err)
	}
	settings, _ = os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if strings.Contains(string(settings), "syschange") || !strings.Contains(string(settings), `"other"`) {
		t.Fatalf("after remove:\n%s", settings)
	}
	if states, _ := g.Status(); slices.ContainsFunc(states, func(s AgentState) bool { return s.Connected }) {
		t.Fatalf("still connected: %+v", states)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude/skills/syschange")); err == nil {
		t.Fatal("skill link left behind")
	}
	if _, err := os.Stat(filepath.Join(home, ".grok/hooks/syschange.json")); err == nil {
		t.Fatal("grok hook file left behind")
	}
	if gemini, _ := os.ReadFile(filepath.Join(home, ".gemini/settings.json")); strings.Contains(string(gemini), "AfterAgent") {
		t.Fatalf("gemini hook left behind:\n%s", gemini)
	}
}

func TestStopHookGrokAndGemini(t *testing.T) {
	s, _ := newTestStore(t)
	run := func(agent string, start bool, payload string) string {
		var out bytes.Buffer
		var err error
		if start {
			err = s.SessionStartHook(agent, strings.NewReader(payload))
		} else {
			err = s.StopHook(agent, strings.NewReader(payload), &out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	// Grok: camelCase, no transcript, start time comes from the SessionStart hook.
	run("grok", true, `{"sessionId":"g-1"}`)
	write(t, s.ConfigDir, "hypr/bindings.conf", "changed\n")
	if out := run("grok", false, `{"sessionId":"g-1","stopHookActive":false}`); !strings.Contains(out, `"decision":"block"`) || !strings.Contains(out, "--by grok") {
		t.Fatalf("grok: %s", out)
	}
	if out := run("grok", false, `{"sessionId":"g-1","stopHookActive":true}`); out != "" {
		t.Fatalf("grok must not block twice: %s", out)
	}
	if out := run("grok", false, `{"sessionId":"unknown"}`); out != "" {
		t.Fatalf("unknown session must not block: %s", out)
	}
	// Gemini retries with "deny"; a second SessionStart (resume) keeps the first time.
	run("gemini", true, `{"session_id":"m-1"}`)
	first, _ := os.ReadFile(s.sessionFile("gemini", "m-1"))
	run("gemini", true, `{"session_id":"m-1"}`)
	if again, _ := os.ReadFile(s.sessionFile("gemini", "m-1")); string(again) != string(first) {
		t.Fatal("resume reset the session start")
	}
	write(t, s.ConfigDir, "voxtype/config.toml", "x\n")
	if out := run("gemini", false, `{"session_id":"m-1"}`); !strings.Contains(out, `"decision":"deny"`) || !strings.Contains(out, "voxtype/config.toml") {
		t.Fatalf("gemini: %s", out)
	}
}

func TestUndoRecordedEntry(t *testing.T) {
	s, _ := newTestStore(t)
	bindings := filepath.Join(s.ConfigDir, "hypr/bindings.conf")
	write(t, s.ConfigDir, "hypr/bindings.conf", "bind = SUPER, Q, killactive\nbind = SUPER, R, exec, voxtype\n")
	write(t, s.ConfigDir, "voxtype/config.toml", "model = \"base\"\n")
	e, err := s.Record(nil, "Voice typing", "")
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated later edit in the same file survives the undo.
	write(t, s.ConfigDir, "hypr/bindings.conf", "# mine\nbind = SUPER, Q, killactive\nbind = SUPER, R, exec, voxtype\n")
	if _, err := s.Record(nil, "Comment", ""); err != nil {
		t.Fatal(err)
	}

	undo, hint, err := s.Undo(e.ID(), "")
	if err != nil || hint != "" {
		t.Fatalf("undo: %v %q", err, hint)
	}
	if b, _ := os.ReadFile(bindings); string(b) != "# mine\nbind = SUPER, Q, killactive\n" {
		t.Fatalf("bindings after undo: %q", b)
	}
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "voxtype/config.toml")); err == nil {
		t.Fatal("file added by the entry was not removed")
	}
	if undo.Note != "Undo: Voice typing" || undo.Undoes != e.ID() {
		t.Fatalf("undo entry %+v", undo)
	}
	history, _ := s.History()
	if !history[2].Undone || history[0].Undone {
		t.Fatalf("undone flags: %+v", history)
	}
	if _, _, err := s.Undo(e.ID(), ""); err == nil {
		t.Fatal("undoing twice must fail")
	}
	if p, _ := s.Pending(); len(p) != 0 {
		t.Fatalf("undo left pending changes: %v", ids(p))
	}

	// A conflicting later edit makes undo refuse instead of overwriting it.
	write(t, s.ConfigDir, "git/config", "[user]\nname = other\n")
	e2, _ := s.Record(nil, "Rename", "")
	write(t, s.ConfigDir, "git/config", "[user]\nname = third\n")
	s.Record(nil, "Rename again", "")
	if _, _, err := s.Undo(e2.Commit, ""); err == nil {
		t.Fatal("conflicting undo must fail")
	}
	if b, _ := os.ReadFile(filepath.Join(s.ConfigDir, "git/config")); string(b) != "[user]\nname = third\n" {
		t.Fatalf("failed undo touched the file: %q", b)
	}
}

func TestOmarchyHooks(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	write(t, cfg, "omarchy/hooks/theme-set.d/other.hook", "echo hi\n")
	write(t, cfg, "btop/btop.conf", "theme = a\n")
	write(t, cfg, "hypr/input.conf", "kb = us\n")
	s := &Store{ConfigDir: cfg, StateDir: filepath.Join(dir, "state"),
		ListPackages: func() ([]string, error) { return nil, nil },
		ListUnits:    func() ([]string, error) { return nil, nil }}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	out, _ := s.git(nil, "ls-files", "omarchy/hooks")
	if !strings.Contains(string(out), "theme-set.d/syschange.hook") || !strings.Contains(string(out), "post-update.d/syschange.hook") {
		t.Fatalf("hooks not installed at init: %s", out)
	}
	if p, _ := s.Pending(); len(p) != 0 {
		t.Fatalf("pending after init: %v", ids(p))
	}

	// The user edited input an hour ago; a theme switch just rewrote btop.
	write(t, cfg, "hypr/input.conf", "kb = us,ru\n")
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(cfg, "hypr/input.conf"), past, past)
	write(t, cfg, "btop/btop.conf", "theme = b\n")
	if err := s.OmarchyHook("theme-set", []string{"tokyo-night"}); err != nil {
		t.Fatal(err)
	}
	history, _ := s.History()
	if len(history) != 1 || history[0].Note != "Theme: tokyo-night" || history[0].By != "omarchy" || ids(history[0].Changes)[0] != "modified btop/btop.conf" {
		t.Fatalf("history %+v", history)
	}
	if p, _ := s.Pending(); !slices.Equal(ids(p), []string{"modified hypr/input.conf"}) {
		t.Fatalf("the user's change must stay pending: %v", ids(p))
	}

	// Running init again on an existing setup restores a deleted hook and records it.
	os.Remove(s.omarchyHookPath("font-set"))
	s.Record([]string{"omarchy"}, "", "")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if history, _ = s.History(); history[0].By != "syschange" {
		t.Fatalf("hook reinstall not recorded: %+v", history[0])
	}
}

func TestUndoSymlink(t *testing.T) {
	s, _ := newTestStore(t)
	link := filepath.Join(s.ConfigDir, "omarchy/plugins/mine")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink(t.TempDir(), link)
	e, err := s.Record(nil, "Plugin", "")
	if err != nil || len(e.Changes) != 1 {
		t.Fatalf("%+v %v", e, err)
	}
	if _, _, err := s.Undo(e.Commit, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err == nil {
		t.Fatal("added symlink not removed")
	}
	// Undo the undo: the link comes back as a link.
	history, _ := s.History()
	if _, _, err := s.Undo(history[0].ID(), ""); err != nil {
		t.Fatal(err)
	}
	if !isLink(link) {
		t.Fatal("symlink not restored")
	}
}

func TestSuggest(t *testing.T) {
	s, _ := newTestStore(t)
	bin := t.TempDir()
	// A fake agent: prints chatter, then the answer, and records the prompt it got.
	script := "#!/bin/sh\nfor a; do last=$a; done\nprintf '%s' \"$last\" > " + filepath.Join(bin, "prompt") + "\necho 'Thinking...'\necho '\"Голосовой ввод по Super+R.\"'\n"
	os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("LANG", "ru_RU.UTF-8")

	write(t, s.ConfigDir, "hypr/bindings.conf", "bind = SUPER, R, exec, voxtype\n")
	line, err := s.Suggest([]string{"hypr"}, "claude")
	if err != nil || line != "Голосовой ввод по Super+R" {
		t.Fatalf("suggest = %q, %v", line, err)
	}
	prompt, _ := os.ReadFile(filepath.Join(bin, "prompt"))
	for _, want := range []string{"ru_RU.UTF-8", "Hyprland", "+bind = SUPER, R, exec, voxtype"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if _, err := s.Suggest(nil, "pi"); err == nil {
		t.Fatal("unsupported agent must fail")
	}
}
