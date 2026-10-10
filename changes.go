package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Change is one pending difference from the accepted state.
type Change struct {
	ID      string `json:"id"`   // "hypr/bindings.conf", "pkg:voxtype", "unit:user ydotool.service"
	Kind    string `json:"kind"` // config, package, unit
	Op      string `json:"op"`   // added, modified, removed
	Name    string `json:"name"` // file path, package or unit name
	Label   string `json:"label"`
	Added   int    `json:"added,omitempty"` // changed lines, config files only
	Removed int    `json:"removed,omitempty"`
}

// Group collects changes that belong to one app (or packages, or services).
type Group struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Changes []Change `json:"changes"`
}

func (s *Store) Pending() ([]Change, error) {
	if !s.Initialized() {
		return nil, ErrNotInitialized
	}
	if err := s.autoExclude(); err != nil {
		return nil, err
	}
	changes, err := s.configChanges()
	if err != nil {
		return nil, err
	}
	for _, src := range []struct {
		kind, file string
		list       func() ([]string, error)
	}{{"package", "packages.txt", s.ListPackages}, {"unit", "units.txt", s.ListUnits}} {
		accepted, err := readLines(s.path(src.file))
		if err != nil {
			return nil, err
		}
		now, err := src.list()
		if err != nil {
			return nil, err
		}
		changes = append(changes, setChanges(src.kind, accepted, now)...)
	}
	return changes, nil
}

func (s *Store) configChanges() ([]Change, error) {
	out, err := s.git(nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	stat, err := s.numstat()
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, entry := range splitNul(out) {
		if len(entry) < 4 {
			continue
		}
		code, path := entry[:2], entry[3:]
		var op string
		switch {
		case code == "??":
			op = "added"
			stat[path] = [2]int{countLines(filepath.Join(s.ConfigDir, path)), 0}
		case strings.Contains(code, "D"):
			op = "removed"
		default:
			op = "modified"
		}
		if op != "removed" && !s.trackable(path) {
			continue
		}
		changes = append(changes, Change{
			ID: path, Kind: "config", Op: op, Name: path, Label: fileLabel(path),
			Added: stat[path][0], Removed: stat[path][1],
		})
	}
	return changes, nil
}

func (s *Store) numstat() (map[string][2]int, error) {
	out, err := s.git(nil, "diff", "--numstat", "-z", "--no-renames", "HEAD")
	if err != nil {
		return nil, err
	}
	stat := map[string][2]int{}
	for _, rec := range splitNul(out) {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 {
			continue
		}
		added, _ := strconv.Atoi(f[0]) // "-" for binary files
		removed, _ := strconv.Atoi(f[1])
		stat[f[2]] = [2]int{added, removed}
	}
	return stat, nil
}

func countLines(path string) int {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func setChanges(kind string, accepted, now []string) []Change {
	var changes []Change
	for _, name := range now {
		if !slices.Contains(accepted, name) {
			changes = append(changes, itemChange(kind, "added", name))
		}
	}
	for _, name := range accepted {
		if !slices.Contains(now, name) {
			changes = append(changes, itemChange(kind, "removed", name))
		}
	}
	return changes
}

func itemChange(kind, op, name string) Change {
	prefix := "pkg:"
	label := name
	if kind == "unit" {
		prefix = "unit:"
		// "user ydotool.service" -> "ydotool.service (user)"
		if scope, unit, ok := strings.Cut(name, " "); ok {
			label = unit
			if scope == "user" {
				label += " (user)"
			}
		}
	}
	return Change{ID: prefix + name, Kind: kind, Op: op, Name: name, Label: label}
}

// Groups orders changes for display: config apps by name, then packages, then services.
func Groups(changes []Change) []Group {
	var groups []Group
	index := map[string]int{}
	for _, c := range changes {
		key, title := groupOf(c)
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, Group{Key: key, Title: title})
		}
		groups[i].Changes = append(groups[i].Changes, c)
	}
	rank := func(g Group) int {
		switch g.Key {
		case "packages":
			return 1
		case "services":
			return 2
		}
		return 0
	}
	slices.SortStableFunc(groups, func(a, b Group) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})
	return groups
}

