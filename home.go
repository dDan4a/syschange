package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Files outside the config directory that the user chose to track live in the
// same history, under "~/" paths ("~/.gitconfig"). Their index entries are
// skip-worktree, so git never looks for them in the config directory: syschange
// compares them itself, and reads and writes them only through os.Root($HOME).

const homePrefix = "~/"

func isHome(name string) bool { return strings.HasPrefix(name, homePrefix) }

// noHome are paths under $HOME that are never tracked: keys, password stores,
// credential files whose format the secret filter does not know, shell history.
var noHome = []string{".ssh", ".gnupg", ".password-store", ".local/share/keyrings", ".pki", ".netrc", ".pgpass",
	".git-credentials", ".kube", ".aws", ".docker", ".azure", ".vault-token", ".local/share/fish/fish_history"}

func (s *Store) trackedFile() string { return s.path("tracked.txt") }

func (s *Store) trackedFiles() ([]string, error) { return readLines(s.trackedFile()) }

// homeName turns "~/x", an absolute path or a path relative to the working
// directory into the history's name for it, "~/x".
func (s *Store) homeName(p string) (string, error) {
	if s.HomeDir == "" {
		return "", errors.New("no home directory")
	}
	if rel, ok := strings.CutPrefix(p, homePrefix); ok {
		return homePrefix + filepath.ToSlash(filepath.Clean(rel)), nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.HomeDir, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is not inside %s", p, s.HomeDir)
	}
	return homePrefix + filepath.ToSlash(rel), nil
}

// homeAllowed says whether a path under $HOME may be tracked, written or
// removed: never the config directory (tracked as is), syschange's own state,
// key stores or paths the excludes call secret.
func (s *Store) homeAllowed(rel string) error {
	if s.HomeDir == "" {
		return errors.New("no home directory")
	}
	if !safePath(rel) || rel == "." {
		return fmt.Errorf("refusing unsafe path %q", rel)
	}
	abs := filepath.Join(s.HomeDir, rel)
	for _, dir := range []string{s.ConfigDir, s.StateDir} {
		if r, err := filepath.Rel(dir, abs); err == nil && filepath.IsLocal(r) || abs == dir {
			return fmt.Errorf("~/%s is inside %s, which syschange manages itself", rel, dir)
		}
	}
	for _, d := range noHome {
		if rel == d || strings.HasPrefix(rel, d+"/") || strings.HasSuffix(rel, "_history") {
			return fmt.Errorf("~/%s may hold keys or passwords: syschange never tracks it", rel)
		}
	}
	// Exit status 1 alone means no exclude pattern (*token*, *.key, id_rsa* ...)
	// matches; anything else refuses. The path is checked as if it were in the
	// config dir: "~/x" would match "*~". check-ignore rejects
	// --literal-pathspecs, so it runs without s.git's flags.
	cmd := exec.Command("git", "--git-dir="+s.gitDir(), "--work-tree="+s.ConfigDir, "check-ignore", "-q", "--no-index", "--", rel)
	cmd.Dir = s.ConfigDir
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return fmt.Errorf("~/%s matches an exclude pattern (see syschange ignore)", rel)
	}
	return nil
}

// readHome reads a regular file under $HOME. Through os.Root, no symlink or
// ".." leads outside it; the file itself is checked on the open descriptor, so
// a symlink, a FIFO or a file growing past the limit swapped in meanwhile is refused.
func (s *Store) readHome(rel string) ([]byte, error) {
	root, err := os.OpenRoot(s.HomeDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("~/%s is a symlink", rel)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("~/%s is not a regular file", rel)
	}
	body, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err == nil && len(body) > maxFileSize {
		return nil, fmt.Errorf("~/%s is larger than %d KB", rel, maxFileSize>>10)
	}
	return body, err
}

// homeContent is a tracked file's content when syschange may keep a copy of it.
// exists is false only when the file is gone.
func (s *Store) homeContent(name string) (body []byte, ok, exists bool) {
	rel := strings.TrimPrefix(name, homePrefix)
	body, err := s.readHome(rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false
	}
	return body, err == nil && s.homeAllowed(rel) == nil && plainText(body), true
}

