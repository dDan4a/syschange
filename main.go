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
	"time"

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

var commands = map[string]func(*invocation) error{
	"init":        cmdInit,
	"status":      cmdStatus, //nolint:goconst // subcommand name, like every other key here
	"note":        cmdNote,
	"skip":        cmdSkip,
	"checkpoint":  cmdCheckpoint,
	"checkpoints": cmdCheckpoints,
	"restore":     cmdRestore,
	"suggest":     cmdSuggest,
	"diff":        cmdDiff,
	"revert":      cmdRevert,
	"ignore":      cmdIgnore,
	"track":       cmdTrack,
	"untrack":     cmdUntrack,
	"digest":      cmdDigest,
	"notify":      cmdNotify,
	"log":         cmdLog,
	"apply":       cmdApply,
	"remote":      cmdRemote,
	"push":        cmdPush,
	"export":      cmdExport,
	"undo":        cmdUndo,
	"hook":        cmdHook,
	"agents":      cmdAgents,
}

func cmdStatus(c *invocation) error { return status(c.s, c.stdout, c.asJSON) }

func cmdDiff(c *invocation) error { return c.s.Diff(c.stdout, c.args, c.color || isTerminal(c.stdout)) }

func cmdLog(c *invocation) error { return printLog(c.s, c.stdout, c.grep, c.asJSON) }

func cmdAgents(c *invocation) error { return agentsCmd(c.args, c.stdout) }

func cmdInit(c *invocation) error {
	if err := c.s.Init(); err != nil {
		return err
	}

	fmt.Fprintf(c.stdout, "Tracking %s. History lives in %s.\n", c.s.ConfigDir, c.s.StateDir)

	return nil
}

func cmdNote(c *invocation) error {
	if len(c.args) == 0 || strings.TrimSpace(c.args[0]) == "" {
		return errors.New("note needs a text: syschange note \"why\" [ID...]")
	}

	e, err := c.s.Record(c.args[1:], c.args[0], c.by)
	if err != nil {
		return err
	}

	fmt.Fprintf(c.stdout, "Recorded %d change(s).\n", len(e.Changes))

	return nil
}

func cmdSkip(c *invocation) error {
	e, err := c.s.Record(c.args, "", c.by)
	if err != nil {
		return err
	}

	fmt.Fprintf(c.stdout, "Recorded %d change(s) without a description.\n", len(e.Changes))

	return nil
}

func cmdCheckpoint(c *invocation) error {
	cp, err := c.s.Checkpoint(strings.Join(c.args, " "))
	if err != nil {
		return err
	}

	fmt.Fprintf(c.stdout, "Saved checkpoint %q.\n", cp.Name)

	return nil
}

func cmdCheckpoints(c *invocation) error {
	all, err := c.s.Checkpoints()
	if err != nil {
		return err
	}

	for _, cp := range all {
		fmt.Fprintf(c.stdout, "%s  %s\n", cp.Time.Format("2006-01-02 15:04"), cp.Name)
	}

	return nil
}

func cmdRestore(c *invocation) error {
	if len(c.args) == 0 {
		return errors.New("restore takes a checkpoint name, see syschange checkpoints")
	}

	hint, err := c.s.Restore(strings.Join(c.args, " "), c.by)
	if err != nil {
		return err
	}

	fmt.Fprintln(c.stdout, "Config files restored. The state before is saved as a checkpoint too.")

	if hint != "" {
		fmt.Fprintln(c.stdout, hint)
	}

	return nil
}

func cmdSuggest(c *invocation) error {
	if c.all {
		return suggestAll(c.s, c.stdout, c.agent, c.send)
	}

	line, err := c.s.Suggest(c.args, c.agent)
	if err != nil {
		return err
	}

	fmt.Fprintln(c.stdout, line)

	return nil
}

func cmdRevert(c *invocation) error {
	if len(c.args) == 0 {
		return errors.New("revert needs at least one ID")
	}

	return c.s.Revert(c.args)
}