func groupOf(c Change) (key, title string) {
	switch c.Kind {
	case "package":
		return "packages", "Packages"
	case "unit":
		return "services", "Services"
	}
	top, _, _ := strings.Cut(c.Name, "/")
	return top, appName(top)
}

// Select picks the changes named by items: a change ID, a group key, or a
// package/unit name. No items selects everything.
func Select(changes []Change, items []string) []Change {
	if len(items) == 0 {
		return changes
	}
	var out []Change
	for _, c := range changes {
		key, _ := groupOf(c)
		for _, it := range items {
			it = strings.TrimSuffix(it, "/")
			if it == c.ID || it == key || (c.Kind != "config" && it == c.Name) ||
				(c.Kind == "config" && strings.HasPrefix(c.Name, it+"/")) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

var apps = map[string]string{
	"hypr": "Hyprland", "omarchy": "Omarchy", "alacritty": "Alacritty", "ghostty": "Ghostty",
	"kitty": "Kitty", "foot": "Foot", "walker": "Walker", "elephant": "Elephant", "mako": "Mako",
	"swayosd": "SwayOSD", "waybar": "Waybar", "nvim": "Neovim", "git": "Git", "fish": "Fish",
	"uwsm": "UWSM", "environment.d": "Environment", "systemd": "systemd", "voxtype": "Voxtype",
	"fontconfig": "Fonts", "gtk-3.0": "GTK 3", "gtk-4.0": "GTK 4", "btop": "btop",
	"lazygit": "lazygit", "tmux": "tmux", "starship.toml": "Starship", "mimeapps.list": "Default apps",
	"user-dirs.dirs": "User folders", "autostart": "Autostart", "fcitx5": "Fcitx5", "mise": "mise",
	"xdg-desktop-portal": "Desktop portals", "gh": "GitHub CLI", "rtk": "RTK", "xkb": "Keyboard layouts",
	"wireplumber": "Audio (WirePlumber)", "pipewire": "Audio (PipeWire)", "opencode": "OpenCode",
}

func appName(top string) string {
	if name, ok := apps[top]; ok {
		return name
	}
	return top
}

var fileLabels = map[string]string{
	"hypr/bindings": "Key bindings", "hypr/monitors": "Monitors", "hypr/input": "Input",
	"hypr/looknfeel": "Look and feel", "hypr/envs": "Environment variables", "hypr/autostart": "Autostart",
	"hypr/hypridle": "Idle and sleep", "hypr/hyprlock": "Lock screen", "hypr/hyprland": "Main config",
	"hypr/hyprsunset": "Night light", "hypr/windows": "Window rules", "hypr/workspaces": "Workspaces",
	"omarchy/shell": "Shell and bar settings",
}

// fileLabel names a config file the way a person would: "Key bindings" for
// hypr/bindings.conf, otherwise its path inside the app directory.
func fileLabel(path string) string {
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	if label, ok := fileLabels[stem]; ok {
		return label
	}
	parts := strings.Split(path, "/")
	switch {
	case len(parts) >= 3 && parts[0] == "omarchy" && parts[1] == "themes":
		return "Theme " + parts[2] + ": " + strings.Join(parts[3:], "/")
	case len(parts) == 3 && parts[0] == "omarchy" && parts[1] == "plugins":
		return "Plugin " + parts[2]
	case len(parts) >= 2 && parts[0] == "omarchy" && parts[1] == "hooks":
		return "Hook " + strings.Join(parts[2:], "/")
	case len(parts) >= 3 && parts[0] == "systemd" && parts[1] == "user":
		return "User service " + strings.Join(parts[2:], "/")
	case len(parts) == 1:
		return parts[0]
	}
	return strings.Join(parts[1:], "/")
}
