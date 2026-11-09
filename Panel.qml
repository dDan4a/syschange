pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import qs.Commons
import qs.Ui

Panel {
  id: root
  moduleName: "dan4a.syschange"
  ipcTarget: root.moduleName
  manageIpc: false

  property var anchorItem: null
  property var hostWidget: null
  property var source: null
  readonly property var barIdentity: hostWidget || root
  readonly property color foreground: root.bar ? root.bar.foreground : Color.foreground
  readonly property color dim: Qt.darker(foreground, 1.4)
  readonly property string fontFamily: root.bar ? root.bar.fontFamily : Style.font.family

  readonly property var groups: source ? source.pending : []
  readonly property var history: source ? source.history : []
  property bool searching: false
  readonly property var shownHistory: searching && source && source.query !== "" ? source.found : history
  property int selected: 0
  property string editingKey: ""   // group being described
  property real now: Date.now()    // what ago() counts from: the lists keep their delegates while their data stays the same
  readonly property string keyHelp: "↑↓ select · Enter describe · a AI · A AI all · s keep · o/v diff · / search · "
                                    + "c checkpoint · r refresh, clear error"
  property string armed: ""        // destructive action waiting for a second click: "revert:<id>", "ignore:<group>", "undo:<entry>"

  function open() {
    root.selected = 0
    root.editingKey = ""
    root.controller.show()
  }
  function close() {
    root.editingKey = ""
    if (root.searching)
      root.closeSearch()
    root.controller.hide()
  }
  function toggle() {
    root.opened ? root.close() : root.open()
  }
  function switchPanel(direction) {
    if (root.bar && typeof root.bar.switchPanelFrom === "function")
      return root.bar.switchPanelFrom(root.barIdentity, direction)
    return false
  }

  function current() {
    return groups.length > 0 ? groups[Math.min(selected, groups.length - 1)] : null
  }

  function describe(group, text) {
    if (!group)
      return
    editingKey = group.key
    Qt.callLater(function () {
      var f = noteLoader.item
      if (f) {
        f.text = text || ""
        f.forceActiveFocus()
        f.selectAll()
      }
    })
  }

  // Only the panel that asked (one per monitor) saves the answer, straight away:
  // an answer shown in the shared text field ended up under whatever group was selected by then.
  property string askedKey: ""
  function suggest(group) {
    if (!group || !root.source || root.source.suggestingKey !== "")
      return
    askedKey = group.key
    root.source.suggest(group.key)
  }
  Connections {
    target: root.source
    function onSuggested(key, text) {
      if (key !== root.askedKey)
        return
      root.askedKey = ""
      if (text !== "")
        root.source.note(key, text)
    }
  }
  function cancelDescribe() {
    editingKey = ""
    Qt.callLater(function () {
      if (keyCatcher)
        keyCatcher.forceActiveFocus()
    })
  }
  function commitDescribe() {
    var text = noteLoader.item ? noteLoader.item.text.trim() : ""
    if (source && editingKey === "@checkpoint")
      source.checkpoint(text)
    else if (text !== "" && source)
      source.note(editingKey, text)
    cancelDescribe()
  }
  function openSearch() {
    searching = true
    Qt.callLater(function () {
      var f = searchLoader.item
      if (f) {
        f.forceActiveFocus()
        f.selectAll()
      }
    })
  }
  function closeSearch() {
    searching = false
    searchDelay.stop()
    if (source)
      source.search("")
    Qt.callLater(function () {
      if (keyCatcher)
        keyCatcher.forceActiveFocus()
    })
  }
  function newCheckpoint() {
    editingKey = "@checkpoint"
    Qt.callLater(function () {
      var f = noteLoader.item
      if (f) {
        f.text = ""
        f.forceActiveFocus()
      }
    })
  }

  // Destructive actions take two clicks: the first arms, the second runs.
  function confirm(key, action) {
    if (armed !== key) {
      armed = key
      disarm.restart()
      return
    }
    armed = ""
    action()
  }
  function revert(change) {
    confirm("revert:" + change.id, function () {
      root.source.revert(change.id)
    })
  }
  // No trailing "/": a group can be one top-level file (mimeapps.list).
  function ignoreGroup(group) {
    confirm("ignore:" + group.key, function () {
      root.source.ignore(group.key)
    })
  }
  function toggleDiff(group) {
    if (!root.source)
      return
    root.source.showDiff(root.source.diffKey === group.key ? "" : group.key)
  }

  function restoreCheckpoint(cp) {
    confirm("restore:" + cp.commit, function () {
      root.source.restore(cp.commit)
    })
  }
  function undoEntry(entry) {
    confirm("undo:" + entry.time, function () {
      root.source.undo(entry.time)
    })
  }
  // An Omarchy update changed files you had edited: take its change back out of just those.
  function keepMine(entry) {
    confirm("keep:" + entry.time, function () {
      root.source.undo(entry.time, entry.touched)
    })
  }

  // Who wrote the entry, or else which agents made its changes.
  function heroMeta() {
    if (!root.source)
      return ""
    if (root.source.missing)
      return "Not installed"
    if (!root.source.initialized)
      return "Not tracking yet"
    if (root.source.count === 0)
      return "Everything is recorded"
    return root.source.count + " new" + (root.source.overdue ? ", waiting " + root.source.pendingDays + " days" : "")
  }
  function cliNotice() {
    if (!root.source)
      return ""
    if (!root.source.missing)
      return "syschange " + root.source.version + " is older than this widget expects (" + root.source.cliVersion + ")."
    const install = "This widget needs the syschange command. Install builds it from github.com/dDan4a/syschange (v" + root.source.cliVersion
          + ") into ~/.local/bin with Go. No Go yet? mise fetches one just for the build. Then tracking starts."
    const binary = root.source.binary
    if (binary.indexOf("/") >= 0 && binary.indexOf("/.local/bin/") < 0)
      return install + " Your widget settings point at " + binary + ": clear that setting to use the installed one."
    return install
  }

  // Omarchy's default agent comes first and alone; the other installed agents
  // are offered once it is connected.
  function agentsToConnect() {
    if (!root.source)
      return []
    var all = root.source.agents
    var def = all.filter(function (a) {
      return a["default"] && !a.connected
    })
    return def.length > 0 ? def : all.filter(function (a) {
      return !a.connected
    })
  }
  function agentsCaption() {
    if (!root.source)
      return ""
    var connected = root.source.agents.filter(function (a) {
      return a.connected
    }).map(function (a) {
      return a.title
    })
    var next = agentsToConnect()
    if (next.length === 0)
      return "󰚩 " + connected.join(", ") + (connected.length > 1 ? " describe the changes they make" : " describes the changes it makes")
    if (connected.length > 0)
      return "󰚩 " + connected.join(", ") + " connected. Also:"
    if (next.length === 1)
      return "󰚩 Let " + next[0].title + " describe the system changes it makes"
    return "󰚩 Let your AI agents describe the system changes they make"
  }

  function ago(iso) {
    var s = Math.max(0, (root.now - new Date(iso).getTime()) / 1000)
    if (s < 60)
      return "now"
    if (s < 3600)
      return Math.floor(s / 60) + "m ago"
    if (s < 86400)
      return Math.floor(s / 3600) + "h ago"
    return new Date(iso).toLocaleDateString(Qt.locale(), "d MMM")
  }
  onGroupsChanged: if (selected >= groups.length)
                     selected = Math.max(0, groups.length - 1)

  Timer {
    id: disarm
    interval: 4000
    onTriggered: root.armed = ""
  }
  onOpenedChanged: if (root.opened)
                     root.now = Date.now()
  Timer {
    interval: 60000
    running: root.opened
    repeat: true
    onTriggered: root.now = Date.now()
  }

  KeyboardPanel {
    id: panel
    anchorItem: root.anchorItem
    owner: root.barIdentity
    bar: root.bar
    open: root.opened
    focusTarget: keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(480))
    contentHeight: panel.fittedContentHeight(contentColumn.implicitHeight)

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      blocked: root.editingKey !== "" || (searchLoader.item !== null && searchLoader.item.activeFocus)
      onMoveRequested: function (dx, dy) {
        if (root.groups.length === 0)
          return
        root.selected = Math.max(0, Math.min(root.groups.length - 1, root.selected + dy))
        pendingList.positionViewAtIndex(root.selected, ListView.Contain)
      }
      onActivateRequested: root.describe(root.current())
      onCloseRequested: root.close()
      onTabRequested: function (direction) {
        root.switchPanel(direction)
      }
      onTextKey: function (text) {
        var key = text.toLowerCase(), g = root.current()
        if (!root.source)
          return
        if (text === "A") {
          if (root.source.suggestingKey === "" && root.groups.length > 0)
            root.source.suggestAll()
        } else if (key === "r") {
          root.source.dismissError()
          root.source.refresh()
        } else if (key === "c")
          root.newCheckpoint()
        else if (key === "m")
          root.source.exportMarkdown()
        else if (key === "/")
          root.openSearch()
        else if (!g)
          return
        else if (key === "d")
          root.describe(g)
        else if (key === "s")
          root.source.skip(g.key)
        else if (key === "v")
          root.source.openDiff(g.key)
        else if (key === "o")
          root.toggleDiff(g)
        else if (key === "a")
          root.suggest(g)
      }

      // The panel caps its height on small screens; the content scrolls instead of being cut.
      Flickable {
        id: scroller
        anchors.fill: parent
        contentWidth: width
        contentHeight: contentColumn.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds
        Column {
          id: contentColumn
          width: parent.width
          spacing: Style.space(10)

          PanelHero {
            width: parent.width
            title: "System changes"
            meta: root.heroMeta()
            detail: root.source && root.source.error !== "" ? "Error" : ""
            foreground: root.foreground
            fontFamily: root.fontFamily
            iconComponent: Text {
              text: "󰋚"
              color: root.source && root.source.count > 0 ? Color.accent : root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.display
            }
          }

          Text {
            visible: text !== ""
            width: parent.width
            text: root.source ? root.source.error : ""
            color: Color.urgent
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            wrapMode: Text.Wrap
            textFormat: Text.PlainText
            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: root.source.dismissError()
            }
          }

          Text {
            visible: text !== ""
            width: parent.width
            text: root.source ? root.source.notice : ""
            color: Color.accent
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            wrapMode: Text.Wrap
            textFormat: Text.PlainText
          }

          // No CLI yet: build it from here. An old CLI: offer the update.
          Column {
            visible: root.source !== null && (root.source.missing || root.source.outdated)
            width: parent.width
            spacing: Style.space(8)
            Text {
              width: parent.width
              text: root.cliNotice()
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              wrapMode: Text.Wrap
            }
            Button {
              text: root.source && root.source.missing ? "Install syschange" : "Update syschange"
              bordered: true
              tooltipText: "Opens a terminal and runs go install, then syschange init"
              onClicked: root.source.installCli()
            }
          }

          // First run: one button to start.
          Column {
            visible: root.source !== null && !root.source.missing && !root.source.initialized
            width: parent.width
            spacing: Style.space(8)
            Text {
              width: parent.width
              text: "Keep a history of your config files in ~/.config, installed packages and enabled services. "
                    + "Describe each change in one line, see the diff and undo it."
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              wrapMode: Text.Wrap
            }
            Button {
              text: "Start tracking"
              onClicked: root.source.start()
            }
          }

          Item {
            visible: root.groups.length > 0
            width: parent.width
            height: Math.max(newHeader.implicitHeight, describeAllButton.implicitHeight)
            PanelSectionHeader {
              id: newHeader
              anchors.left: parent.left
              anchors.verticalCenter: parent.verticalCenter
              text: "New"
              foreground: root.foreground
              fontFamily: root.fontFamily
            }
            PanelActionButton {
              id: describeAllButton
              readonly property bool working: root.source !== null && root.source.suggestingKey === "@all"
              anchors.right: parent.right
              anchors.verticalCenter: parent.verticalCenter
              iconText: working ? "󰔟" : "󰁨" // nf-md-timer_sand / nf-md-auto_fix
              tooltipText: working ? "Describing every change with AI…" :
                                     "Describe all with AI (A): your default agent describes each app's changes and saves them"
              foreground: working ? Color.accent : root.foreground
              enabled: root.source !== null && root.source.suggestingKey === ""
              onClicked: root.source.suggestAll()
            }
          }

          ListView {
            id: pendingList
            visible: root.groups.length > 0
            width: parent.width
            height: Math.min(contentHeight, Style.space(360))
            clip: true
            spacing: Style.space(4)
            model: root.groups
            boundsBehavior: Flickable.StopAtBounds

            delegate: GroupDelegate {
              panel: root
            }
          }

          // Inputs are LineEdit, not TextField (which reads the clipboard by itself), and exist only while typing.
          Loader {
            id: noteLoader
            active: root.editingKey !== ""
            visible: active
            width: parent.width
            sourceComponent: LineEdit {
              width: noteLoader.width
              placeholderText: root.editingKey === "@checkpoint" ? "Checkpoint name, for example: before new animations (empty: date)" :
                                                                   "Why? For example: voice typing on Super+R"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onCanceled: root.cancelDescribe()
              onAccepted: root.commitDescribe()
            }
          }

          PanelSeparator {
            width: parent.width
            visible: root.history.length > 0
          }

          Item {
            visible: root.history.length > 0
            width: parent.width
            height: Math.max(historyHeader.implicitHeight, exportButton.implicitHeight)
            PanelSectionHeader {
              id: historyHeader
              anchors.left: parent.left
              anchors.verticalCenter: parent.verticalCenter
              text: "History" + (root.source && root.source.autoExport ? "  ·  kept in " + root.source.autoExport.replace(/^.*\//, "") : "")
              foreground: root.foreground
              fontFamily: root.fontFamily
            }
            PanelActionButton {
              id: searchButton
              anchors.right: backupButton.visible ? backupButton.left : exportButton.left
              anchors.verticalCenter: parent.verticalCenter
              iconText: "󰍉" // nf-md-magnify
              tooltipText: "Search the history (/): descriptions, files, packages and diffs"
              foreground: root.searching ? Color.accent : root.foreground
              onClicked: root.searching ? root.closeSearch() : root.openSearch()
            }
            PanelActionButton {
              id: backupButton
              readonly property var b: root.source ? root.source.backup : null
              visible: b !== null
              anchors.right: exportButton.left
              anchors.verticalCenter: parent.verticalCenter
              iconText: b && b.error ? "󰅤" : "󰅠" // nf-md-cloud_alert / nf-md-cloud_upload
              foreground: b && b.error ? Color.urgent : root.dim
              tooltipText: !b ? "" : b.error ? "Backup to " + b.remote + " failed: " + b.error + ". Click to retry" : new Date(b.time).getFullYear()
                                               < 2000 ? "Backup to " + b.remote + ": not pushed yet. Click to push now" : "Backed up to " + b.remote
                                                        + ", " + root.ago(b.time) + ". Click to push now"
              onClicked: root.source.push()
            }
            PanelActionButton {
              id: exportButton
              anchors.right: parent.right
              anchors.verticalCenter: parent.verticalCenter
              iconText: "󰍔" // nf-md-language_markdown
              tooltipText: "Export to Markdown and open it (m). Keep a file updated: syschange export --auto FILE"
              foreground: root.foreground
              onClicked: root.source.exportMarkdown()
            }
          }

          Loader {
            id: searchLoader
            active: root.searching
            visible: active
            width: parent.width
            sourceComponent: LineEdit {
              width: searchLoader.width
              placeholderText: "Search: waybar, Super+R, docker…"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onTextChanged: searchDelay.restart()
              onCanceled: root.closeSearch()
              onAccepted: keyCatcher.forceActiveFocus()
            }
          }
          Timer {
            id: searchDelay
            interval: 250
            onTriggered: if (root.source && searchLoader.item)
                           root.source.search(searchLoader.item.text.trim())
          }

          Text {
            visible: root.searching && root.source !== null && root.source.query !== "" && root.shownHistory.length === 0
            width: parent.width
            text: "Nothing in the history mentions “" + (root.source ? root.source.query : "") + "”."
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            horizontalAlignment: Text.AlignHCenter
            wrapMode: Text.Wrap
            textFormat: Text.PlainText
          }

          Text {
            visible: root.source !== null && root.source.initialized && root.groups.length === 0 && root.history.length === 0
            width: parent.width
            text: "Nothing has changed since tracking started."
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            horizontalAlignment: Text.AlignHCenter
          }

          ListView {
            visible: root.shownHistory.length > 0
            width: parent.width
            height: Math.min(contentHeight, Style.space(260))
            clip: true
            spacing: Style.space(6)
            model: root.shownHistory.slice(0, 30)
            boundsBehavior: Flickable.StopAtBounds

            delegate: HistoryDelegate {
              panel: root
            }
          }

          PanelSeparator {
            width: parent.width
            visible: root.source !== null && root.source.initialized
          }

          Item {
            visible: root.source !== null && root.source.initialized
            width: parent.width
            height: Math.max(cpHeader.implicitHeight, cpAdd.implicitHeight)
            PanelSectionHeader {
              id: cpHeader
              anchors.left: parent.left
              anchors.verticalCenter: parent.verticalCenter
              text: "Checkpoints"
              foreground: root.foreground
              fontFamily: root.fontFamily
            }
            PanelActionButton {
              id: cpAdd
              anchors.right: parent.right
              anchors.verticalCenter: parent.verticalCenter
              iconText: "󰆓" // nf-md-content_save
              tooltipText: "Save a checkpoint of the whole setup (c)"
              foreground: root.foreground
              onClicked: root.newCheckpoint()
            }
          }

          Text {
            visible: root.source !== null && root.source.initialized && root.source.checkpoints.length === 0
            width: parent.width
            text: "Save one before an experiment, and get back to it in one click."
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            wrapMode: Text.Wrap
          }

          Repeater {
            model: root.source ? root.source.checkpoints.slice(0, 5) : []
            delegate: CheckpointDelegate {
              panel: root
              width: contentColumn.width
            }
          }

          // AI agents: one click gives Claude Code / Codex the skill and the Stop hook.
          Item {
            visible: root.source !== null && root.source.initialized && root.source.agents.length > 0
            width: parent.width
            height: Math.max(agentsText.implicitHeight, connectButton.implicitHeight)
            Text {
              id: agentsText
              anchors.left: parent.left
              anchors.right: connectButton.visible ? connectButton.left : parent.right
              anchors.rightMargin: Style.space(8)
              anchors.verticalCenter: parent.verticalCenter
              text: root.agentsCaption()
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
              wrapMode: Text.Wrap
              textFormat: Text.PlainText
            }
            Button {
              id: connectButton
              visible: root.source !== null && !root.source.agentsConnected
              anchors.right: parent.right
              anchors.verticalCenter: parent.verticalCenter
              text: "Connect " + root.agentsToConnect().map(function (a) {
                return a.title
              }).join(" + ")
              bordered: true
              tooltipText: "Adds the syschange skill and hooks. Undo: syschange agents remove"
              onClicked: root.source.connectAgents(root.agentsToConnect().map(function (a) {
                return a.name
              }))
            }
          }

          Text {
            width: parent.width
            text: root.editingKey !== "" ? "Enter save · Esc cancel" : root.keyHelp
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            horizontalAlignment: Text.AlignHCenter
            wrapMode: Text.Wrap
          }
        }
      }
    }
  }
}
