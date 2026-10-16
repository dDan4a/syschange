package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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
	for _, want := range []string{"sudo pacman -S --needed voxtype", "- added `voxtype/config.toml`",
		"Voice typing · _claude_\n", "  - Voxtype: config.toml (`voxtype/config.toml`), added\n", "  - Installed voxtype\n"} {
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
	legacy := filepath.Join(home, ".agents/skills/syschange") // where v0.1.0 linked the skill
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	os.Symlink(g.skillPath(), legacy)

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
	if !strings.HasPrefix(string(settings), "{\n  \"model\"") || strings.Count(string(settings), "hook claude || true\"") != 1 ||
		strings.Count(string(settings), "hook claude start") != 1 ||
		!strings.Contains(string(settings), `"[ -x '/opt/my bin/syschange' ] && '/opt/my bin/syschange' hook claude || true"`) || !strings.Contains(string(settings), `"other"`) {
		t.Fatalf("settings.json:\n%s", settings)
	}
	if _, err := os.Lstat(legacy); err == nil {
		t.Fatal("the shared ~/.agents/skills link must go")
	}
	for _, p := range []string{".codex/skills/syschange/SKILL.md", ".gemini/skills/syschange/SKILL.md", ".grok/skills/syschange/SKILL.md"} {
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
	script := "#!/bin/sh\ncat > " + filepath.Join(bin, "prompt") + "\necho 'Thinking...'\necho '- \"Голосовой ввод по Super+R.\"'\n"
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
	// Refusals, questions and the UNKNOWN marker are errors, never labels.
	for _, reply := range []string{
		"UNKNOWN",
		"I don't have enough context to determine what Happ.conf does",
		"I don't have enough context.\nCould you clarify what bld is",
		"Что делает этот файл?",
		strings.Repeat("слово ", 30),
	} {
		os.WriteFile(filepath.Join(bin, "reply"), []byte(reply+"\n"), 0o644)
		os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat >/dev/null\ncat "+filepath.Join(bin, "reply")+"\n"), 0o755)
		if line, err := s.Suggest([]string{"hypr"}, "claude"); err == nil || !strings.Contains(err.Error(), "describe it yourself") {
			t.Errorf("reply %q: suggest = %q, %v", reply, line, err)
		}
	}
}

func TestAttributionFromToolCalls(t *testing.T) {
	s, _ := newTestStore(t)
	tool := func(pre bool, id string) {
		in := `{"session_id":"s-1","tool_use_id":"` + id + `"}`
		if err := s.ToolHook("claude", pre, strings.NewReader(in)); err != nil {
			t.Fatal(err)
		}
	}
	// The user edits a file before the agent's tool call...
	write(t, s.ConfigDir, "git/config", "[user]\nname = me2\n")
	past := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(s.ConfigDir, "git/config"), past, past)
	// ...and the agent edits another one during its call.
	tool(true, "t1")
	write(t, s.ConfigDir, "hypr/bindings.conf", "agent\n")
	tool(false, "t1")

	pending, _ := s.Pending()
	s.Attribute(pending)
	by := map[string]string{}
	for _, c := range pending {
		by[c.ID] = c.By
	}
	if by["hypr/bindings.conf"] != "claude" || by["git/config"] != "" {
		t.Fatalf("attribution %v", by)
	}

	// The stop hook asks only about this session's change, not the user's.
	var out bytes.Buffer
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(transcript, []byte(`{"timestamp":"`+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)+`"}`+"\n"), 0o600)
	s.StopHook("claude", strings.NewReader(`{"session_id":"s-1","transcript_path":"`+transcript+`"}`), &out)
	if !strings.Contains(out.String(), "hypr/bindings.conf") || strings.Contains(out.String(), "git/config") {
		t.Fatalf("stop hook: %s", out.String())
	}

	// The history keeps who made each change.
	e, err := s.Record([]string{"hypr"}, "", "")
	if err != nil || e.Changes[0].By != "claude" {
		t.Fatalf("recorded %+v %v", e.Changes, err)
	}
}