func cmdIgnore(c *invocation) error {
	if len(c.args) != 1 {
		return errors.New("ignore takes one path")
	}

	return c.s.Ignore(c.args[0])
}

func cmdTrack(c *invocation) error {
	if len(c.args) > 0 {
		return c.s.Track(c.args)
	}

	names, err := c.s.trackedFiles()
	for _, n := range names {
		fmt.Fprintln(c.stdout, n)
	}

	return err
}

func cmdUntrack(c *invocation) error {
	if len(c.args) == 0 {
		return errors.New("untrack needs at least one file")
	}

	return c.s.Untrack(c.args)
}

func cmdDigest(c *invocation) error {
	history, err := c.s.History()
	if err != nil {
		return err
	}

	now := time.Now()

	text := Digest(history, now.AddDate(0, 0, -c.days), now)
	if text == "" {
		text = fmt.Sprintf("Nothing recorded in the last %d days.", c.days)
	}

	fmt.Fprintln(c.stdout, text)

	return nil
}

func cmdNotify(c *invocation) error {
	if !c.s.Initialized() {
		return ErrNotInitialized
	}

	due, err := c.s.Notify(time.Now(), c.remindDays, !c.noDigest)
	if err != nil {
		return err
	}

	if !c.send {
		return json.NewEncoder(c.stdout).Encode(append([]Notification{}, due...))
	}

	for _, n := range due {
		if err := sendNotification(n); err != nil {
			return fmt.Errorf("notification: %w", err)
		}
	}

	return nil
}

func cmdApply(c *invocation) error {
	if len(c.args) != 1 {
		return errors.New("usage: syschange apply [--yes] URL-or-path-of-a-backup")
	}

	plan, err := c.s.PlanApply(c.args[0])
	if err != nil {
		return err
	}
	defer plan.Cleanup()

	plan.Print(c.stdout)

	if plan.Empty() {
		return nil
	}

	if !c.yes {
		if err := confirmApply(c.stdin, c.stdout); err != nil {
			return err
		}
	}

	if err := c.s.Apply(plan, runAttached, c.by); err != nil {
		return err
	}

	fmt.Fprintln(c.stdout, "Applied. Undo the config part with: syschange restore \"Before applying "+c.args[0]+"\"")

	return nil
}

func confirmApply(stdin io.Reader, stdout io.Writer) error {
	if !isTerminal(stdout) {
		return errors.New("run with --yes to apply")
	}

	fmt.Fprint(stdout, "\nApply? A checkpoint of the current state is saved first. [y/N] ")

	var answer string

	_, _ = fmt.Fscanln(stdin, &answer)

	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return errors.New("cancelled")
	}

	return nil
}

func cmdRemote(c *invocation) error {
	switch {
	case len(c.args) == 2 && c.args[0] == "set":
		if err := c.s.SetRemote(c.args[1]); err != nil {
			return err
		}

		fmt.Fprintf(c.stdout, "Backed up to %s. Every change is pushed from now on.\n", c.args[1])
		fmt.Fprintln(c.stdout, "Keep that repository private: it holds your config files.")

		return nil
	case len(c.args) == 1 && c.args[0] == "off":
		if err := c.s.SetRemote(""); err != nil {
			return err
		}

		fmt.Fprintln(c.stdout, "Backup is off. The remote keeps what it has.")

		return nil
	case len(c.args) == 0:
		printRemote(c.s, c.stdout)

		return nil
	}

	return errors.New("usage: syschange remote [set URL | off]")
}

func printRemote(s *Store, w io.Writer) {
	url := s.RemoteURL()
	if url == "" {
		fmt.Fprintln(w, "No backup remote. Set one: syschange remote set URL")
		return
	}

	fmt.Fprintln(w, url)

	if st := s.LastPush(); st != nil {
		state := "ok"
		if st.Error != "" {
			state = st.Error
		}

		fmt.Fprintf(w, "last push %s: %s\n", st.Time.Format("2006-01-02 15:04"), state)
	}
}

