import QtQuick
import Quickshell
import Quickshell.Io

// Runs the syschange CLI and holds its state. Loaded once as the plugin's
// service, so every monitor's bar shares one poll.
Item {
  id: root

  property string binary: "syschange"
  property int intervalSec: 30

  property bool initialized: true
  property var pending: []      // groups: {key, title, changes: [...]}
  property var history: []      // entries: {time, note, by, changes: [...]}
  property int count: 0
  property var agents: []       // installed coding agents: {name, connected}
  readonly property bool agentsConnected: agents.length > 0 && agents.every(function(a) { return a.connected })
  property string error: ""
  property string notice: ""        // commands to run as root after an undo or restore
  property var checkpoints: []
  property string diffKey: ""       // group whose diff is shown inline
  property var diffLines: []        // [{kind: "add"|"del"|"hunk"|"file"|"ctx", text}]
  property string lastStatus: ""
  property bool busy: actionProcess.running
  property string suggestingKey: ""  // group an AI description is being written for

  // A ready AI description for a group; the panel opens it for editing.
  signal suggested(string key, string text)

  function refresh() {
    if (statusProcess.running) return
    statusProcess.command = [binary, "status", "--json"]
    statusProcess.running = true
  }

  function act(args) {
    if (actionProcess.running) return
    notice = ""
    actionProcess.command = [binary].concat(args)
    actionProcess.running = true
  }

  function start() { act(["init"]) }
  function connectAgents(names) { act(["agents", "install"].concat(names || [])) }
  function note(key, text) { act(["note", text, key]) }
  function skip(key) { act(["skip", key]) }
  function revert(id) { act(["revert", id]) }
  function ignore(path) { act(["ignore", path]) }
  function undo(entryTime) { act(["undo", entryTime]) }
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

  function failure(text) {
    var msg = String(text || "").trim().replace(/^syschange: /, "")
    if (msg === "") msg = "syschange failed"
    if (/not found|No such file/.test(msg)) msg = "syschange is not installed: go install github.com/dDan4a/syschange@latest"
    error = msg.length > 400 ? msg.slice(0, 397) + "..." : msg
  }

  Component.onCompleted: refresh()

  IpcHandler {
    target: "dan4a.syschange"
    function refresh(): void { root.refresh() }
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
      if (exitCode !== 0) { root.failure(statusErr.text || statusOut.text); return }
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
        // The shown diff follows the files: reload it when anything changed.
        if (changed && root.diffKey !== "") {
          var still = root.pending.some(function(g) { return g.key === root.diffKey })
          root.showDiff(still ? root.diffKey : "")
        }
        root.error = ""
      } catch (e) {
        root.failure("unexpected output from syschange status")
      }
    }
  }

  Process {
    id: diffProcess
    stdout: StdioCollector { id: diffOut; waitForEnd: true }
    onExited: function(exitCode) {
      if (exitCode === 0) root.diffLines = root.parseDiff(diffOut.text)
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
      root.error = ""
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
        root.error = ""
        var out = String(actionOut.text || "").trim()
        if (/sudo |systemctl /.test(out)) root.notice = out
      }
      root.refresh()
    }
  }
}
