package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Store is the on-disk state: a shadow git repository whose work tree is the
// config directory, plus accepted snapshots of packages and services.
type Store struct {
	ConfigDir string // the tracked directory, normally ~/.config
	StateDir  string // ~/.local/state/syschange

	// Listers are swapped out in tests.
	ListPackages func() ([]string, error)
	ListUnits    func() ([]string, error)
}

func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	config := os.Getenv("SYSCHANGE_CONFIG_DIR")
	if config == "" {
		config = envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	}
	state := filepath.Join(envOr("XDG_STATE_HOME", filepath.Join(home, ".local", "state")), "syschange")
	return &Store{ConfigDir: config, StateDir: state, ListPackages: pacmanExplicit, ListUnits: enabledUnits}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var ErrNotInitialized = errors.New("syschange is not initialized, run: syschange init")

func (s *Store) gitDir() string      { return filepath.Join(s.StateDir, "config.git") }
func (s *Store) excludeFile() string { return filepath.Join(s.gitDir(), "info", "exclude") }
func (s *Store) path(name string) string {
	return filepath.Join(s.StateDir, name)
}

func (s *Store) Initialized() bool {
	_, err := os.Stat(s.gitDir())
	return err == nil
}

// git runs git against the shadow repository. Pathspecs are literal, so a file
// named "*" or ":(glob)x" is just a file.
func (s *Store) git(stdin []byte, args ...string) ([]byte, error) {
	base := []string{
		"--git-dir=" + s.gitDir(), "--work-tree=" + s.ConfigDir, "--literal-pathspecs",
		"-c", "core.quotepath=off", "-c", "user.name=syschange", "-c", "user.email=syschange@localhost",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
	}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = s.ConfigDir // git apply resolves paths against the working directory
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Init starts tracking. Run again, it only refreshes the Omarchy hooks.
func (s *Store) Init() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if s.Initialized() {
		created, err := s.installOmarchyHooks(exe)
		if err != nil || len(created) == 0 {
			return err
		}
		_, err = s.Record(created, "Record Omarchy updates, theme and font changes automatically", "syschange")
		return err
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return err
	}
	// The history holds copies of config files, so keep it private.
	if err := os.Chmod(s.StateDir, 0o700); err != nil {
		return err
	}
	if _, err := s.git(nil, "init", "--quiet"); err != nil {
		return err
	}
	if err := os.WriteFile(s.excludeFile(), []byte(defaultExcludes), 0o600); err != nil {
		return err
	}
	if err := s.autoExclude(); err != nil {
		return err
	}
	if _, err := s.installOmarchyHooks(exe); err != nil {
		return err
	}
	files, err := s.untracked()
	if err != nil {
		return err
	}
	if _, err := s.git(nulJoin(files), "add", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
		return err
	}
	if _, err := s.git(nil, "commit", "--quiet", "--allow-empty", "-m", "syschange init"); err != nil {
		return err
	}
	for _, snap := range []struct {
		list func() ([]string, error)
		name string
	}{{s.ListPackages, "packages"}, {s.ListUnits, "units"}} {
		items, err := snap.list()
		if err != nil {
			return err
		}
		// The baseline lets export say what changed since syschange was set up.
		for _, f := range []string{snap.name + ".txt", snap.name + ".init.txt"} {
			if err := writeLines(s.path(f), items); err != nil {
				return err
			}
		}
	}
	return nil
}

