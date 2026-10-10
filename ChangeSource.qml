import QtQuick
import Quickshell
import Quickshell.Io

// Runs the syschange CLI and holds its state. Loaded once as the plugin's
// service, so every monitor's bar shares one poll.
Item {
  id: root

  property string binary: "syschange"
  // The CLI release this plugin is written for. The install button builds
  // exactly this tag, never an unpinned branch.
  readonly property string cliVersion: "0.1.1"
  property bool missing: false      // the CLI is not installed
  property string version: ""       // the installed CLI's version
  readonly property bool outdated: version !== "" && newer(cliVersion, version)
  property int intervalSec: 30
  property string exportPath: ""
  property string autoExport: ""    // file the CLI keeps in sync, if any
  property var backup: null         // {remote, time, error} when a backup remote is set

  property bool initialized: true
  property var pending: []      // groups: {key, title, changes: [...]}
  property var history: []      // entries: {time, note, by, changes: [...]}
  property int count: 0
  property var agents: []       // installed coding agents: {name, connected}
  readonly property bool agentsConnected: agents.length > 0 && agents.every(function(a) { return a.connected })
  // Errors of the last action stay until the next action; a later status poll
  // must not wipe "run this as root" or "undo it by hand".
  property string statusError: ""
  property string actionError: ""
  readonly property string error: actionError !== "" ? actionError : statusError
  property string notice: ""        // commands to run as root after an undo or restore
  property var checkpoints: []
  property string diffKey: ""       // group whose diff is shown inline
  property var diffLines: []        // [{kind: "add"|"del"|"hunk"|"file"|"ctx", text}]
  property string lastStatus: ""
  property bool busy: actionProcess.running
  property string suggestingKey: ""  // group an AI description is being written for

  // A ready AI description for a group; the panel opens it for editing.
  signal suggested(string key, string text)

  property bool refreshAgain: false
  function refresh() {
    // A poll already running may predate the last action: run once more after it.
    if (statusProcess.running) { refreshAgain = true; return }
    // Through sh, so a missing binary is a clear exit 127 instead of a failed spawn.
    statusProcess.command = ["sh", "-c", 'command -v "$0" >/dev/null 2>&1 || exit 127; exec "$0" status --json', binary]
    statusProcess.running = true
  }

  // newer("0.2.0", "0.1.3") === true
  function newer(a, b) {
    var x = a.split("."), y = b.split(".")
    for (var i = 0; i < 3; i++) {
      var d = (parseInt(x[i]) || 0) - (parseInt(y[i]) || 0)
      if (d !== 0) return d > 0
    }
    return false
  }

  // Builds the CLI from source in a terminal: with the user's Go if there is
  // one, else with a Go that mise fetches for this command only. Then starts
  // tracking (or, on an update, refreshes the hooks).
  function installCli() {
    var script = 'set -e; export GOBIN="$HOME/.local/bin"; mod="github.com/dDan4a/syschange@v' + cliVersion + '"; ' +
      'echo "Building $mod into $GOBIN"; ' +
      'if command -v go >/dev/null 2>&1; then go install "$mod"; else mise exec go@latest -- go install "$mod"; fi; ' +
      '"$GOBIN/syschange" init; echo; echo "Done. The bar picks it up in a few seconds."'
    Quickshell.execDetached(["setsid", "uwsm-app", "--", "xdg-terminal-exec", "--app-id=org.omarchy.terminal",
      "--title=Install syschange", "-e", "bash", "-c", script + '; read -rsn1 -p "Press any key to close"'])
    installPoll.restart()
  }

  // Actions run one at a time, in order; quick clicks queue up instead of
  // getting lost. "--" keeps free text (a note starting with "-") from being
  // read as a flag.
  property var queue: []
  function act(args) {
    queue = queue.concat([args])
    if (!actionProcess.running) runNext()
  }
  function runNext() {
    if (queue.length === 0) return
    var args = queue[0]
    queue = queue.slice(1)
    notice = ""
    actionError = ""
    actionProcess.command = [binary, args[0], "--"].concat(args.slice(1))
    actionProcess.running = true
  }

  function start() { act(["init"]) }
  function connectAgents(names) { act(["agents", "install"].concat(names || [])) }
  function note(key, text) { act(["note", text, key]) }
  function skip(key) { act(["skip", key]) }
  function revert(id) { act(["revert", id]) }
  function ignore(path) { act(["ignore", path]) }
  function undo(entryTime) { act(["undo", entryTime]) }
  // Exports to the file kept in sync if there is one, else to the configured
  // path, then opens it.
  function exportMarkdown() {
    var path = autoExport || exportPath
    if (path === "" || exportProcess.running) return
    exportProcess.path = path
    exportProcess.command = [binary, "export", "--to", path]
    exportProcess.running = true
  }

  function push() { act(["push"]) }

  function checkpoint(name) { act(["checkpoint", name]) }
  function restore(commit) { act(["restore", commit]) }

  function suggest(key) {
    if (suggestProcess.running) return
    suggestingKey = key
    suggestProcess.command = [binary, "suggest", key]
    suggestProcess.running = true
  }

  function showDiff(key) {
    diffKey = key
    diffLines = []
    if (key === "") return
    diffProcess.running = false
    diffProcess.command = [binary, "diff", key]
    diffProcess.running = true
  }

  // Unified diff -> short, typed lines for the panel.
  function parseDiff(text) {
    var out = []
    var firstHunk = true
    var lines = String(text || "").split("\n")
    for (var i = 0; i < lines.length; i++) {
      var l = lines[i]
      if (l.indexOf("diff --git ") === 0) {
        var m = / b\/(.*)$/.exec(l)
        out.push({ kind: "file", text: m ? m[1] : l })
        firstHunk = true
      } else if (/^(index |--- |\+\+\+ |new file|deleted file|old mode|new mode|similarity)/.test(l)) {
        continue
      } else if (l.indexOf("@@") === 0) {
        if (!firstHunk) out.push({ kind: "hunk", text: "⋯" }) // a gap between hunks of one file
        firstHunk = false
      } else if (l[0] === "+") {
        out.push({ kind: "add", text: l })
      } else if (l[0] === "-") {
        out.push({ kind: "del", text: l })
      } else if (l !== "") {
        out.push({ kind: "ctx", text: l })
      }
    }
    return out
  }

  function openDiff(key) {
    Quickshell.execDetached(["setsid", "uwsm-app", "--", "xdg-terminal-exec", "--app-id=org.omarchy.terminal",
      "--title=System changes", "-e", "sh", "-c", '"$0" diff --color "$1" | less -R', binary, key])
  }

  function message(text) {
    var msg = String(text || "").trim().replace(/^syschange: /, "")
    if (msg === "") msg = "syschange failed"
    return msg.length > 400 ? msg.slice(0, 397) + "..." : msg
  }
  function failure(text) { actionError = message(text) }

  Component.onCompleted: refresh()

  IpcHandler {
    target: "dan4a.syschange"
    function refresh(): void { root.refresh() }
  }

  // While an install runs in the terminal, look for the binary more often.
  Timer {
    id: installPoll
    interval: 3000
    repeat: true
    property int ticks: 0
    onRunningChanged: if (running) ticks = 0
    onTriggered: {
      root.refresh()
      if (++ticks > 100 || (!root.missing && !root.outdated && root.initialized)) stop()
    }
  }

  Timer {
    interval: Math.max(10, root.intervalSec) * 1000
    running: true
    repeat: true
    onTriggered: root.refresh()
  }

  Process {
    id: statusProcess
    stdout: StdioCollector { id: statusOut; waitForEnd: true }
    stderr: StdioCollector { id: statusErr; waitForEnd: true }
    onExited: function(exitCode) {
      root.missing = exitCode === 127
      if (root.refreshAgain) { root.refreshAgain = false; Qt.callLater(root.refresh) }
      if (root.missing) { root.statusError = ""; root.initialized = false; return }
      if (exitCode !== 0) { root.statusError = root.message(statusErr.text || statusOut.text); return }
      try {
        var changed = statusOut.text !== root.lastStatus
        root.lastStatus = statusOut.text
        var s = JSON.parse(statusOut.text)
        root.initialized = s.initialized
        root.pending = s.pending || []
        root.history = s.history || []
        root.count = s.count || 0
        root.agents = s.agents || []
        root.checkpoints = s.checkpoints || []
        root.autoExport = s.autoExport || ""
        root.version = s.version || "0.0.0"
        root.backup = s.backup || null
        // The shown diff follows the files: reload it when anything changed.
        if (changed && root.diffKey !== "") {
          var still = root.pending.some(function(g) { return g.key === root.diffKey })
          root.showDiff(still ? root.diffKey : "")
        }
        root.statusError = ""
      } catch (e) {
        root.failure("unexpected output from syschange status")
      }
    }
  }

  Process {
    id: exportProcess
    property string path: ""
    stderr: StdioCollector { id: exportErr; waitForEnd: true }
    onExited: function(exitCode) {
      if (exitCode !== 0) { root.failure(exportErr.text); return }
      root.notice = "Exported to " + path
      Qt.openUrlExternally("file://" + path)
    }
  }

  Process {
    id: diffProcess
    stdout: StdioCollector { id: diffOut; waitForEnd: true }
    onExited: function(exitCode) {
      root.diffLines = exitCode === 0 ? root.parseDiff(diffOut.text) : [{ kind: "del", text: "Could not load the diff; press v to see it in a terminal" }]
    }
  }

  Process {
    id: suggestProcess
    stdout: StdioCollector { id: suggestOut; waitForEnd: true }
    stderr: StdioCollector { id: suggestErr; waitForEnd: true }
    onExited: function(exitCode) {
      var key = root.suggestingKey
      root.suggestingKey = ""
      if (exitCode !== 0) { root.failure(suggestErr.text || suggestOut.text); return }
      root.suggested(key, String(suggestOut.text || "").trim())
    }
  }

  Process {
    id: actionProcess
    stdout: StdioCollector { id: actionOut; waitForEnd: true }
    stderr: StdioCollector { id: actionErr; waitForEnd: true }
    onExited: function(exitCode) {
      // Reverting a package prints the command to run as root: show it.
      if (exitCode !== 0) root.failure(actionErr.text || actionOut.text)
      else {
        var out = String(actionOut.text || "").trim()
        if (/sudo |systemctl /.test(out)) root.notice = out
      }
      root.refresh()
      root.runNext()
    }
  }
}
