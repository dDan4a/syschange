import QtQuick
import Quickshell
import qs.Commons
import qs.Ui

Panel {
  id: root
  moduleName: "dan4a.syschange"
  ipcTarget: moduleName
  manageIpc: false

  property var anchorItem: null
  property var hostWidget: null
  property var source: null
  readonly property var barIdentity: hostWidget || root
  readonly property color foreground: bar ? bar.foreground : Color.foreground
  readonly property color dim: Qt.darker(foreground, 1.4)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family

  readonly property var groups: source ? source.pending : []
  readonly property var history: source ? source.history : []
  property bool searching: false
  readonly property var shownHistory: searching && source && source.query !== "" ? source.found : history
  property int selected: 0
  property string editingKey: ""   // group being described
  property string armed: ""        // destructive action waiting for a second click: "revert:<id>", "ignore:<group>", "undo:<entry>"

  function open() { root.selected = 0; root.editingKey = ""; root.controller.show() }
  function close() { root.editingKey = ""; if (root.searching) root.closeSearch(); root.controller.hide() }
  function toggle() { root.opened ? root.close() : root.open() }
  function switchPanel(direction) {
    if (root.bar && typeof root.bar.switchPanelFrom === "function")
      return root.bar.switchPanelFrom(root.barIdentity, direction)
    return false
  }

  function current() { return groups.length > 0 ? groups[Math.min(selected, groups.length - 1)] : null }

  function describe(group, text) {
    if (!group) return
    editingKey = group.key
    Qt.callLater(function() { var f = noteLoader.item; if (f) { f.text = text || ""; f.forceActiveFocus(); f.selectAll() } })
  }

  // Only the panel that asked (one per monitor) saves the answer, straight away:
  // an answer shown in the shared text field ended up under whatever group was selected by then.
  property string askedKey: ""
  function suggest(group) {
    if (!group || !root.source || root.source.suggestingKey !== "") return
    askedKey = group.key
    root.source.suggest(group.key)
  }
  Connections {
    target: root.source
    function onSuggested(key, text) {
      if (key !== root.askedKey) return
      root.askedKey = ""
      if (text !== "") root.source.note(key, text)
    }
  }
  function cancelDescribe() {
    editingKey = ""
    Qt.callLater(function() { if (keyCatcher) keyCatcher.forceActiveFocus() })
  }
  function commitDescribe() {
    var text = noteLoader.item ? noteLoader.item.text.trim() : ""
    if (source && editingKey === "@checkpoint") source.checkpoint(text)
    else if (text !== "" && source) source.note(editingKey, text)
    cancelDescribe()
  }
  function openSearch() {
    searching = true
    Qt.callLater(function() { var f = searchLoader.item; if (f) { f.forceActiveFocus(); f.selectAll() } })
  }
  function closeSearch() {
    searching = false
    searchDelay.stop()
    if (source) source.search("")
    Qt.callLater(function() { if (keyCatcher) keyCatcher.forceActiveFocus() })
  }
  function newCheckpoint() {
    editingKey = "@checkpoint"
    Qt.callLater(function() { var f = noteLoader.item; if (f) { f.text = ""; f.forceActiveFocus() } })
  }

  // Destructive actions take two clicks: the first arms, the second runs.
  function confirm(key, action) {
    if (armed !== key) { armed = key; disarm.restart(); return }
    armed = ""
    action()
  }
  function revert(change) { confirm("revert:" + change.id, function() { root.source.revert(change.id) }) }
  // No trailing "/": a group can be one top-level file (mimeapps.list).
  function ignoreGroup(group) { confirm("ignore:" + group.key, function() { root.source.ignore(group.key) }) }
  function toggleDiff(group) {
    if (!root.source) return
    root.source.showDiff(root.source.diffKey === group.key ? "" : group.key)
  }
  function diffColor(kind) {
    return kind === "add" ? Color.accent : kind === "del" ? Color.urgent : kind === "file" ? root.foreground : root.dim
  }

  function restoreCheckpoint(cp) { confirm("restore:" + cp.commit, function() { root.source.restore(cp.commit) }) }
  function undoEntry(entry) { confirm("undo:" + entry.time, function() { root.source.undo(entry.time) }) }
  // An Omarchy update changed files you had edited: take its change back out of just those.
  function keepMine(entry) { confirm("keep:" + entry.time, function() { root.source.undo(entry.time, entry.touched) }) }

  // Who wrote the entry, or else which agents made its changes.
  function entryAuthor(e) {
    if (e.by) return e.by
    var seen = []
    for (var i = 0; i < e.changes.length; i++)
      if (e.changes[i].by && seen.indexOf(e.changes[i].by) < 0) seen.push(e.changes[i].by)
    return seen.join(", ")
  }

  // Omarchy's default agent comes first and alone; the other installed agents
  // are offered once it is connected.
  function agentsToConnect() {
    if (!root.source) return []
    var all = root.source.agents
    var def = all.filter(function(a) { return a["default"] && !a.connected })
    return def.length > 0 ? def : all.filter(function(a) { return !a.connected })
  }
  function agentsCaption() {
    if (!root.source) return ""
    var connected = root.source.agents.filter(function(a) { return a.connected }).map(function(a) { return a.title })
    var next = agentsToConnect()
    if (next.length === 0) return "󰚩 " + connected.join(", ") + (connected.length > 1 ? " describe the changes they make" : " describes the changes it makes")
    if (connected.length > 0) return "󰚩 " + connected.join(", ") + " connected. Also:"
    if (next.length === 1) return "󰚩 Let " + next[0].title + " describe the system changes it makes"
    return "󰚩 Let your AI agents describe the system changes they make"
  }

  function sign(op) { return op === "added" ? "+" : op === "removed" ? "−" : "~" }
  function opColor(op) { return op === "added" ? Color.accent : op === "removed" ? Color.urgent : root.foreground }
  function changeText(c) {
    var text = c.label
    if (c.kind === "package") text = (c.op === "added" ? "Installed " : "Removed ") + c.label
    else if (c.kind === "unit") text = (c.op === "added" ? "Enabled " : "Disabled ") + c.label
    if (c.added || c.removed) text += "  +" + (c.added || 0) + " −" + (c.removed || 0)
    if (c.by) text += "  󰚩 " + c.by
    return text
  }

  function ago(iso) {
    var s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
    if (s < 60) return "now"
    if (s < 3600) return Math.floor(s / 60) + "m ago"
    if (s < 86400) return Math.floor(s / 3600) + "h ago"
    return new Date(iso).toLocaleDateString(Qt.locale(), "d MMM")
  }
  onGroupsChanged: if (selected >= groups.length) selected = Math.max(0, groups.length - 1)

  Timer { id: disarm; interval: 4000; onTriggered: root.armed = "" }

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
      onMoveRequested: function(dx, dy) {
        if (root.groups.length === 0) return
        root.selected = Math.max(0, Math.min(root.groups.length - 1, root.selected + dy))
        pendingList.positionViewAtIndex(root.selected, ListView.Contain)
      }
      onActivateRequested: root.describe(root.current())
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }
      onTextKey: function(text) {
        var key = text.toLowerCase(), g = root.current()
        if (!root.source) return
        if (key === "r") root.source.refresh()
        else if (key === "c") root.newCheckpoint()
        else if (key === "m") root.source.exportMarkdown()
        else if (key === "/") root.openSearch()
        else if (!g) return
        else if (key === "d") root.describe(g)
        else if (key === "s") root.source.skip(g.key)
        else if (key === "v") root.source.openDiff(g.key)
        else if (key === "o") root.toggleDiff(g)
        else if (key === "a") root.suggest(g)
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
            meta: !root.source ? "" : root.source.missing ? "Not installed" : !root.source.initialized ? "Not tracking yet"
              : root.source.count === 0 ? "Everything is recorded"
              : root.source.count + " new" + (root.source.overdue ? ", waiting " + root.source.pendingDays + " days" : "")
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
              text: root.source && root.source.missing
                ? "This widget needs the syschange command. Install builds it from github.com/dDan4a/syschange (v" + root.source.cliVersion + ") into ~/.local/bin with Go. No Go yet? mise fetches one just for the build. Then tracking starts."
                  + (root.source.binary.indexOf("/") >= 0 && root.source.binary.indexOf("/.local/bin/") < 0 ? " Your widget settings point at " + root.source.binary + ": clear that setting to use the installed one." : "")
                : "syschange " + (root.source ? root.source.version : "") + " is older than this widget expects (" + (root.source ? root.source.cliVersion : "") + ")."
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
              text: "Keep a history of your config files in ~/.config, installed packages and enabled services. Describe each change in one line, see the diff and undo it."
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

          PanelSectionHeader {
            visible: root.groups.length > 0
            text: "New"
            foreground: root.foreground
            fontFamily: root.fontFamily
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

            delegate: Rectangle {
              id: groupItem
              required property var modelData
              required property int index
              readonly property bool isSelected: index === root.selected

              width: ListView.view.width
              height: groupColumn.implicitHeight + Style.space(12)
              radius: Style.space(6)
              color: isSelected || hover.hovered ? Qt.rgba(root.foreground.r, root.foreground.g, root.foreground.b, 0.08) : "transparent"

              HoverHandler { id: hover }
              TapHandler { onTapped: root.selected = groupItem.index }

              Column {
                id: groupColumn
                x: Style.space(8)
                y: Style.space(6)
                width: parent.width - Style.space(12)
                spacing: Style.space(2)

                Item {
                  width: parent.width
                  height: Math.max(titleText.implicitHeight, actions.implicitHeight)
                  TapHandler {
                    enabled: groupItem.modelData.key !== "packages" && groupItem.modelData.key !== "services"
                    onTapped: { root.selected = groupItem.index; root.toggleDiff(groupItem.modelData) }
                  }
                  Text {
                    id: titleText
                    anchors.verticalCenter: parent.verticalCenter
                    anchors.left: parent.left
                    anchors.right: actions.left
                    text: root.armed === "ignore:" + groupItem.modelData.key ? "Click again to stop tracking " + groupItem.modelData.title : groupItem.modelData.title
                    color: root.armed === "ignore:" + groupItem.modelData.key ? Color.urgent : root.foreground
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.subtitle
                    font.bold: true
                    elide: Text.ElideRight
                    textFormat: Text.PlainText
                  }
                  Row {
                    id: actions
                    anchors.right: parent.right
                    spacing: Style.space(2)
                    PanelActionButton {
                      iconText: "󰏫" // nf-md-pencil
                      tooltipText: "Describe why (Enter)"
                      foreground: root.foreground
                      onClicked: { root.selected = groupItem.index; root.describe(groupItem.modelData) }
                    }
                    PanelActionButton {
                      readonly property bool working: root.source !== null && root.source.suggestingKey === groupItem.modelData.key
                      iconText: working ? "󰔟" : "󰁨" // nf-md-timer_sand / nf-md-auto_fix
                      tooltipText: working ? "Asking your AI agent…" : "Describe with AI (a): your default agent reads the diff"
                      foreground: working ? Color.accent : root.foreground
                      enabled: root.source !== null && root.source.suggestingKey === ""
                      onClicked: root.suggest(groupItem.modelData)
                    }
                    PanelActionButton {
                      iconText: "󰄬" // nf-md-check
                      tooltipText: "Keep without a description (s)"
                      foreground: root.foreground
                      onClicked: root.source.skip(groupItem.modelData.key)
                    }
                    PanelActionButton {
                      visible: groupItem.modelData.key !== "packages" && groupItem.modelData.key !== "services"
                      iconText: "󰦓" // nf-md-file_compare
                      tooltipText: "Show diff (v)"
                      foreground: root.foreground
                      onClicked: root.source.openDiff(groupItem.modelData.key)
                    }
                    PanelActionButton {
                      visible: groupItem.modelData.key !== "packages" && groupItem.modelData.key !== "services"
                      iconText: "󰈉" // nf-md-eye_off
                      tooltipText: "Stop tracking " + groupItem.modelData.title
                      foreground: root.armed === "ignore:" + groupItem.modelData.key ? Color.urgent : root.foreground
                      onClicked: root.ignoreGroup(groupItem.modelData)
                    }
                  }
                }

                Repeater {
                  model: groupItem.modelData.changes
                  delegate: Item {
                    id: changeItem
                    required property var modelData
                    readonly property bool armed: root.armed === "revert:" + modelData.id
                    width: groupColumn.width
                    height: Math.max(changeText.implicitHeight, undo.implicitHeight)

                    Text {
                      id: signText
                      anchors.verticalCenter: parent.verticalCenter
                      width: Style.space(14)
                      text: root.sign(changeItem.modelData.op)
                      color: root.opColor(changeItem.modelData.op)
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.body
                      font.bold: true
                    }
                    Text {
                      id: changeText
                      anchors.verticalCenter: parent.verticalCenter
                      anchors.left: signText.right
                      anchors.right: ignoreFile.left
                      text: changeItem.armed ? "Click again to undo: " + changeItem.modelData.label : root.changeText(changeItem.modelData)
                      color: changeItem.armed ? Color.urgent : root.foreground
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.body
                      elide: Text.ElideMiddle
                      textFormat: Text.PlainText
                    }
                    PanelActionButton {
                      id: ignoreFile
                      anchors.right: undo.left
                      anchors.verticalCenter: parent.verticalCenter
                      visible: changeItem.modelData.kind === "config"
                      width: visible ? implicitWidth : 0
                      iconText: "󰈉" // nf-md-eye_off
                      tooltipText: "Stop tracking this file"
                      foreground: root.dim
                      onClicked: root.source.ignore(changeItem.modelData.name)
                    }
                    PanelActionButton {
                      id: undo
                      anchors.right: parent.right
                      anchors.verticalCenter: parent.verticalCenter
                      iconText: "󰕌" // nf-md-undo
                      tooltipText: changeItem.modelData.kind === "config" ? "Undo this change" : "Show the command to undo"
                      foreground: changeItem.armed ? Color.urgent : root.dim
                      onClicked: root.revert(changeItem.modelData)
                    }
                  }
                }

                // Inline diff (o, or click the title). Long diffs: v opens a terminal.
                Column {
                  readonly property bool shown: root.source !== null && root.source.diffKey === groupItem.modelData.key
                  visible: shown
                  width: parent.width
                  topPadding: Style.space(4)
                  spacing: 0
                  Text {
                    visible: parent.shown && root.source.diffLines.length === 0
                    text: "Loading diff…"
                    color: root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                  }
                  Repeater {
                    model: parent.shown ? root.source.diffLines.slice(0, 40) : []
                    delegate: Text {
                      required property var modelData
                      width: groupColumn.width
                      text: modelData.text
                      color: root.diffColor(modelData.kind)
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.caption
                      font.bold: modelData.kind === "file"
                      elide: Text.ElideRight
                      textFormat: Text.PlainText
                    }
                  }
                  Text {
                    visible: parent.shown && root.source.diffLines.length > 40
                    text: "… " + (root.source ? root.source.diffLines.length - 40 : 0) + " more lines, press v for the full diff"
                    color: root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                  }
                }
              }
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
              placeholderText: root.editingKey === "@checkpoint" ? "Checkpoint name, for example: before new animations (empty: date)" : "Why? For example: voice typing on Super+R"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onCanceled: root.cancelDescribe()
              onAccepted: root.commitDescribe()
            }
          }

          PanelSeparator { width: parent.width; visible: root.history.length > 0 }

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
              tooltipText: !b ? "" : b.error ? "Backup to " + b.remote + " failed: " + b.error + ". Click to retry"
                : new Date(b.time).getFullYear() < 2000 ? "Backup to " + b.remote + ": not pushed yet. Click to push now"
                : "Backed up to " + b.remote + ", " + root.ago(b.time) + ". Click to push now"
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
            onTriggered: if (root.source && searchLoader.item) root.source.search(searchLoader.item.text.trim())
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

            delegate: Item {
              id: entryItem
              required property var modelData
              readonly property bool armed: root.armed === "undo:" + modelData.time
              width: ListView.view.width
              height: entryColumn.implicitHeight
              opacity: modelData.undone ? 0.5 : 1

              Column {
                id: entryColumn
                anchors.left: parent.left
                anchors.right: undoEntryButton.left
                anchors.rightMargin: Style.space(4)
                spacing: Style.space(1)

                Text {
                  width: parent.width
                  text: entryItem.armed ? "Click again to undo this"
                    : (entryItem.modelData.note || "No description") + (root.entryAuthor(entryItem.modelData) ? "  󰚩 " + root.entryAuthor(entryItem.modelData) : "")
                  color: entryItem.armed ? Color.urgent : entryItem.modelData.note ? root.foreground : root.dim
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.body
                  font.italic: !entryItem.modelData.note
                  font.strikeout: entryItem.modelData.undone === true
                  elide: Text.ElideRight
                  textFormat: Text.PlainText
                }
                Item {
                  readonly property bool armed: root.armed === "keep:" + entryItem.modelData.time
                  visible: (entryItem.modelData.touched || []).length > 0
                  width: parent.width
                  height: visible ? Math.max(touchedText.implicitHeight, keepButton.implicitHeight) : 0
                  Text {
                    id: touchedText
                    anchors.left: parent.left
                    anchors.right: keepButton.left
                    anchors.verticalCenter: parent.verticalCenter
                    text: parent.armed ? "Click again to bring your version back"
                      : "Changed your edits: " + (entryItem.modelData.touched || []).join(", ")
                    color: Color.urgent
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                    elide: Text.ElideMiddle
                    textFormat: Text.PlainText
                  }
                  PanelActionButton {
                    id: keepButton
                    anchors.right: parent.right
                    anchors.verticalCenter: parent.verticalCenter
                    iconText: "󰦛" // nf-md-restore
                    tooltipText: "Keep mine: undo the update in these files only, your other edits and its other changes stay"
                    foreground: parent.armed ? Color.urgent : root.foreground
                    onClicked: root.keepMine(entryItem.modelData)
                  }
                }
                Text {
                  width: parent.width
                  text: root.ago(entryItem.modelData.time) + " · " + entryItem.modelData.title + (entryItem.modelData.undone ? " · undone" : "")
                  color: root.dim
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.caption
                  elide: Text.ElideRight
                  textFormat: Text.PlainText
                }
              }
              PanelActionButton {
                id: undoEntryButton
                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
                visible: !entryItem.modelData.undone
                iconText: "󰕌" // nf-md-undo
                tooltipText: "Undo this change"
                foreground: entryItem.armed ? Color.urgent : root.dim
                onClicked: root.undoEntry(entryItem.modelData)
              }
            }
          }

          PanelSeparator { width: parent.width; visible: root.source !== null && root.source.initialized }

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
            delegate: Item {
              id: cpItem
              required property var modelData
              readonly property bool armed: root.armed === "restore:" + modelData.commit
              width: contentColumn.width
              height: Math.max(cpText.implicitHeight, cpRestore.implicitHeight)
              Text {
                id: cpText
                anchors.left: parent.left
                anchors.right: cpRestore.left
                anchors.verticalCenter: parent.verticalCenter
                text: cpItem.armed ? "Click again to restore config files to “" + cpItem.modelData.name + "”"
                  : cpItem.modelData.name + "  ·  " + root.ago(cpItem.modelData.time)
                color: cpItem.armed ? Color.urgent : root.foreground
                font.family: root.fontFamily
                font.pixelSize: Style.font.body
                elide: Text.ElideRight
                textFormat: Text.PlainText
              }
              PanelActionButton {
                id: cpRestore
                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
                iconText: "󰦛" // nf-md-restore
                tooltipText: "Restore. The current state is saved as a checkpoint first"
                foreground: cpItem.armed ? Color.urgent : root.dim
                onClicked: root.restoreCheckpoint(cpItem.modelData)
              }
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
              text: "Connect " + root.agentsToConnect().map(function(a) { return a.title }).join(" + ")
              bordered: true
              tooltipText: "Adds the syschange skill and hooks. Undo: syschange agents remove"
              onClicked: root.source.connectAgents(root.agentsToConnect().map(function(a) { return a.name }))
            }
          }

          Text {
            width: parent.width
            text: root.editingKey !== "" ? "Enter save · Esc cancel" : "↑↓ select · Enter describe · a AI · s keep · o/v diff · / search · c checkpoint · r refresh"
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
