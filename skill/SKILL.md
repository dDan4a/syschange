---
name: syschange
description: Record why the system setup changed, with the syschange CLI. Use after ANY change to the user's system rather than to project code — editing files in ~/.config (Hyprland, Omarchy, terminals, editors, apps), installing or removing packages, enabling or disabling systemd services, adding shell plugins, themes or keybindings — and when the user asks what changed on the system, why, or to undo a system change. Not for project source code.
---

# Recording system changes with syschange

`syschange` keeps a history of the user's setup: config files in `~/.config`, explicitly
installed packages and enabled services. Every change waits in the bar as "new" until
someone describes it in one line. Your job is to describe the changes **you** made, so the
user sees why their system changed, not just that it did.

## After you change the system

1. Finish the change and check that it works. Record the final state, not attempts.
2. See what is pending:
   ```bash
   syschange status
   ```
   Each group is an app (`hypr`, `omarchy`, ...), `packages` or `services`; each line is
   one change with its ID in the JSON output (`syschange status --json`).
3. Record your changes, one call per logical change, with a short reason in the
   user's language:
   ```bash
   syschange note --by <agent> "Voice typing on Super+R" voxtype hypr/bindings.lua
   ```
   IDs can be a file path inside `~/.config`, an app directory (`hypr`), a package
   (`voxtype`), a unit (`unit:user voxtype.service`) or a whole group (`packages`).
4. **Leave out changes you did not make.** They belong to the user; they will describe
   them from the bar.

Write the reason, not the diff: "Fix Bluetooth headset switching to HSP", not
"edit wireplumber.conf". Never put secrets into a note.

## When the user asks

| Request | Command |
|---|---|
| What changed recently, and why | `syschange log` |
| What exactly changed in a file | `syschange diff hypr/bindings.lua` |
| Undo a config change that is not recorded yet | `syschange revert <ID>` |
| Undo a recorded change | find the commit in `syschange log`, show the diff, ask, then edit the file back |
| Stop tracking noisy app state | `syschange ignore <path>` |
| Steps to repeat this setup on a new machine | `syschange export` |

`revert` cannot undo packages or services because they need root: it prints the
command to run instead. Ask before reverting anything.

If `syschange status` says it is not initialized, tell the user and offer
`syschange init`; do not run it on your own.