func cmdPush(c *invocation) error {
	if err := c.s.Push(); err != nil {
		return err
	}

	if !c.quiet {
		fmt.Fprintf(c.stdout, "Pushed to %s.\n", c.s.RemoteURL())
	}

	return nil
}

func cmdExport(c *invocation) error {
	switch {
	case c.autoExport == "off":
		if err := c.s.SetAutoExport(""); err != nil {
			return err
		}

		fmt.Fprintln(c.stdout, "Auto export is off.")

		return nil
	case c.autoExport != "":
		if err := c.s.SetAutoExport(expandHome(c.autoExport)); err != nil {
			return err
		}

		fmt.Fprintf(c.stdout, "Exported to %s, and it will be kept up to date.\n", c.s.autoExportPath())

		return nil
	case c.exportTo != "":
		path := expandHome(c.exportTo)
		if err := c.s.ExportTo(path); err != nil {
			return err
		}

		fmt.Fprintf(c.stdout, "Exported to %s.\n", path)

		return nil
	}

	return c.s.Export(c.stdout)
}

func cmdUndo(c *invocation) error {
	if len(c.args) == 0 {
		return errors.New("undo takes a history entry: its time (see status --json) or commit")
	}

	e, hint, err := c.s.Undo(c.args[0], c.by, c.args[1:]...)
	if err != nil {
		return err
	}

	if len(e.Changes) > 0 {
		fmt.Fprintf(c.stdout, "Undid %d config change(s).\n", len(e.Changes))
	}

	if hint != "" {
		fmt.Fprintln(c.stdout, hint)
	}

	return nil
}

func cmdHook(c *invocation) error {
	args := c.args
	if len(args) >= 2 && args[0] == "omarchy" { //nolint:goconst // subcommand name, not the author or the config directory
		return c.s.OmarchyHook(args[1], args[2:])
	}

	if len(args) == 0 || !slices.Contains([]string{agentClaude, agentCodex, agentGemini, agentGrok}, args[0]) {
		return errors.New("usage: syschange hook claude|codex|gemini|grok [start]")
	}

	if len(args) > 1 {
		switch args[1] {
		case "start":
			return c.s.SessionStartHook(args[0], c.stdin)
		case "pre", "post":
			return c.s.ToolHook(args[0], args[1] == "pre", c.stdin)
		}
	}

	return c.s.StopHook(args[0], c.stdin, c.stdout)
}

func suggestAll(s *Store, w io.Writer, agent string, send bool) error {
	var lines strings.Builder

	described, total, err := s.SuggestAll(agent, io.MultiWriter(w, &lines))
	if err != nil {
		return err
	}

	if total == 0 {
		return errors.New("nothing to describe")
	}

	summary := fmt.Sprintf("Described %d of %d", described, total)
	fmt.Fprintln(w, summary+".")

	if send {
		if err := sendNotification(Notification{Summary: summary, Body: strings.TrimSpace(lines.String())}); err != nil {
			return err
		}
	}

	if described == 0 {
		return fmt.Errorf("no change could be described:\n%s", strings.TrimSpace(lines.String()))
	}

	return nil
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

//nolint:tagliatelle // camelCase keys are the published format the QML side reads
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
	// OldestPending is when the oldest unrecorded change was made, if known.
	OldestPending *time.Time `json:"oldestPending,omitempty"`
}

func status(s *Store, w io.Writer, asJSON bool) error {
	out := statusJSON{
		Initialized: s.Initialized(), ConfigDir: s.ConfigDir, Version: version,
		Pending: []Group{}, History: []Entry{}, Agents: []AgentState{}, Checkpoints: []Checkpoint{},
	}

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
		if t := s.OldestPending(pending); !t.IsZero() {
			out.OldestPending = &t
		}

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
	case opAdded:
		return "+"
	case opRemoved:
		return "-"
	}

	return "~"
}

func lineStat(c Change) string {
	if c.Kind != kindConfig || (c.Added == 0 && c.Removed == 0) {
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
