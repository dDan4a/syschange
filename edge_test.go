package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUndoRefusesWhenTheFileMovedOn(t *testing.T) {
	s, _ := newTestStore(t)
	cases := []struct {
		name   string
		change func() // what the entry records
		later  func() // what happens after it
		want   string
	}{
		{"edited after created",
			func() { write(t, s.ConfigDir, "new/a.conf", "1\n") },
			func() { write(t, s.ConfigDir, "new/a.conf", "2\n") }, "edited after it was created"},
		{"exists again",
			func() { os.Remove(filepath.Join(s.ConfigDir, "git/config")) },
			func() { write(t, s.ConfigDir, "git/config", "back\n") }, "exists again"},
		{"deleted since",
			func() { write(t, s.ConfigDir, "hypr/bindings.conf", "changed\n") },
			func() { os.Remove(filepath.Join(s.ConfigDir, "hypr/bindings.conf")) }, "was deleted"},
	}
	for _, tc := range cases {
		tc.change()
		e, err := s.Record(nil, tc.name, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		tc.later()
		s.Record(nil, "later", "")
		if _, _, err := s.Undo(e.ID(), ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}

	link := filepath.Join(s.ConfigDir, "links/l")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink("/one", link)
	e, _ := s.Record(nil, "link", "")
	os.Remove(link)
	os.Symlink("/two", link)
	s.Record(nil, "relinked", "")
	if _, _, err := s.Undo(e.ID(), ""); err == nil || !strings.Contains(err.Error(), "edited after it was created") {
		t.Errorf("relinked: %v", err)
	}
}

func TestUndoPackagesGivesCommands(t *testing.T) {
	s, pkgs := newTestStore(t)
	*pkgs = append(*pkgs, "vim")
	e, _ := s.Record(nil, "vim", "")
	_, hint, err := s.Undo(e.ID(), "")
	if err != nil || !strings.Contains(hint, "sudo pacman -Rns vim") {
		t.Fatalf("hint %q err %v", hint, err)
	}
	h, _ := s.History()
	if len(h) != 1 {
		t.Fatal("a package-only undo records nothing until the user runs the command")
	}
}

func TestIgnoreTrackedFile(t *testing.T) {
	s, _ := newTestStore(t)
	write(t, s.ConfigDir, "git/config", "[user]\nname = changed\n")
	if err := s.Ignore("git/config"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Pending(); len(p) != 0 {
		t.Fatalf("pending %v", ids(p))
	}
	out, _ := s.git(nil, "ls-files")
	if strings.Contains(string(out), "git/config") {
		t.Fatal("ignored file still tracked")
	}
	if err := s.Ignore("  "); err == nil {
		t.Fatal("empty pattern accepted")
	}
}

func TestDiffOfEveryKind(t *testing.T) {
	s, pkgs := newTestStore(t)
	*pkgs = append(*pkgs, "vim")
	os.Remove(filepath.Join(s.ConfigDir, "git/config"))
	var out bytes.Buffer
	if err := s.Diff(&out, nil, false); err != nil {
		t.Fatal(err)
	}
	must(t, out.String(), "-name = me")
	must(t, out.String(), "+ package vim")
	if lineStat(Change{Kind: "config"}) != "" || opSign("modified") != "~" {
		t.Fatal("lineStat/opSign")
	}
}

func TestApplyAURServicesAndFailures(t *testing.T) {
	a, aPkgs := newTestStore(t)
	units := []string{"system sshd.service"}
	a.ListUnits = func() ([]string, error) { return slices.Clone(units), nil }
	a.ListForeign = func() ([]string, error) { return []string{"paru-bin"}, nil }
	link := filepath.Join(a.ConfigDir, "omarchy/plugins/p")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink("/opt/plugin", link)
	*aPkgs = append(*aPkgs, "paru-bin")
	*aPkgs = slices.DeleteFunc(*aPkgs, func(p string) bool { return p == "git" })
	units = append(units, "system bluetooth.service")
	a.Record(nil, "setup", "")
	remote := filepath.Join(t.TempDir(), "r.git")
	exec.Command("git", "init", "--bare", "--quiet", remote).Run()
	if err := a.SetRemote(remote); err != nil {
		t.Fatal(err)
	}

	b, _ := newTestStore(t)
	plan, err := b.PlanApply(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	if !slices.Equal(plan.AUR, []string{"paru-bin"}) || !slices.Equal(plan.Removals, []string{"git"}) || !slices.Equal(plan.Units, []string{"system bluetooth.service"}) {
		t.Fatalf("plan %+v", plan)
	}
	var printed bytes.Buffer
	plan.Print(&printed)
	must(t, printed.String(), "paru-bin (AUR)")
	must(t, printed.String(), "left alone here: git")
	must(t, printed.String(), "enable    system bluetooth.service")

	var ran []string
	err = b.Apply(plan, func(args ...string) error {
		ran = append(ran, strings.Join(args, " "))
		if args[0] == "yay" {
			return errors.New("aur down")
		}
		return nil
	}, "")
	if err == nil || !strings.Contains(err.Error(), "installing AUR packages") {
		t.Fatalf("err %v ran %v", err, ran)
	}
	if !isLink(filepath.Join(b.ConfigDir, "omarchy/plugins/p")) {
		t.Fatal("symlink not applied as a link")
	}

	// Services: system ones through sudo, and a failure is reported.
	plan.AUR = nil
	ran = nil
	err = b.Apply(plan, func(args ...string) error {
		ran = append(ran, strings.Join(args, " "))
		if args[1] == "systemctl" {
			return errors.New("no")
		}
		return nil
	}, "")
	if err == nil || !strings.Contains(err.Error(), "enabling bluetooth.service") || !slices.Contains(ran, "sudo systemctl enable --now -- bluetooth.service") {
		t.Fatalf("err %v ran %v", err, ran)
	}
	plan.Units = nil
	plan.Repo = []string{"htop"}
	if err := b.Apply(plan, func(...string) error { return errors.New("pacman down") }, ""); err == nil {
		t.Fatal("repo install failure must be reported")
	}
}

func TestApplyInitializesAFreshMachine(t *testing.T) {
	a, _ := newTestStore(t)
	write(t, a.ConfigDir, "kitty/kitty.conf", "x\n")
	a.Record(nil, "kitty", "")
	remote := filepath.Join(t.TempDir(), "r.git")
	exec.Command("git", "init", "--bare", "--quiet", remote).Run()
	a.SetRemote(remote)

	dir := t.TempDir()
	b := &Store{ConfigDir: filepath.Join(dir, "cfg"), StateDir: filepath.Join(dir, "state"),
		ListPackages: func() ([]string, error) { return nil, nil }, ListUnits: func() ([]string, error) { return nil, nil }}
	os.MkdirAll(b.ConfigDir, 0o755)
	plan, err := b.PlanApply(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	if err := b.Apply(plan, func(...string) error { return nil }, ""); err != nil {
		t.Fatal(err)
	}
	if !b.Initialized() {
		t.Fatal("apply did not initialize")
	}
}

func TestPruneWindows(t *testing.T) {
	s, _ := newTestStore(t)
	old := window{Agent: "claude", Session: "old", Start: time.Now().Add(-60 * 24 * time.Hour), End: time.Now().Add(-60 * 24 * time.Hour)}
	b, _ := json.Marshal(old)
	line := string(b) + "\n"
	os.MkdirAll(s.StateDir, 0o700)
	os.WriteFile(s.path("agent-windows.jsonl"), []byte(strings.Repeat(line, 600<<10/len(line)+1)), 0o600)
	s.pruneWindows()
	if ws := s.windows(); len(ws) != 0 {
		t.Fatalf("%d old windows kept", len(ws))
	}
}

func TestChangeTimeAndAttributionOfPackages(t *testing.T) {
	s, pkgs := newTestStore(t)
	db := t.TempDir()
	old := pacmanDB
	pacmanDB = db
	t.Cleanup(func() { pacmanDB = old })
	write(t, db, "vim-9-1/desc", "%NAME%\nvim\n%INSTALLDATE%\n"+itoa(time.Now().Unix())+"\n")
	*pkgs = append(*pkgs, "vim")
	in := `{"session_id":"s","tool_use_id":"1"}`
	s.ToolHook("claude", true, strings.NewReader(in))
	time.Sleep(10 * time.Millisecond)
	s.ToolHook("claude", false, strings.NewReader(in))
	p, _ := s.Pending()
	s.Attribute(p)
	if p[0].By != "claude" {
		t.Fatalf("package not attributed: %+v", p[0])
	}
	if _, ok := changeTime(s, itemChange("package", "removed", "vim")); ok {
		t.Fatal("a removal has no time")
	}
	if _, ok := changeTime(s, Change{Kind: "config", Op: "modified", Name: "missing"}); ok {
		t.Fatal("a missing file has no time")
	}
	// A post without a pre records nothing.
	s.ToolHook("claude", false, strings.NewReader(`{"session_id":"s","tool_use_id":"x"}`))
}

func TestOmarchyNotesAndAncestors(t *testing.T) {
	if n := omarchyNote("theme-set", []string{"tokyo-night"}); n != "Theme: tokyo-night" {
		t.Fatal(n)
	}
	if n := omarchyNote("font-set", []string{"JetBrains Mono"}); n != "Font: JetBrains Mono" {
		t.Fatal(n)
	}
	if n := omarchyNote("post-update", nil); !strings.HasPrefix(n, "Omarchy update") {
		t.Fatal(n)
	}
	// go test runs this binary as a child of "go": that is a real ancestor to find.
	if at, ok := ancestorStart("go"); ok && at.After(time.Now()) {
		t.Fatalf("ancestor started in the future: %v", at)
	}
	if _, ok := ancestorStart("no-such-process-name"); ok {
		t.Fatal("found a process that does not exist")
	}
	s, _ := newTestStore(t)
	if err := s.OmarchyHook("unknown-event", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSmallHelpers(t *testing.T) {
	home, _ := os.UserHomeDir()
	if expandHome("~") != home || expandHome("~/x") != home+"/x" || expandHome("/abs") != "/abs" {
		t.Fatal("expandHome")
	}
	if appName("unknown-app") != "unknown-app" || appName("hypr") != "Hyprland" {
		t.Fatal("appName")
	}
	t.Setenv("XDG_TEST_UNSET", "")
	if envOr("XDG_TEST_UNSET", "d") != "d" {
		t.Fatal("envOr")
	}
	if lastLine("\n  \n") != "" || lastLine("a\n«b.»") != "b" {
		t.Fatal("lastLine")
	}
	if _, err := jqEditBroken(t); err == nil {
		t.Fatal("jqEdit on broken JSON must fail")
	}
	s, err := DefaultStore()
	if err != nil || s.ListPackages == nil || s.ListUnits == nil || s.SpawnPush == nil {
		t.Fatalf("DefaultStore %+v %v", s, err)
	}
	if cp, err := func() (Checkpoint, error) { st, _ := newTestStore(t); return st.Checkpoint("") }(); err != nil || cp.Name == "" {
		t.Fatalf("default checkpoint name: %+v %v", cp, err)
	}
}

func jqEditBroken(t *testing.T) (string, error) {
	f := filepath.Join(t.TempDir(), "broken.json")
	os.WriteFile(f, []byte("{not json"), 0o644)
	return "", jqEdit(f, ".")
}

// The real listers need pacman and systemd; they run only where those exist.
func TestRealListers(t *testing.T) {
	if _, err := exec.LookPath("pacman"); err == nil {
		if pkgs, err := pacmanExplicit(); err != nil || len(pkgs) == 0 {
			t.Errorf("pacmanExplicit: %v %v", len(pkgs), err)
		}
		foreignPackages()
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		if _, err := enabledUnits(); err != nil {
			t.Logf("enabledUnits (no user session here?): %v", err)
		}
	}
}

func TestConcurrentRecordsKeepTheirOwnFiles(t *testing.T) {
	s, _ := newTestStore(t)
	const n = 6
	for i := range n {
		write(t, s.ConfigDir, fmt.Sprintf("app%d/conf", i), "x\n")
	}
	stop := make(chan struct{})
	go func() { // the bar polls status all the time
		for {
			select {
			case <-stop:
				return
			default:
				s.Pending()
			}
		}
	}()
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Record([]string{fmt.Sprintf("app%d/conf", i)}, fmt.Sprintf("note %d", i), "")
		}()
	}
	wg.Wait()
	close(stop)
	for i, err := range errs {
		if err != nil {
			t.Errorf("record %d: %v", i, err)
		}
	}
	history, err := s.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range history {
		out, err := s.git(nil, "show", "--name-only", "--format=", e.Commit)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Fields(string(out)); len(got) != 1 || len(e.Changes) != 1 || got[0] != e.Changes[0].Name {
			t.Errorf("entry %q has changes %v, its commit has %v", e.Note, e.Changes, got)
		}
	}
}

func TestRestoreLeavesFilesItCannotSave(t *testing.T) {
	s, _ := newTestStore(t)
	write(t, s.ConfigDir, "app/keep.conf", "v1\n")
	write(t, s.ConfigDir, "app/gone.conf", "v1\n")
	s.Record(nil, "add", "")
	s.Checkpoint("point")

	// Ignored since, and edited: no checkpoint has the edit.
	if err := s.Ignore("app/keep.conf"); err != nil {
		t.Fatal(err)
	}
	write(t, s.ConfigDir, "app/keep.conf", "my edit\n")
	// Removed from the history, then created again holding a secret.
	os.Remove(filepath.Join(s.ConfigDir, "app/gone.conf"))
	s.Record(nil, "remove", "")
	secret := "token = ghp_abcdefghijklmnopqrstuvwxyz0123\n"
	write(t, s.ConfigDir, "app/gone.conf", secret)

	if _, err := s.Restore("point", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(s.ConfigDir, "app/keep.conf")); got != "my edit\n" {
		t.Errorf("ignored file = %q", got)
	}
	if got := readFile(t, filepath.Join(s.ConfigDir, "app/gone.conf")); got != secret {
		t.Errorf("file with a secret = %q", got)
	}
}

func TestApplyWillNotDeleteAFileWithASecret(t *testing.T) {
	dir := t.TempDir()
	a := &Store{ConfigDir: filepath.Join(dir, "a"), StateDir: filepath.Join(dir, "as"),
		ListPackages: func() ([]string, error) { return nil, nil }, ListUnits: func() ([]string, error) { return nil, nil }}
	write(t, a.ConfigDir, "app/conf.ini", "plain\n")
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(a.ConfigDir, "app/conf.ini"))
	write(t, a.ConfigDir, "other/x", "y\n")
	a.Record(nil, "remove", "")
	remote := filepath.Join(t.TempDir(), "r.git")
	exec.Command("git", "init", "--bare", "--quiet", remote).Run()
	a.SetRemote(remote)

	b, _ := newTestStore(t)
	write(t, b.ConfigDir, "app/conf.ini", "token = ghp_abcdefghijklmnopqrstuvwxyz0123\n")
	if _, err := b.PlanApply(remote); err == nil || !strings.Contains(err.Error(), "cannot keep") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyHomeFileOnAFreshMachine(t *testing.T) {
	a, _ := newHomeStore(t)
	a.Track([]string{"~/.gitconfig"})
	a.Record(nil, "setup", "")
	remote := filepath.Join(t.TempDir(), "r.git")
	exec.Command("git", "init", "--bare", "--quiet", remote).Run()
	a.SetRemote(remote)

	dir := t.TempDir()
	b := &Store{ConfigDir: filepath.Join(dir, "cfg"), StateDir: filepath.Join(dir, "state"), HomeDir: filepath.Join(dir, "home"),
		ListPackages: func() ([]string, error) { return nil, nil }, ListUnits: func() ([]string, error) { return nil, nil }}
	os.MkdirAll(b.ConfigDir, 0o755)
	os.MkdirAll(b.HomeDir, 0o755)
	plan, err := b.PlanApply(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	if err := b.Apply(plan, func(...string) error { return nil }, ""); err != nil {
		t.Fatal(err)
	}
	must(t, readFile(t, filepath.Join(b.HomeDir, ".gitconfig")), "name = me")
	// Still refused before init: a name that looks like a secret.
	c := &Store{ConfigDir: b.ConfigDir, StateDir: filepath.Join(dir, "state2"), HomeDir: b.HomeDir}
	if err := c.homeAllowed("my-token.txt"); err == nil {
		t.Error("an exclude pattern must still refuse before init")
	}
}

func TestTornJournalLineDoesNotBreakHistory(t *testing.T) {
	s, _ := newTestStore(t)
	write(t, s.ConfigDir, "app/a", "x\n")
	s.Record(nil, "kept", "")
	f, err := os.OpenFile(s.path("journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"time":"2026-`)
	f.Close()
	history, err := s.History()
	if err != nil || len(history) != 1 || history[0].Note != "kept" {
		t.Fatalf("history = %v, err = %v", history, err)
	}
}
