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

// version is the CLI release; the Omarchy plugin asks for at least its own.
const version = "0.1.1"

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
  syschange diff [--color] [ID...]     show what changed in config files
  syschange revert ID...               undo config changes
  syschange ignore PATH                never track PATH (a gitignore pattern)
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
  syschange export [--to FILE]         history and setup steps as Markdown
  syschange export --auto FILE|off     keep FILE updated after every record
  syschange agents [install [NAME...]|remove]
                                       connect Claude Code, Codex, Gemini CLI, Grok: skill + hooks
  syschange hook AGENT [start]         the hooks themselves (read stdin)
  syschange version                    print the version

ID is a file path inside ~/.config (hypr/bindings.conf), an app directory (hypr),
a package or unit name, or a group: packages, services. No ID means all changes.
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
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	color := fs.Bool("color", false, "colored diff even when not writing to a terminal")
	yes := fs.Bool("yes", false, "apply: do it without asking")
	quiet := fs.Bool("quiet", false, "push: print nothing")
	exportTo := fs.String("to", "", "export: write to this file instead of stdout")
	autoExport := fs.String("auto", "", "export: keep this file updated after every record; off to stop")
	grep := fs.String("grep", "", "log: only entries that mention this text")
	agentName := fs.String("agent", "", "agent for suggest; default: Omarchy's default agent")
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
	case "checkpoint":
		cp, err := s.Checkpoint(strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Saved checkpoint %q.\n", cp.Name)
		return nil
	case "checkpoints":
		all, err := s.Checkpoints()
		if err != nil {
			return err
		}
		for _, cp := range all {
			fmt.Fprintf(stdout, "%s  %s\n", cp.Time.Format("2006-01-02 15:04"), cp.Name)
		}
		return nil
	case "restore":
		if len(args) == 0 {
			return errors.New("restore takes a checkpoint name, see syschange checkpoints")
		}
		hint, err := s.Restore(strings.Join(args, " "), *by)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Config files restored. The state before is saved as a checkpoint too.")
		if hint != "" {
			fmt.Fprintln(stdout, hint)
		}
		return nil
	case "suggest":
		line, err := s.Suggest(args, *agentName)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, line)
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
		return printLog(s, stdout, *grep, *asJSON)
	case "apply":
		if len(args) != 1 {
			return errors.New("usage: syschange apply [--yes] URL-or-path-of-a-backup")
		}
		plan, err := s.PlanApply(args[0])
		if err != nil {
			return err
		}
		defer plan.Cleanup()
		plan.Print(stdout)
		if plan.Empty() {
			return nil
		}
		if !*yes {
			if !isTerminal(stdout) {
				return errors.New("run with --yes to apply")
			}
			fmt.Fprint(stdout, "\nApply? A checkpoint of the current state is saved first. [y/N] ")
			var answer string
			fmt.Fscanln(stdin, &answer)
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				return errors.New("cancelled")
			}
		}
		if err := s.Apply(plan, runAttached, *by); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Applied. Undo the config part with: syschange restore \"Before applying "+args[0]+"\"")
		return nil
	case "remote":
		switch {
		case len(args) == 2 && args[0] == "set":
			if err := s.SetRemote(args[1]); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Backed up to %s. Every change is pushed from now on.\nKeep that repository private: it holds your config files.\n", args[1])
			return nil
		case len(args) == 1 && args[0] == "off":
			if err := s.SetRemote(""); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "Backup is off. The remote keeps what it has.")
			return nil
		case len(args) == 0:
			if url := s.RemoteURL(); url != "" {
				fmt.Fprintln(stdout, url)
				if st := s.LastPush(); st != nil {
					state := "ok"
					if st.Error != "" {
						state = st.Error
					}
					fmt.Fprintf(stdout, "last push %s: %s\n", st.Time.Format("2006-01-02 15:04"), state)
				}
			} else {
				fmt.Fprintln(stdout, "No backup remote. Set one: syschange remote set URL")
			}
			return nil
		}
		return errors.New("usage: syschange remote [set URL | off]")
	case "push":
		if err := s.Push(); err != nil {
			return err
		}
		if !*quiet {
			fmt.Fprintf(stdout, "Pushed to %s.\n", s.RemoteURL())
		}
		return nil
	case "export":
		switch {
		case *autoExport == "off":
			if err := s.SetAutoExport(""); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "Auto export is off.")
			return nil
		case *autoExport != "":
			if err := s.SetAutoExport(expandHome(*autoExport)); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Exported to %s, and it will be kept up to date.\n", s.autoExportPath())
			return nil
		case *exportTo != "":
			path := expandHome(*exportTo)
			if err := s.ExportTo(path); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Exported to %s.\n", path)
			return nil
		}
		return s.Export(stdout)
	case "undo":
		if len(args) == 0 {
			return errors.New("undo takes a history entry: its time (see status --json) or commit")
		}
		e, hint, err := s.Undo(args[0], *by, args[1:]...)
		if err != nil {
			return err
		}
		if len(e.Changes) > 0 {
			fmt.Fprintf(stdout, "Undid %d config change(s).\n", len(e.Changes))
		}
		if hint != "" {
			fmt.Fprintln(stdout, hint)
		}
		return nil
	case "hook":
		if len(args) >= 2 && args[0] == "omarchy" {
			return s.OmarchyHook(args[1], args[2:])
		}
		if len(args) == 0 || !slices.Contains([]string{"claude", "codex", "gemini", "grok"}, args[0]) {
			return errors.New("usage: syschange hook claude|codex|gemini|grok [start]")
		}
		if len(args) > 1 {
			switch args[1] {
			case "start":
				return s.SessionStartHook(args[0], stdin)
			case "pre", "post":
				return s.ToolHook(args[0], args[1] == "pre", stdin)
			}
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

type statusJSON struct {
	Initialized bool         `json:"initialized"`
	Count       int          `json:"count"`
	Pending     []Group      `json:"pending"`
	History     []Entry      `json:"history"`
	ConfigDir   string       `json:"configDir"`
	Agents      []AgentState `json:"agents"`
	Checkpoints []Checkpoint `json:"checkpoints"`
	AutoExport  string       `json:"autoExport"` // file kept in sync, if any
	Version     string       `json:"version"`
	Backup      *PushStatus  `json:"backup,omitempty"`
}

func status(s *Store, w io.Writer, asJSON bool) error {
	out := statusJSON{Initialized: s.Initialized(), ConfigDir: s.ConfigDir, Pending: []Group{}, History: []Entry{}, Agents: []AgentState{}, Checkpoints: []Checkpoint{}, Version: version}
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
		s.Attribute(pending)
		out.Count = len(pending)
		out.Pending = Groups(pending)
		history, err := s.History()
		if err != nil {
			return err
		}
		out.History = history[:min(len(history), 50)]
		out.AutoExport = s.autoExportPath()
		if url := s.RemoteURL(); url != "" {
			out.Backup = &PushStatus{Remote: url}
			if st := s.LastPush(); st != nil && st.Remote == url {
				out.Backup = st
			}
		}
		checkpoints, err := s.Checkpoints()
		if err != nil {
			return err
		}
		for _, cp := range checkpoints[:min(len(checkpoints), 10)] {
			cp.Packages, cp.Units = nil, nil // the panel needs names, not lists
			out.Checkpoints = append(out.Checkpoints, cp)
		}
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
			by := ""
			if c.By != "" {
				by = "  [" + c.By + "]"
			}
			fmt.Fprintf(w, "  %s %s%s%s\n", opSign(c.Op), c.Label, lineStat(c), by)
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
		done, err := g.Install(args[1:]...)
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
			def := ""
			if st.Default {
				def = " (Omarchy default)"
			}
			fmt.Fprintf(w, "%s%s: %s\n", st.Name, def, state)
		}
		return nil
	}
	return fmt.Errorf("unknown agents action %q: use install, remove or status", action)
}

func printLog(s *Store, w io.Writer, grep string, asJSON bool) error {
	history, err := s.Search(grep)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(w).Encode(append([]Entry{}, history...))
	}
	for _, e := range history {
		note := e.Note
		if note == "" {
			note = "(no description)"
		}
		if e.By != "" {
			note += "  [" + e.By + "]"
		}
		if e.Undone {
			note += "  (undone)"
		}
		fmt.Fprintf(w, "%s  %s  %s\n", e.Time.Format("2006-01-02 15:04"), e.Commit, note)
		if len(e.Touched) > 0 {
			fmt.Fprintf(w, "    ! changed files you had edited: %s (keep yours: syschange undo %s %s)\n",
				strings.Join(e.Touched, " "), e.Commit, strings.Join(e.Touched, " "))
		}
		for _, c := range e.Changes {
			fmt.Fprintf(w, "    %s %s\n", opSign(c.Op), c.ID)
		}
	}
	return nil
}