// Track starts tracking files under $HOME. Their current content becomes the
// baseline, so only later edits show up as changes.
func (s *Store) Track(paths []string) error {
	if !s.Initialized() {
		return ErrNotInitialized
	}
	names, err := s.trackedFiles()
	if err != nil {
		return err
	}
	var add []string
	for _, p := range paths {
		name, err := s.homeName(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(name, homePrefix)
		if err := s.homeAllowed(rel); err != nil {
			return err
		}
		body, err := s.readHome(rel)
		if err != nil {
			return err
		}
		if !plainText(body) {
			return fmt.Errorf("%s looks like it holds a secret or binary data: not tracking it", name)
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
			add = append(add, name)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := writeLines(s.trackedFile(), names); err != nil {
		return err
	}
	if err := s.stageHome(nil, add); err != nil {
		return err
	}
	return s.commitStaged("track " + strings.Join(add, " "))
}

// Untrack stops tracking files under $HOME. Their past versions stay in the history.
func (s *Store) Untrack(paths []string) error {
	if !s.Initialized() {
		return ErrNotInitialized
	}
	names, err := s.trackedFiles()
	if err != nil {
		return err
	}
	var drop []string
	for _, p := range paths {
		name, err := s.homeName(p)
		if err != nil {
			return err
		}
		if !slices.Contains(names, name) {
			return fmt.Errorf("%s is not tracked", name)
		}
		drop = append(drop, name)
	}
	names = slices.DeleteFunc(names, func(n string) bool { return slices.Contains(drop, n) })
	if err := writeLines(s.trackedFile(), names); err != nil {
		return err
	}
	if err := s.updateIndex(nil, nil, drop); err != nil {
		return err
	}
	return s.commitStaged("untrack " + strings.Join(drop, " "))
}

func (s *Store) commitStaged(msg string) error {
	staged, err := s.git(nil, "diff", "--cached", "--name-only")
	if err != nil || len(staged) == 0 {
		return err
	}
	_, err = s.git(nil, "commit", "--quiet", "-m", msg)
	return err
}

// stageHome puts tracked files' current content into the index (env picks a
// private one): gone files leave it, files it may not keep are left as they were.
func (s *Store) stageHome(env []string, names []string) error {
	var blobs [][2]string // name, sha
	var gone []string
	for _, name := range names {
		body, ok, exists := s.homeContent(name)
		switch {
		case !exists:
			gone = append(gone, name)
		case ok:
			sha, err := s.git(body, "hash-object", "-w", "--stdin")
			if err != nil {
				return err
			}
			blobs = append(blobs, [2]string{name, strings.TrimSpace(string(sha))})
		}
	}
	return s.updateIndex(env, blobs, gone)
}

func (s *Store) updateIndex(env []string, blobs [][2]string, gone []string) error {
	var info []string
	var added []string
	for _, b := range blobs {
		info = append(info, "100644 "+b[1]+"\t"+b[0])
		added = append(added, b[0])
	}
	for _, name := range gone {
		info = append(info, "0 0000000000000000000000000000000000000000\t"+name)
	}
	if len(info) == 0 {
		return nil
	}
	if _, err := s.gitEnv(env, nulJoin(info), "update-index", "-z", "--index-info"); err != nil {
		return err
	}
	if len(added) == 0 {
		return nil
	}
	_, err := s.gitEnv(env, nulJoin(added), "update-index", "-z", "--skip-worktree", "--stdin")
	return err
}

// recorded returns the content of tracked files under $HOME at rev.
func (s *Store) recorded(rev string) (map[string][]byte, error) {
	out, err := s.git(nil, "ls-tree", "-r", "-z", "--name-only", rev, "--", "~")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, name := range splitNul(out) {
		body, err := s.git(nil, "show", rev+":"+name)
		if err != nil {
			return nil, err
		}
		files[name] = body
	}
	return files, nil
}

func (s *Store) homeChanges() ([]Change, error) {
	names, err := s.trackedFiles()
	if err != nil || len(names) == 0 {
		return nil, err
	}
	head, err := s.recorded("HEAD")
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, name := range names {
		body, ok, exists := s.homeContent(name)
		old, inHead := head[name]
		// The group is ~/.claude, so the label is what is inside: settings.json.
		label := strings.TrimPrefix(name, homePrefix)
		if _, inner, ok := strings.Cut(label, "/"); ok {
			label = inner
		}
		c := Change{ID: name, Kind: "config", Name: name, Label: label}
		switch {
		case inHead && !exists:
			c.Op = "removed"
		case !ok:
			continue
		case !inHead:
			c.Op = "added"
		case string(old) != string(body):
			c.Op = "modified"
		default:
			continue
		}
		if !inHead {
			old = nil
		}
		c.Added, c.Removed = numstat(diffBlobs(name, old, body, "--numstat"))
		changes = append(changes, c)
	}
	return changes, nil
}

func numstat(out []byte) (added, removed int) {
	fmt.Sscanf(string(out), "%d\t%d", &added, &removed)
	return added, removed
}

// diffBlobs diffs two versions of a file, nil meaning absent, labelled
// a/name and b/name like git diff does.
func diffBlobs(name string, old, now []byte, args ...string) []byte {
	tmp, err := os.MkdirTemp("", "syschange-diff-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(tmp)
	sides := []string{"/dev/null", "/dev/null"}
	for i, body := range [][]byte{old, now} {
		if body == nil {
			continue
		}
		side := filepath.Join([]string{"a", "b"}[i], name)
		if os.MkdirAll(filepath.Dir(filepath.Join(tmp, side)), 0o700) != nil ||
			os.WriteFile(filepath.Join(tmp, side), body, 0o600) != nil {
			return nil
		}
		sides[i] = side
	}
	cmd := exec.Command("git", append(append([]string{"diff", "--no-index", "--no-prefix"}, args...), "--", sides[0], sides[1])...)
	cmd.Dir = tmp
	out, _ := cmd.Output() // exit status 1 just means "files differ"
	return out
}

// realPath is where a change's file lives on disk.
func (s *Store) realPath(name string) string {
	if rel, ok := strings.CutPrefix(name, homePrefix); ok {
		return filepath.Join(s.HomeDir, rel)
	}
	return filepath.Join(s.ConfigDir, name)
}

// currentEntry reads a file the way git stores it, through os.Root for $HOME.
func (s *Store) currentEntry(name string) ([]byte, bool) {
	if isHome(name) {
		body, err := s.readHome(strings.TrimPrefix(name, homePrefix))
		return body, err == nil
	}
	return readEntry(s.realPath(name))
}

// canon turns absolute paths, as a shell expands ~/x, into change IDs.
func (s *Store) canon(items []string) []string {
	out := slices.Clone(items)
	for i, it := range out {
		if !filepath.IsAbs(it) {
			continue
		}
		if rel, err := filepath.Rel(s.ConfigDir, it); err == nil && filepath.IsLocal(rel) {
			out[i] = filepath.ToSlash(rel)
		} else if name, err := s.homeName(it); err == nil {
			out[i] = name
		}
	}
	return out
}
