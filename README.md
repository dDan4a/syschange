# syschange

A history of changes to your Linux setup, with a reason for each one and undo.

You tweak a keybinding, install a package, enable a service. A week later something is off and you
don't remember what you touched, or your AI agent changed it while fixing something else.
syschange notices every change to your setup and asks for a one-line description. You can see the
diff and undo it.

- **Config files in `~/.config`**: kept in a private shadow git repository, so every edit has a diff
  and can be undone.
- **Packages** you installed explicitly (`pacman`, AUR included) and **systemd services** you enabled,
  system and user.
- **Human names**: "Hyprland: Key bindings" instead of `hypr/bindings.lua`.
- **Undo anything**: a change not recorded yet, or a recorded one later on. Undo takes back only that
  entry's lines, so later edits to the same file survive.
- **Quiet on Omarchy**: an `omarchy update`, a theme or a font switch is recorded by itself as one entry
  ("Theme: tokyo-night"), not dumped on you as dozens of changed files.
- **AI agents describe their own changes.** One click gives Claude Code, Codex, Gemini CLI and Grok a skill
  and hooks. When an agent changed your system during a session, it records why before it finishes.
- **Export** to Markdown: the steps to repeat your setup on a new machine, plus the history.
- **Omarchy bar widget**: new changes, history, describe, diff, undo.

Browser and Electron profiles, caches, databases, binaries and anything that looks like a password,
token or key are never copied into the history.

## Install

```bash
go install github.com/dDan4a/syschange@latest      # or: go build -o ~/.local/bin/syschange .
syschange init
```

The Omarchy bar widget:

```bash
omarchy plugin add https://github.com/dDan4a/syschange.git --enable --yes
```

If `syschange` is not on the shell's `PATH` (for example it lives in `~/go/bin`), set the full path in
the widget settings.

## Use

```text
$ syschange status
Hyprland  [hypr]
  ~ Key bindings  +1 -0
Packages  [packages]
  + voxtype

$ syschange note "Voice typing on Super+R" hypr voxtype
Recorded 2 change(s).
```

| Command | What it does |
|---|---|
| `syschange status [--json]` | changes not recorded yet |
| `syschange note [--by NAME] TEXT [ID...]` | record changes with a reason |
| `syschange skip [ID...]` | record changes without a reason |
| `syschange diff [ID...]` | what changed in config files |
| `syschange revert ID...` | undo config changes; prints the `sudo` command for packages and services |
| `syschange ignore PATH` | never track this path |
| `syschange log` | the history |
| `syschange undo ENTRY` | undo a recorded entry (commit from `log`, or its time) |
| `syschange export` | Markdown: setup steps and history |
| `syschange agents [install\|remove]` | connect Claude Code and Codex |

An ID is a file inside `~/.config` (`hypr/bindings.lua`), an app directory (`hypr`), a package or unit name,
or a group (`packages`, `services`). No ID means everything.

In the bar panel: `↑↓` select, `Enter` describe, `s` keep without a description, `v` diff, `r` refresh.
Each line can be undone or ignored; each history entry can be undone. Undo and "stop tracking an app"
ask for a second click.

## Omarchy

On Omarchy, `syschange init` adds `syschange.hook` to `~/.config/omarchy/hooks/{post-update,theme-set,font-set}.d/`.
When Omarchy updates itself or switches a theme or font, the hook records what that command changed
(files modified and packages installed since it started) as one entry by `omarchy`. Your own pending
changes are left alone. Run `syschange init` again to restore the hooks if you deleted them.

## AI agents

```bash
syschange agents install     # or the Connect button in the bar panel
```

| Agent | Skill | Hooks |
|---|---|---|
| Claude Code | `~/.claude/skills` | `SessionStart` + `Stop` in `~/.claude/settings.json` |
| Codex | `~/.agents/skills`, `~/.codex/skills` | `SessionStart` + `Stop` in `~/.codex/hooks.json` |
| Gemini CLI | `~/.agents/skills` | `SessionStart` + `AfterAgent` in `~/.gemini/settings.json` |
| Grok | `~/.grok/skills` | its own file `~/.grok/hooks/syschange.json` |

Only agents whose config directory exists are touched. Edited files keep their key order, and the previous
version is saved as `*.syschange.bak`.

`SessionStart` notes when the session began. The stop hook then looks only at what changed **after** that:
config files modified and packages installed by the agent, not your own pending changes. It asks once per
turn, so an agent is never stuck in a loop. Records made by an agent carry its name in the history.
`syschange agents remove` undoes all of it.

Only want the skill? It also installs with [skills](https://github.com/vercel-labs/skills):
`npx skills add dDan4a/syschange`.

## Where things live

| Path | What |
|---|---|
| `~/.local/state/syschange/config.git` | shadow repository of `~/.config` |
| `~/.local/state/syschange/config.git/info/exclude` | what is never tracked, gitignore syntax |
| `~/.local/state/syschange/journal.jsonl` | the history with descriptions |
| `~/.local/state/syschange/*.txt` | accepted package and service lists |

The state directory is `0700`. Nothing leaves your machine.

## Limits

- `/etc` is not tracked yet.
- Directories that are git repositories of their own (for example plugins cloned into `~/.config`) are
  not tracked file by file.
- Files over 256 KB are skipped.

## Remove

```bash
syschange agents remove                         # if you connected agents
omarchy plugin remove dan4a.syschange           # the bar widget
rm ~/.config/omarchy/hooks/*.d/syschange.hook   # the Omarchy hooks
rm -rf ~/.local/state/syschange                 # the history
rm "$(command -v syschange)"                    # the CLI
```

## License

MIT
