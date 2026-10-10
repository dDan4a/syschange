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
  function connectAgents() { act(["agents", "install"]) }
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
        var s = JSON.parse(statusOut.text)
        root.initialized = s.initialized
        root.pending = s.pending || []
        root.history = s.history || []
        root.count = s.count || 0
        root.agents = s.agents || []
        root.checkpoints = s.checkpoints || []
        root.error = ""
      } catch (e) {
        root.failure("unexpected output from syschange status")
      }
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
