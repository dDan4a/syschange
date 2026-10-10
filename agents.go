package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed skills/syschange/SKILL.md
var skillMD []byte

// agent is a coding agent syschange can connect to: a skill so it knows how to
// record changes, and hooks so it does not forget to. All four speak the same
// hook protocol, with different file locations and event names.
type agent struct {
	Name      string
	Home      string   // the agent's config directory; missing means not installed
	SkillDirs []string // where it looks for skills
	HookFile  string   // JSON file with {"hooks": {"<Event>": [...]}}
	OwnFile   bool     // HookFile belongs to syschange alone (Grok reads every file in its hooks dir)
	StopEvent string   // the event fired when the agent is about to finish its turn
	PreEvent  string   // before and after every tool call
	PostEvent string
	TimeoutMS bool // hook timeout is in milliseconds, not seconds
}

func agents(home string) []agent {
	shared := filepath.Join(home, ".agents", "skills") // read by Codex, Gemini and Grok
	return []agent{
		{
			Name: "claude", Home: filepath.Join(home, ".claude"),
			SkillDirs: []string{filepath.Join(home, ".claude", "skills")},
			HookFile:  filepath.Join(home, ".claude", "settings.json"), StopEvent: "Stop", PreEvent: "PreToolUse", PostEvent: "PostToolUse",
		},
		{
			Name: "codex", Home: filepath.Join(home, ".codex"),
			SkillDirs: []string{shared, filepath.Join(home, ".codex", "skills")},
			HookFile:  filepath.Join(home, ".codex", "hooks.json"), StopEvent: "Stop", PreEvent: "PreToolUse", PostEvent: "PostToolUse",
		},
		{
			Name: "gemini", Home: filepath.Join(home, ".gemini"),
			SkillDirs: []string{shared},
			HookFile:  filepath.Join(home, ".gemini", "settings.json"), StopEvent: "AfterAgent", PreEvent: "BeforeTool", PostEvent: "AfterTool", TimeoutMS: true,
		},
		{
			Name: "grok", Home: filepath.Join(home, ".grok"),
			SkillDirs: []string{filepath.Join(home, ".grok", "skills")},
			HookFile:  filepath.Join(home, ".grok", "hooks", "syschange.json"), OwnFile: true, StopEvent: "Stop", PreEvent: "PreToolUse", PostEvent: "PostToolUse",
		},
	}
}

// events are the hooks syschange adds: session start, the tool-call window for
// attribution, and the stop gate.
func (a agent) events() []string {
	return []string{"SessionStart", a.PreEvent, a.PostEvent, a.StopEvent}
}

// hookArg is the syschange hook sub-argument for an agent event.
func (a agent) hookArg(event string) string {
	switch event {
	case "SessionStart":
		return " start"
	case a.PreEvent:
		return " pre"
	case a.PostEvent:
		return " post"
	}
	return ""
}

func (a agent) installed() bool {
	info, err := os.Stat(a.Home)
	return err == nil && info.IsDir()
}

// AgentState says, per installed agent, whether it is connected.
type AgentState struct {
	Name      string `json:"name"`
	Title     string `json:"title"` // "Claude Code"
	Connected bool   `json:"connected"`
	Default   bool   `json:"default"` // Omarchy's default agent
}

var agentTitles = map[string]string{"claude": "Claude Code", "codex": "Codex", "gemini": "Gemini CLI", "grok": "Grok"}

type Agents struct {
	Home    string // user's home
	Exe     string // absolute path of this binary, used in hook commands
	Default string // Omarchy's default agent, if any
}

func DefaultAgents() (*Agents, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Agents{Home: home, Exe: exe, Default: omarchyDefaultAgent()}, nil
}

func (g *Agents) skillPath() string {
	return filepath.Join(envOr("XDG_DATA_HOME", filepath.Join(g.Home, ".local", "share")), "syschange", "skills", "syschange")
}

// hookCommand does nothing when the binary is gone, so uninstalling syschange
// without disconnecting never breaks the agent's tool calls.
func (g *Agents) hookCommand(a agent, event string) string {
	exe := shellQuote(g.Exe)
	return "[ -x " + exe + " ] && " + exe + " hook " + a.Name + a.hookArg(event) + " || true"
}

func (g *Agents) Status() ([]AgentState, error) {
	var out []AgentState
	for _, a := range agents(g.Home) {
		if !a.installed() {
			continue
		}
		hooked := true
		for _, ev := range a.events() {
			ok, err := hasHook(a.HookFile, ev)
			if err != nil {
				return nil, err
			}
			hooked = hooked && ok
		}
		out = append(out, AgentState{Name: a.Name, Title: agentTitles[a.Name], Connected: hooked && g.hasSkill(a), Default: a.Name == g.Default})
	}
	return out, nil
}

