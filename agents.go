package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed skill/SKILL.md
var skillMD []byte

// agent is a coding agent syschange can connect to: a skill so it knows how to
// record changes, and a Stop hook so it does not forget to.
type agent struct {
	Name      string
	Home      string   // the agent's config directory; missing means not installed
	SkillDirs []string // where it looks for skills
	HookFile  string   // JSON file with {"hooks": {"Stop": [...]}}
}

func agents(home string) []agent {
	return []agent{
		{
			Name: "claude", Home: filepath.Join(home, ".claude"),
			SkillDirs: []string{filepath.Join(home, ".claude", "skills")},
			HookFile:  filepath.Join(home, ".claude", "settings.json"),
		},
		{
			Name: "codex", Home: filepath.Join(home, ".codex"),
			// ~/.agents/skills is the shared location Omarchy and Codex read.
			SkillDirs: []string{filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".codex", "skills")},
			HookFile:  filepath.Join(home, ".codex", "hooks.json"),
		},
	}
}

func (a agent) installed() bool {
	info, err := os.Stat(a.Home)
	return err == nil && info.IsDir()
}

// AgentState says, per installed agent, whether it is connected.
type AgentState struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

type Agents struct {
	Home string // user's home
	Exe  string // absolute path of this binary, used in hook commands
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
	return &Agents{Home: home, Exe: exe}, nil
}

func (g *Agents) skillPath() string {
	return filepath.Join(envOr("XDG_DATA_HOME", filepath.Join(g.Home, ".local", "share")), "syschange", "skills", "syschange")
}

func (g *Agents) hookCommand(name string) string {
	return shellQuote(g.Exe) + " hook " + name
}

func (g *Agents) Status() ([]AgentState, error) {
	var out []AgentState
	for _, a := range agents(g.Home) {
		if !a.installed() {
			continue
		}
		hooked, err := hasHook(a.HookFile)
		if err != nil {
			return nil, err
		}
		out = append(out, AgentState{Name: a.Name, Connected: hooked && g.hasSkill(a)})
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

// Install connects every installed agent. It is safe to run again.
func (g *Agents) Install() ([]string, error) {
	if err := os.MkdirAll(g.skillPath(), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(g.skillPath(), "SKILL.md"), skillMD, 0o644); err != nil {
		return nil, err
	}
	var done []string
	for _, a := range agents(g.Home) {
		if !a.installed() {
			continue
		}
		for _, dir := range a.SkillDirs {
			if err := g.linkSkill(dir); err != nil {
				return done, err
			}
		}
		if err := g.addHook(a); err != nil {
			return done, err
		}
		done = append(done, a.Name)
	}
	if len(done) == 0 {
		return nil, errors.New("no supported agent found (Claude Code or Codex)")
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
		if _, err := os.Stat(a.HookFile); err == nil {
			if err := jqEdit(a.HookFile, `if .hooks.Stop then .hooks.Stop |= (map(.hooks |= map(select(.command | test($m) | not))) | map(select(.hooks | length > 0))) else . end`, "--arg", "m", hookMarker); err != nil {
				return err
			}
		}
	}
	return nil
}

// hookMarker finds our hook in a file whatever path the binary had.
const hookMarker = `syschange'? hook (claude|codex)$`

func hasHook(file string) (bool, error) {
	if _, err := os.Stat(file); err != nil {
		return false, nil
	}
	out, err := exec.Command("jq", "--arg", "m", hookMarker, `[.hooks.Stop[]?.hooks[]?.command | select(test($m))] | length > 0`, file).Output()
	if err != nil {
		return false, fmt.Errorf("jq %s: %w", file, err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func (g *Agents) addHook(a agent) error {
	if ok, err := hasHook(a.HookFile); err != nil || ok {
		return err
	}
	if _, err := os.Stat(a.HookFile); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(a.HookFile, []byte("{}\n"), 0o644); err != nil {
			return err
		}
	}
	return jqEdit(a.HookFile, `.hooks.Stop += [{"hooks": [{"type": "command", "command": $cmd, "timeout": 30}]}]`, "--arg", "cmd", g.hookCommand(a.Name))
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
