package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Suggest asks a coding agent for a one-line description of pending changes.
// The agent sees the diff, which never contains files that look like secrets.
func (s *Store) Suggest(items []string, agent string) (string, error) {
	pending, err := s.Pending()
	if err != nil {
		return "", err
	}
	chosen := Select(pending, items)
	if len(chosen) == 0 {
		return "", errors.New("nothing to describe: no pending change matches")
	}
	var diff bytes.Buffer
	if err := s.Diff(&diff, items, false); err != nil {
		return "", err
	}
	const maxDiff = 8000
	body := diff.String()
	if len(body) > maxDiff {
		body = body[:maxDiff] + "\n[diff truncated]"
	}
	if agent == "" {
		if agent = omarchyDefaultAgent(); agent == "" {
			agent = "claude"
		}
	}
	prompt := fmt.Sprintf(`You label changes to a Linux desktop setup (Arch, Hyprland, Omarchy).
Reply with ONE line of at most 80 characters that says what this change does for the user: its purpose, not file names.
No quotes, no preamble, no trailing period. Write it in the language of the locale %q.

Changed: %s

%s`, locale(), groupTitles(chosen), body)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := runAgent(ctx, agent, prompt)
	if err != nil {
		return "", err
	}
	line := lastLine(out)
	if line == "" {
		return "", fmt.Errorf("%s returned no text", agent)
	}
	return line, nil
}

// omarchyDefaultAgent is the agent the user picked in Omarchy (omarchy default
// agent), or "" when none is set.
func omarchyDefaultAgent() string {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config")), "omarchy", "defaults", "agent"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func locale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" {
			return v
		}
	}
	return "en_US"
}

// runAgent runs one non-interactive, tool-less prompt. Only agents with a
// headless mode are supported; Omarchy's lazy installers are never triggered
// for an agent the user did not choose.
func runAgent(ctx context.Context, agent, prompt string) (string, error) {
	// The prompt goes on stdin (or a private file), never on the command line,
	// where other local users could read it in /proc.
	var args []string
	var outFile string
	stdin := prompt
	switch agent {
	case "claude":
		args = []string{"claude", "-p", "--model", "haiku", "--tools", "", "--no-session-persistence"}
	case "codex":
		f, err := os.CreateTemp("", "syschange-suggest-")
		if err != nil {
			return "", err
		}
		f.Close()
		defer os.Remove(f.Name())
		outFile = f.Name()
		args = []string{"codex", "exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only", "-o", outFile, "-"}
	case "gemini":
		args = []string{"gemini", "-p", "Follow the instructions given on stdin."} // Gemini appends stdin to -p
	case "grok":
		f, err := os.CreateTemp("", "syschange-prompt-")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		f.WriteString(prompt)
		f.Close()
		args = []string{"grok", "--prompt-file", f.Name(), "--max-turns", "1"}
		stdin = ""
	default:
		return "", fmt.Errorf("describing with %q is not supported; use claude, codex, gemini or grok (syschange suggest --agent NAME)", agent)
	}
	if path, err := exec.LookPath(args[0]); err != nil || lazyInstaller(path) {
		return "", fmt.Errorf("%s is not installed", args[0])
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = os.TempDir() // not inside a project the agent might start exploring
	cmd.Env = append(os.Environ(), "SYSCHANGE_SUGGEST=1")
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return "", fmt.Errorf("%s failed: %v %s", agent, err, msg)
	}
	if outFile != "" {
		return readFileString(outFile)
	}
	return string(out), nil
}

// lazyInstaller spots Omarchy's stubs in ~/.local/bin that install an agent
// through mise the first time they run. Running one would install software
// the user never asked for.
func lazyInstaller(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 1024)
	n, _ := f.Read(head)
	return bytes.HasPrefix(head[:n], []byte("#!")) && bytes.Contains(head[:n], []byte("mise use -g"))
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// lastLine keeps the answer and drops any chatter or quotes around it.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimLeft(strings.TrimSpace(lines[i]), "-*• ")
		line = strings.Trim(line, "\"'`«»“”")
		line = strings.TrimSuffix(line, ".")
		if line != "" {
			if r := []rune(line); len(r) > 120 {
				line = string(r[:120])
			}
			return line
		}
	}
	return ""
}
