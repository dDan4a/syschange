// Command syschange keeps a history of changes to your system setup: config
// files in ~/.config, installed packages and enabled services. Describe each
// change in one line, see what changed and undo it.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// version is the CLI release; the Omarchy plugin asks for at least its own.
const version = "0.3.1"

const usage = `syschange keeps a history of changes to your system setup.

Usage:
  syschange init                       start tracking (~/.config, packages, services);
                                       with Omarchy, its updates and theme changes record themselves
  syschange status [--json]            show changes not recorded yet
  syschange note [--by NAME] TEXT [ID...]
                                       record changes with a one-line reason
  syschange skip [ID...]               record changes without a reason
  syschange suggest [--agent NAME] [ID...]
                                       ask an AI agent for a one-line description
  syschange suggest --all [--send] [--agent NAME]
                                       describe every app's changes with AI and record them;
                                       --send shows the result as a notification
  syschange diff [--color] [ID...]     show what changed in config files
  syschange revert ID...               undo config changes
  syschange ignore PATH                never track PATH (a gitignore pattern)
  syschange track [FILE...]            also track these files under your home folder (~/.gitconfig),
                                       or list the tracked ones
  syschange untrack FILE...            stop tracking them
  syschange log [--grep TEXT] [--json] show the history, or the entries that mention TEXT
                                       in their description, files, packages or diff
  syschange checkpoint [NAME]          save the whole setup now, unrecorded edits included
  syschange checkpoints                list them
  syschange restore NAME               bring config files back to a checkpoint
  syschange undo ENTRY [PATH...]       undo a recorded entry (its commit, or time from status --json),
                                       or only these files of it
  syschange remote [set URL | off]     back the history up to a private git repository
  syschange push                       push now (it happens by itself after every change)
  syschange apply [--yes] BACKUP       bring a backed-up setup to this machine: files, packages, services
  syschange digest [--days N]          sum up the last N days (default 7)
  syschange notify [--send] [--remind-days N] [--no-digest]
                                       notifications due now, shown (--send) or as JSON: a reminder when changes
                                       wait unrecorded N days (default 3, 0 off), once a day,
                                       and last week's digest, once a week
  syschange export [--to FILE]         history and setup steps as Markdown
  syschange export --auto FILE|off     keep FILE updated after every record
  syschange agents [install [NAME...]|remove]
                                       connect Claude Code, Codex, Gemini CLI, Grok: skill + hooks
  syschange hook AGENT [start]         the hooks themselves (read stdin)
  syschange version                    print the version

ID is a file path inside ~/.config (hypr/bindings.conf), an app directory (hypr),
a tracked file (~/.gitconfig), a package or unit name, or a group: packages, services.
No ID means all changes.
`

// newStore is swapped out in tests, so the CLI runs against a sandbox.
var newStore = DefaultStore

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "syschange:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return nil
	}

	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return nil
	}

	s, err := newStore()
	if err != nil {
		return err
	}

	name := args[0]

	opts, rest, err := parseFlags(name, args[1:])
	if err != nil {
		return err
	}

	cmd, ok := commands[name]
	if !ok {
		return fmt.Errorf("unknown command %q, see syschange help", name)
	}

	return cmd(&invocation{sharedFlags: opts, s: s, args: rest, stdin: stdin, stdout: stdout})
}

type sharedFlags struct {
	asJSON, color, yes, quiet, send, noDigest, all bool
	exportTo, autoExport, grep, agent, by          string
	days, remindDays                               int
}

func parseFlags(cmd string, args []string) (sharedFlags, []string, error) {
	var f sharedFlags

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.BoolVar(&f.asJSON, "json", false, "machine-readable output")
	fs.BoolVar(&f.color, "color", false, "colored diff even when not writing to a terminal")
	fs.BoolVar(&f.yes, "yes", false, "apply: do it without asking")
	fs.BoolVar(&f.quiet, "quiet", false, "push: print nothing")
	fs.StringVar(&f.exportTo, "to", "", "export: write to this file instead of stdout")
	fs.StringVar(&f.autoExport, "auto", "", "export: keep this file updated after every record; off to stop")
	fs.IntVar(&f.days, "days", 7, "digest: how many days back")
	fs.IntVar(&f.remindDays, "remind-days", 3, "notify: remind about changes unrecorded this many days; 0 never")
	fs.BoolVar(&f.send, "send", false, "notify, suggest --all: show the notifications instead of printing them")
	fs.BoolVar(&f.noDigest, "no-digest", false, "notify: no weekly digest")
	fs.StringVar(&f.grep, "grep", "", "log: only entries that mention this text")
	fs.StringVar(&f.agent, "agent", "", "agent for suggest; default: Omarchy's default agent")
	fs.BoolVar(&f.all, "all", false, "suggest: describe and record every pending group")
	fs.StringVar(&f.by, "by", defaultAuthor(), "who made the change, for example an agent name")

	if err := fs.Parse(args); err != nil {
		return f, nil, err
	}

	return f, fs.Args(), nil
}

type invocation struct {
	sharedFlags

	s      *Store
	args   []string
	stdin  io.Reader
	stdout io.Writer
}

// defaultAuthor tags records made from inside an agent that marks its shell, without the agent
// having to remember --by.
func defaultAuthor() string {
	switch {
	case os.Getenv("CLAUDECODE") == "1":
		return agentClaude
	case os.Getenv("GEMINI_CLI") == "1":
		return agentGemini
	}

	return ""
}

func expandHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && (p == "~" || strings.HasPrefix(p, "~/")) {
		return home + p[1:]
	}

	return p
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