func TestCheckpointRestore(t *testing.T) {
	s, pkgs := newTestStore(t)
	// The checkpoint includes an unrecorded edit and a new file.
	write(t, s.ConfigDir, "hypr/bindings.conf", "at checkpoint\n")
	write(t, s.ConfigDir, "kitty/kitty.conf", "font_size 12\n")
	if _, err := s.Checkpoint("before experiment"); err != nil {
		t.Fatal(err)
	}
	// The experiment: edit, delete, add, record some of it, install a package.
	write(t, s.ConfigDir, "hypr/bindings.conf", "experiment\n")
	os.Remove(filepath.Join(s.ConfigDir, "kitty/kitty.conf"))
	os.Remove(filepath.Join(s.ConfigDir, "git/config"))
	write(t, s.ConfigDir, "waybar/config.jsonc", "{}\n")
	s.Record([]string{"hypr", "git"}, "Experiment", "")
	*pkgs = append(*pkgs, "waybar")

	hint, err := s.Restore("before experiment", "")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"hypr/bindings.conf": "at checkpoint\n", "kitty/kitty.conf": "font_size 12\n", "git/config": "[user]\nname = me\n"} {
		if b, err := os.ReadFile(filepath.Join(s.ConfigDir, path)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", path, b, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "waybar/config.jsonc")); err == nil {
		t.Error("file added after the checkpoint was not removed")
	}
	if !strings.Contains(hint, "sudo pacman -Rns waybar") {
		t.Errorf("hint %q", hint)
	}
	if p, _ := s.Pending(); len(p) != 1 || p[0].ID != "pkg:waybar" {
		t.Errorf("pending after restore: %v", ids(p))
	}
	history, _ := s.History()
	if history[0].Note != "Restore checkpoint: before experiment" {
		t.Errorf("restore not recorded: %+v", history[0])
	}

	// The state before the restore is a checkpoint too: restoring it undoes the restore.
	all, _ := s.Checkpoints()
	if len(all) != 2 || all[0].Name != "Before restoring before experiment" {
		t.Fatalf("checkpoints %+v", all)
	}
	if _, err := s.Restore(all[0].Name, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.ConfigDir, "waybar/config.jsonc")); string(b) != "{}\n" {
		t.Errorf("restore of the restore: waybar = %q", b)
	}
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "kitty/kitty.conf")); err == nil {
		t.Error("kitty should be gone again")
	}
}

func TestAgentsInstallOnlyNamed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	g := &Agents{Home: home, Exe: "/usr/bin/syschange", Default: "claude"}
	if done, err := g.Install("claude"); err != nil || !slices.Equal(done, []string{"claude"}) {
		t.Fatalf("install claude: %v %v", done, err)
	}
	states, _ := g.Status()
	if !states[0].Connected || !states[0].Default || states[0].Title != "Claude Code" || states[1].Connected {
		t.Fatalf("states %+v", states)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex/hooks.json")); err == nil {
		t.Fatal("codex must stay untouched")
	}
	if _, err := g.Install("cursor"); err == nil {
		t.Fatal("unknown agent must fail")
	}
}