// untracked lists new files that are worth tracking: text files below the
// size limit, outside excluded paths.
func (s *Store) untracked() ([]string, error) {
	out, err := s.git(nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range splitNul(out) {
		if s.trackable(f) {
			files = append(files, f)
		}
	}
	return files, nil
}

const maxFileSize = 256 << 10

func (s *Store) trackable(rel string) bool {
	full := filepath.Join(s.ConfigDir, rel)
	info, err := os.Lstat(full)
	if err != nil || info.Size() > maxFileSize {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if !info.Mode().IsRegular() {
		return false
	}
	head := make([]byte, 8000)
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()
	n, _ := f.Read(head)
	if bytes.Contains(head[:n], []byte{0}) {
		return false
	}
	// The history must not become a second copy of your passwords.
	body, err := os.ReadFile(full)
	return err == nil && !secretLike.Match(body)
}

// secretLike matches a password, token or key assignment with a real value.
var secretLike = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|private[_-]?key|client[_-]?secret)["']?\s*[:=]\s*["']?[^"'\s,}{]{6,}`)

// autoExclude excludes directories that look like browser or Electron
// profiles, up to two levels deep. They change on every launch and are app
// state, not setup.
func (s *Store) autoExclude() error {
	current, err := readLines(s.excludeFile())
	if err != nil {
		return err
	}
	var add []string
	var scan func(rel string, depth int)
	scan = func(rel string, depth int) {
		entries, _ := os.ReadDir(filepath.Join(s.ConfigDir, rel))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			child := filepath.Join(rel, e.Name())
			pattern := "/" + filepath.ToSlash(child) + "/"
			if slices.Contains(current, pattern) {
				continue
			}
			if isAppProfile(filepath.Join(s.ConfigDir, child)) {
				add = append(add, pattern)
			} else if depth < 2 {
				scan(child, depth+1)
			}
		}
	}
	scan("", 1)
	if len(add) == 0 {
		return nil
	}
	return appendLines(s.excludeFile(), add)
}

func isAppProfile(dir string) bool {
	for _, marker := range []string{"Local Storage", "GPUCache", "Cookies", "Preferences"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, sc.Err()
}

func writeLines(path string, lines []string) error {
	slices.Sort(lines)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func appendLines(path string, lines []string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func splitNul(b []byte) []string {
	var out []string
	for _, p := range bytes.Split(b, []byte{0}) {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out
}

func nulJoin(items []string) []byte {
	var b bytes.Buffer
	for _, it := range items {
		b.WriteString(it)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// pacmanExplicit lists explicitly installed packages, AUR ones included.
func pacmanExplicit() ([]string, error) {
	out, err := exec.Command("pacman", "-Qqe").Output()
	if err != nil {
		return nil, fmt.Errorf("pacman -Qqe: %w", err)
	}
	return strings.Fields(string(out)), nil
}

// enabledUnits lists enabled systemd units as "system name" / "user name".
func enabledUnits() ([]string, error) {
	var units []string
	for _, scope := range []string{"system", "user"} {
		args := []string{"list-unit-files", "--state=enabled", "--no-legend", "--no-pager"}
		if scope == "user" {
			args = append([]string{"--user"}, args...)
		}
		out, err := exec.Command("systemctl", args...).Output()
		if err != nil {
			return nil, fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				units = append(units, scope+" "+f[0])
			}
		}
	}
	return units, nil
}

// defaultExcludes keeps app state, caches and secrets out of the history.
const defaultExcludes = `# syschange: paths never recorded. Add your own below, one gitignore pattern per line.
# App state and caches
*.sqlite
*.sqlite-*
*.db
*.db-*
*.log
*.lock
*.pid
*.sock
*.tmp
*.bak
*.bak.*
*.backup*
*.omarchy-upgrade-*
*~
.#*
*.swp
Cache/
cache/
*Cache/
logs/
Crashpad/
__pycache__/
/dconf/
/pulse/
/procps/
/QtProject.conf
/user-dirs.locale
/tool_state
/ibus/bus/
/glab-cli/recover/
/herdr/session.json
/herdr/release-notes.json
/vscode-sqltools/runningInfo.json
/omamail/window.json
/omamail/compose.json
/gopls/prompt/
/go/telemetry/
/libreoffice/
/Pinta/addins/
/systemd/user/*.wants/
/systemd/user/*.requires/
# Browsers
/chromium*/
/google-chrome*/
/BraveSoftware/
/microsoft-edge*/
/vivaldi*/
/opera*/
/mozilla/
# Secrets: never copied into the history
*token*
*secret*
*credential*
*password*
*.key
*.pem
*.p12
*.kdbx
id_rsa*
id_ed25519*
/gh/hosts.yml
/glab-cli/config.yml
/gcloud/
/gws*/
/1Password/
/rclone/
/Happ/
/nats/context/
/lazysql/
`