func (g *Agents) hasSkill(a agent) bool {
	for _, dir := range a.SkillDirs {
		if target, err := os.Readlink(filepath.Join(dir, "syschange")); err == nil && target == g.skillPath() {
			return true
		}
	}
	return false
}

// Install connects the named agents, or every installed one when no name is
// given. It is safe to run again.
func (g *Agents) Install(names ...string) ([]string, error) {
	if err := os.MkdirAll(g.skillPath(), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(g.skillPath(), "SKILL.md"), skillMD, 0o644); err != nil {
		return nil, err
	}
	var done []string
	for _, a := range agents(g.Home) {
		if !a.installed() || (len(names) > 0 && !slices.Contains(names, a.Name)) {
			continue
		}
		for _, dir := range a.SkillDirs {
			if err := g.linkSkill(dir); err != nil {
				return done, err
			}
		}
		if err := g.addHooks(a); err != nil {
			return done, err
		}
		done = append(done, a.Name)
	}
	if len(done) == 0 {
		if len(names) > 0 {
			return nil, fmt.Errorf("%s: not installed or not supported (claude, codex, gemini, grok)", strings.Join(names, ", "))
		}
		return nil, errors.New("no supported agent found (Claude Code, Codex, Gemini CLI or Grok)")
	}
	return done, nil
}

func (g *Agents) linkSkill(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	link := filepath.Join(dir, "syschange")
	if target, err := os.Readlink(link); err == nil {
		if target == g.skillPath() {
			return nil
		}
	}
	if _, err := os.Lstat(link); err == nil {
		return fmt.Errorf("%s already exists and is not ours, leaving it alone", link)
	}
	return os.Symlink(g.skillPath(), link)
}

// Remove disconnects every agent: our skill links and Stop hooks go away.
func (g *Agents) Remove() error {
	for _, a := range agents(g.Home) {
		for _, dir := range a.SkillDirs {
			link := filepath.Join(dir, "syschange")
			if target, err := os.Readlink(link); err == nil && target == g.skillPath() {
				if err := os.Remove(link); err != nil {
					return err
				}
			}
		}
		if _, err := os.Stat(a.HookFile); err != nil {
			continue
		}
		if a.OwnFile {
			if err := os.Remove(a.HookFile); err != nil {
				return err
			}
			continue
		}
		for _, ev := range a.events() {
			if err := jqEdit(a.HookFile, `if .hooks[$ev] then .hooks[$ev] |= (map(.hooks |= map(select(.command | test($m) | not))) | map(select(.hooks | length > 0))) | if .hooks[$ev] == [] then del(.hooks[$ev]) else . end else . end`,
				"--arg", "m", hookMarker, "--arg", "ev", ev); err != nil {
				return err
			}
		}
	}
	return nil
}

// hookMarker finds our hook in a file whatever path the binary had.
const hookMarker = `syschange'? hook (claude|codex|gemini|grok)( start| pre| post)?( \|\| true)?$`

func hasHook(file, event string) (bool, error) {
	if _, err := os.Stat(file); err != nil {
		return false, nil
	}
	out, err := exec.Command("jq", "--arg", "m", hookMarker, "--arg", "ev", event,
		`[.hooks[$ev][]?.hooks[]?.command | select(test($m))] | length > 0`, file).Output()
	if err != nil {
		return false, fmt.Errorf("jq %s: %w", file, err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func (g *Agents) addHooks(a agent) error {
	if err := os.MkdirAll(filepath.Dir(a.HookFile), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(a.HookFile); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(a.HookFile, []byte("{}\n"), 0o644); err != nil {
			return err
		}
	}
	for _, ev := range a.events() {
		timeout := 30
		if ev == a.PreEvent || ev == a.PostEvent {
			timeout = 5 // runs on every tool call: must never hold the agent up
		}
		if a.TimeoutMS {
			timeout *= 1000
		}
		if ok, err := hasHook(a.HookFile, ev); err != nil || ok {
			if err != nil {
				return err
			}
			continue
		}
		if err := jqEdit(a.HookFile, `.hooks[$ev] += [{"hooks": [{"type": "command", "command": $cmd, "timeout": $t}]}]`,
			"--arg", "ev", ev, "--arg", "cmd", g.hookCommand(a, ev), "--argjson", "t", fmt.Sprint(timeout)); err != nil {
			return err
		}
	}
	return nil
}

// jqEdit rewrites a JSON file with jq, which keeps the user's key order.
// A copy of the original is left next to it.
func jqEdit(file, filter string, args ...string) error {
	orig, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	cmd := exec.Command("jq", append(append(args, filter), file)...)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("jq %s: %w", file, err)
	}
	if string(out) == string(orig) {
		return nil
	}
	if err := os.WriteFile(file+".syschange.bak", orig, 0o600); err != nil {
		return err
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	tmp := file + ".syschange.tmp"
	if err := os.WriteFile(tmp, out, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