func TestAutoExport(t *testing.T) {
	s, _ := newTestStore(t)
	out := filepath.Join(t.TempDir(), "notes", "System changes.md")
	if err := s.SetAutoExport(filepath.Join(s.ConfigDir, "x.md")); err == nil {
		t.Fatal("a file inside the tracked directory must be refused")
	}
	if err := s.SetAutoExport(out); err != nil {
		t.Fatal(err)
	}
	write(t, s.ConfigDir, "hypr/bindings.conf", "x\n")
	if _, err := s.Record(nil, "Keep it in sync", ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "Keep it in sync") {
		t.Fatalf("auto export not updated:\n%s", b)
	}
	s.SetAutoExport("")
	write(t, s.ConfigDir, "hypr/bindings.conf", "y\n")
	s.Record(nil, "After off", "")
	if b, _ := os.ReadFile(out); strings.Contains(string(b), "After off") {
		t.Fatal("auto export still on")
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"--git-dir=" + dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestBackupRemote(t *testing.T) {
	s, _ := newTestStore(t)
	s.ListForeign = func() ([]string, error) { return []string{"yay-bin"}, nil }
	remote := filepath.Join(t.TempDir(), "backup.git")
	exec.Command("git", "init", "--bare", "--quiet", remote).Run()

	write(t, s.ConfigDir, "hypr/bindings.conf", "pushed\n")
	s.Record(nil, "Before the backup", "")
	if err := s.SetRemote(remote); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, remote, "show", "config:hypr/bindings.conf"); got != "pushed" {
		t.Fatalf("config branch: %q", got)
	}
	if got := gitIn(t, remote, "show", "state:journal.jsonl"); !strings.Contains(got, "Before the backup") {
		t.Fatalf("state branch: %q", got)
	}
	if got := gitIn(t, remote, "show", "state:packages.aur.txt"); got != "yay-bin" {
		t.Fatalf("aur list: %q", got)
	}
	if st := s.LastPush(); st == nil || st.Error != "" || st.Remote != remote {
		t.Fatalf("push status %+v", st)
	}

	// Nothing new: no new state commit. A checkpoint: its ref goes along.
	before := gitIn(t, remote, "rev-parse", "state")
	s.Push()
	if gitIn(t, remote, "rev-parse", "state") != before {
		t.Fatal("an unchanged state made a new commit")
	}
	s.Checkpoint("cp")
	s.Push()
	if refs := gitIn(t, remote, "for-each-ref", "refs/checkpoints"); refs == "" {
		t.Fatal("checkpoint ref not pushed")
	}

	// A broken remote is reported, not hidden.
	s.SetRemote("")
	s.git(nil, "remote", "add", backupRemote, filepath.Join(t.TempDir(), "missing.git"))
	if err := s.Push(); err == nil || s.LastPush().Error == "" {
		t.Fatal("push to a missing remote must fail and be noted")
	}
}

func TestApplyFromBackup(t *testing.T) {
	// Machine A: changes, then a backup.
	a, aPkgs := newTestStore(t)
	aUnits := []string{"system sshd.service"}
	a.ListUnits = func() ([]string, error) { return slices.Clone(aUnits), nil }
	a.ListForeign = func() ([]string, error) { return []string{"yay-bin"}, nil }
	write(t, a.ConfigDir, "hypr/bindings.conf", "bind = SUPER, R, exec, voxtype\n")
	write(t, a.ConfigDir, "voxtype/config.toml", "model = \"base\"\n")
	os.Remove(filepath.Join(a.ConfigDir, "git/config"))
	*aPkgs = append(*aPkgs, "voxtype", "yay-bin")
	aUnits = append(aUnits, "user voxtype.service")
	if _, err := a.Record(nil, "Voice typing", ""); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "backup.git")
	exec.Command("git", "init", "--bare", "--quiet", remote).Run()
	if err := a.SetRemote(remote); err != nil {
		t.Fatal(err)
	}

	// Machine B: fresh, already has yay-bin.
	b, bPkgs := newTestStore(t)
	*bPkgs = append(*bPkgs, "yay-bin")
	b.Record([]string{"packages"}, "", "")
	plan, err := b.PlanApply(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	var files []string
	for _, f := range plan.Files {
		files = append(files, f.Action+" "+f.Path)
	}
	slices.Sort(files)
	if !slices.Equal(files, []string{"add voxtype/config.toml", "delete git/config", "overwrite hypr/bindings.conf"}) ||
		!slices.Equal(plan.Repo, []string{"voxtype"}) || len(plan.AUR) != 0 || !slices.Equal(plan.Units, []string{"user voxtype.service"}) {
		t.Fatalf("plan files=%v repo=%v aur=%v units=%v", files, plan.Repo, plan.AUR, plan.Units)
	}

	var ran []string
	run := func(args ...string) error {
		ran = append(ran, strings.Join(args, " "))
		if args[1] == "pacman" {
			*bPkgs = append(*bPkgs, "voxtype")
		}
		return nil
	}
	if err := b.Apply(plan, run, ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"sudo pacman -S --needed -- voxtype", "systemctl --user enable --now -- voxtype.service"}) {
		t.Fatalf("ran %v", ran)
	}
	if got, _ := os.ReadFile(filepath.Join(b.ConfigDir, "hypr/bindings.conf")); string(got) != "bind = SUPER, R, exec, voxtype\n" {
		t.Fatalf("bindings %q", got)
	}
	if _, err := os.Stat(filepath.Join(b.ConfigDir, "git/config")); err == nil {
		t.Fatal("git/config should be deleted")
	}
	history, _ := b.History()
	if history[0].Note != "Applied setup from "+remote {
		t.Fatalf("history %+v", history[0])
	}
	if cps, _ := b.Checkpoints(); len(cps) != 1 || !strings.HasPrefix(cps[0].Name, "Before applying") {
		t.Fatalf("checkpoints %+v", cps)
	}
	// Files and the package are recorded in one entry.
	if p, _ := b.Pending(); len(p) != 0 {
		t.Fatalf("pending after apply: %v", ids(p))
	}

	// Applying again: nothing left to do.
	again, err := b.PlanApply(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Cleanup()
	if len(again.Files) != 0 || len(again.Repo) != 0 {
		t.Fatalf("second plan not empty: %+v", again)
	}
}

func TestOmarchyTouchedYourFiles(t *testing.T) {
	s, _ := newTestStore(t)
	write(t, s.ConfigDir, "hypr/bindings.conf", "bind = SUPER, Q, killactive\nbind = SUPER, R, exec, voxtype\n")
	if _, err := s.Record(nil, "Voice typing", ""); err != nil {
		t.Fatal(err)
	}
	// An update rewrites your bindings and adds a file of its own.
	write(t, s.ConfigDir, "hypr/bindings.conf", "bind = SUPER, Q, killactive\nbind = SUPER, W, close\n")
	write(t, s.ConfigDir, "waybar/config.jsonc", "{}\n")
	update, err := s.Record(nil, "Omarchy update to 3.9", "omarchy")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := s.History()
	if !slices.Equal(h[0].Touched, []string{"hypr/bindings.conf"}) {
		t.Fatalf("touched %v", h[0].Touched)
	}
	if _, _, err := s.Undo(update.Commit, "", "kitty/kitty.conf"); err == nil {
		t.Fatal("a file outside the entry must be refused")
	}
	if _, _, err := s.Undo(update.Commit, "", "hypr/bindings.conf"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.ConfigDir, "hypr/bindings.conf")); !strings.Contains(string(b), "voxtype") || strings.Contains(string(b), "SUPER, W") {
		t.Fatalf("your bindings are not back:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(s.ConfigDir, "waybar/config.jsonc")); string(b) != "{}\n" {
		t.Fatal("the update's other files must stay")
	}
	h, _ = s.History()
	if h[1].Note != "Omarchy update to 3.9" || len(h[1].Touched) != 0 || h[1].Undone {
		t.Fatalf("after keeping yours: %+v", h[1])
	}
	if !strings.Contains(h[0].Note, "(hypr/bindings.conf)") {
		t.Fatalf("undo note %q", h[0].Note)
	}
}

func TestNotifyRemindAndDigest(t *testing.T) {
	s, pkgs := newTestStore(t)
	now := time.Now()
	write(t, s.ConfigDir, "hypr/bindings.conf", "bind = SUPER, R, exec, voxtype\n")
	*pkgs = append(*pkgs, "voxtype")
	if _, err := s.Record(nil, "Voice typing", "claude"); err != nil {
		t.Fatal(err)
	}
	write(t, s.ConfigDir, "kitty/kitty.conf", "font_size 12\n")
	if _, err := s.Record(nil, "", ""); err != nil {
		t.Fatal(err)
	}

	// A change left unrecorded for five days.
	write(t, s.ConfigDir, "waybar/config.jsonc", "{}\n")
	old := now.AddDate(0, 0, -5)
	os.Chtimes(filepath.Join(s.ConfigDir, "waybar/config.jsonc"), old, old)

	if due, _ := s.Notify(now, 7, false); len(due) != 0 {
		t.Fatalf("five days is under the limit: %+v", due)
	}
	due, err := s.Notify(now, 3, false)
	if err != nil || len(due) != 1 || !strings.Contains(due[0].Summary, "not recorded for 5 days") || !strings.Contains(due[0].Body, "Waybar") {
		t.Fatalf("reminder %+v %v", due, err)
	}
	if due, _ := s.Notify(now.Add(time.Minute), 3, false); len(due) != 0 {
		t.Fatalf("one reminder a day: %+v", due)
	}

	// Next week: the digest of this one, once.
	due, err = s.Notify(now.AddDate(0, 0, 7), 0, true)
	if err != nil || len(due) != 1 || due[0].Summary != "Your setup last week" {
		t.Fatalf("digest %+v %v", due, err)
	}
	for _, want := range []string{"2 entries, 3 changes.", "Config: Hyprland (1), Kitty (1).", "Installed: voxtype.", "By: claude 1, you 1.", "1 without a description."} {
		if !strings.Contains(due[0].Body, want) {
			t.Fatalf("digest lacks %q:\n%s", want, due[0].Body)
		}
	}
	if due, _ := s.Notify(now.AddDate(0, 0, 7).Add(time.Hour), 0, true); len(due) != 0 {
		t.Fatalf("one digest a week: %+v", due)
	}
	if due, _ := s.Notify(now.AddDate(0, 0, 21), 0, true); len(due) != 0 {
		t.Fatalf("an empty week sends nothing: %+v", due)
	}
}
