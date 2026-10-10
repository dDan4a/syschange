// Command syschange keeps a history of changes to your system setup: config
// files in ~/.config, installed packages and enabled services. Describe each
// change in one line, see what changed and undo it.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"
)

const usage = `syschange keeps a history of changes to your system setup.

Usage:
  syschange init                       start tracking (~/.config, packages, services)
  syschange status [--json]            show changes not recorded yet
  syschange note [--by NAME] TEXT [ID...]
                                       record changes with a one-line reason
  syschange skip [ID...]               record changes without a reason
  syschange diff [--color] [ID...]     show what changed in config files
  syschange revert ID...               undo config changes
  syschange ignore PATH                never track PATH (a gitignore pattern)
  syschange log                        show the history
  syschange export                     history and setup steps as Markdown
  syschange agents [install|remove]     connect Claude Code, Codex, Gemini CLI, Grok: skill + hooks
  syschange hook AGENT [start]         the hooks themselves (read stdin)

ID is a file path inside ~/.config (hypr/bindings.conf), an app directory (hypr),
a package or unit name, or a group: packages, services. No ID means all changes.
`

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
	s, err := DefaultStore()
	if err != nil {
		return err
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	color := fs.Bool("color", false, "colored diff even when not writing to a terminal")
	by := fs.String("by", defaultAuthor(), "who made the change, for example an agent name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	args = fs.Args()

	switch cmd {
	case "init":
		if err := s.Init(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Tracking %s. History lives in %s.\n", s.ConfigDir, s.StateDir)
		return nil
	case "status":
		return status(s, stdout, *asJSON)
	case "note":
		if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
			return errors.New("note needs a text: syschange note \"why\" [ID...]")
		}
		e, err := s.Record(args[1:], args[0], *by)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Recorded %d change(s).\n", len(e.Changes))
		return nil
	case "skip":
		e, err := s.Record(args, "", *by)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Recorded %d change(s) without a description.\n", len(e.Changes))
		return nil
	case "diff":
		return s.Diff(stdout, args, *color || isTerminal(stdout))
	case "revert":
		if len(args) == 0 {
			return errors.New("revert needs at least one ID")
		}
		return s.Revert(args)
	case "ignore":
		if len(args) != 1 {
			return errors.New("ignore takes one path")
		}
		return s.Ignore(args[0])
	case "log":
		return printLog(s, stdout)
	case "export":
		return s.Export(stdout)
	case "hook":
		if len(args) == 0 || !slices.Contains([]string{"claude", "codex", "gemini", "grok"}, args[0]) {
			return errors.New("usage: syschange hook claude|codex|gemini|grok [start]")
		}
		if len(args) > 1 && args[1] == "start" {
			return s.SessionStartHook(args[0], stdin)
		}
		return s.StopHook(args[0], stdin, stdout)
	case "agents":
		return agentsCmd(args, stdout)
	}
	return fmt.Errorf("unknown command %q, see syschange help", cmd)
}

// defaultAuthor tags records made from inside an agent that marks its shell, without the agent
// having to remember --by.
func defaultAuthor() string {
	switch {
	case os.Getenv("CLAUDECODE") == "1":
		return "claude"
	case os.Getenv("GEMINI_CLI") == "1":
		return "gemini"
	}
	return ""
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

type statusJSON struct {
	Initialized bool         `json:"initialized"`
	Count       int          `json:"count"`
	Pending     []Group      `json:"pending"`
	History     []Entry      `json:"history"`
	ConfigDir   string       `json:"configDir"`
	Agents      []AgentState `json:"agents"`
}

func status(s *Store, w io.Writer, asJSON bool) error {
	out := statusJSON{Initialized: s.Initialized(), ConfigDir: s.ConfigDir, Pending: []Group{}, History: []Entry{}, Agents: []AgentState{}}
	if g, err := DefaultAgents(); err == nil {
		if states, err := g.Status(); err == nil {
			out.Agents = append(out.Agents, states...)
		}
	}
	if out.Initialized {
		pending, err := s.Pending()
		if err != nil {
			return err
		}
		out.Count = len(pending)
		out.Pending = Groups(pending)
		history, err := s.History()
		if err != nil {
			return err
		}
		out.History = history[:min(len(history), 50)]
	}
	if asJSON {
		return json.NewEncoder(w).Encode(out)
	}
	if !out.Initialized {
		return ErrNotInitialized
	}
	if out.Count == 0 {
		fmt.Fprintln(w, "Nothing new. Everything is recorded.")
		return nil
	}
	for _, g := range out.Pending {
		fmt.Fprintf(w, "%s  [%s]\n", g.Title, g.Key)
		for _, c := range g.Changes {
			fmt.Fprintf(w, "  %s %s%s\n", opSign(c.Op), c.Label, lineStat(c))
		}
	}
	fmt.Fprintln(w, "\nRecord with: syschange note \"why\" [ID...]   undo with: syschange revert ID...")
	return nil
}

func opSign(op string) string {
	switch op {
	case "added":
		return "+"
	case "removed":
		return "-"
	}
	return "~"
}

func lineStat(c Change) string {
	if c.Kind != "config" || (c.Added == 0 && c.Removed == 0) {
		return ""
	}
	return fmt.Sprintf("  +%d -%d", c.Added, c.Removed)
}

func agentsCmd(args []string, w io.Writer) error {
	g, err := DefaultAgents()
	if err != nil {
		return err
	}
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "install":
		done, err := g.Install()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Connected: %s. Agents now describe the system changes they make.\n", strings.Join(done, ", "))
		return nil
	case "remove":
		if err := g.Remove(); err != nil {
			return err
		}
		fmt.Fprintln(w, "Disconnected all agents.")
		return nil
	case "status":
		states, err := g.Status()
		if err != nil {
			return err
		}
		if len(states) == 0 {
			fmt.Fprintln(w, "No supported agent found (Claude Code, Codex, Gemini CLI or Grok).")
		}
		for _, st := range states {
			state := "not connected, run: syschange agents install"
			if st.Connected {
				state = "connected"
			}
			fmt.Fprintf(w, "%s: %s\n", st.Name, state)
		}
		return nil
	}
	return fmt.Errorf("unknown agents action %q: use install, remove or status", action)
}

func printLog(s *Store, w io.Writer) error {
	history, err := s.History()
	if err != nil {
		return err
	}
	for _, e := range history {
		note := e.Note
		if note == "" {
			note = "(no description)"
		}
		if e.By != "" {
			note += "  [" + e.By + "]"
		}
		fmt.Fprintf(w, "%s  %s\n", e.Time.Format("2006-01-02 15:04"), note)
		for _, c := range e.Changes {
			fmt.Fprintf(w, "    %s %s\n", opSign(c.Op), c.ID)
		}
	}
	return nil
}
