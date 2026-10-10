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
  property int selected: 0
  property string editingKey: ""   // group being described
  property string armed: ""        // destructive action waiting for a second click: "revert:<id>", "ignore:<group>", "undo:<entry>"

  function open() { root.selected = 0; root.editingKey = ""; root.controller.show() }
  function close() { root.editingKey = ""; root.controller.hide() }
  function toggle() { root.opened ? root.close() : root.open() }
  function switchPanel(direction) {
    if (root.bar && typeof root.bar.switchPanelFrom === "function")
      return root.bar.switchPanelFrom(root.barIdentity, direction)
    return false
  }

  function current() { return groups.length > 0 ? groups[Math.min(selected, groups.length - 1)] : null }

  function describe(group) {
    if (!group) return
    editingKey = group.key
    Qt.callLater(function() { noteField.text = ""; noteField.forceActiveFocus() })
  }
  function cancelDescribe() {
    editingKey = ""
    Qt.callLater(function() { if (keyCatcher) keyCatcher.forceActiveFocus() })
  }
  function commitDescribe() {
    var text = noteField.text.trim()
    if (text !== "" && source) source.note(editingKey, text)
    cancelDescribe()
  }

  // Destructive actions take two clicks: the first arms, the second runs.
  function confirm(key, action) {
    if (armed !== key) { armed = key; disarm.restart(); return }
    armed = ""
    action()
  }
  function revert(change) { confirm("revert:" + change.id, function() { root.source.revert(change.id) }) }
  function ignoreGroup(group) { confirm("ignore:" + group.key, function() { root.source.ignore(group.key + "/") }) }
  function undoEntry(entry) { confirm("undo:" + entry.time, function() { root.source.undo(entry.time) }) }

  function sign(op) { return op === "added" ? "+" : op === "removed" ? "−" : "~" }
  function opColor(op) { return op === "added" ? Color.accent : op === "removed" ? Color.urgent : root.foreground }
  function changeText(c) {
    var text = c.label
    if (c.kind === "package") text = (c.op === "added" ? "Installed " : "Removed ") + c.label
    else if (c.kind === "unit") text = (c.op === "added" ? "Enabled " : "Disabled ") + c.label
    if (c.added || c.removed) text += "  +" + (c.added || 0) + " −" + (c.removed || 0)
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
      blocked: root.editingKey !== ""
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
        if (key === "r") root.source.refresh()
        else if (!g) return
        else if (key === "d") root.describe(g)
        else if (key === "s") root.source.skip(g.key)
        else if (key === "v") root.source.openDiff(g.key)
      }

      Column {
        id: contentColumn
        width: parent.width
        spacing: Style.space(10)

        PanelHero {
          width: parent.width
          title: "System changes"
          meta: !root.source ? "" : !root.source.initialized ? "Not tracking yet"
            : root.source.count === 0 ? "Everything is recorded" : root.source.count + " new"
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

        // First run: one button to start.
        Column {
          visible: root.source !== null && !root.source.initialized
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
            }
          }
        }

        Row {
          visible: root.editingKey !== ""
          width: parent.width
          spacing: Style.space(6)
          TextField {
            id: noteField
            width: parent.width
            placeholderText: "Why? For example: voice typing on Super+R"
            foreground: root.foreground
            font.family: root.fontFamily
            Keys.onPressed: function(event) {
              if (event.key === Qt.Key_Escape) { root.cancelDescribe(); event.accepted = true }
              else if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) { root.commitDescribe(); event.accepted = true }
            }
          }
        }

        PanelSeparator { width: parent.width; visible: root.history.length > 0 }

        PanelSectionHeader {
          visible: root.history.length > 0
          text: "History"
          foreground: root.foreground
          fontFamily: root.fontFamily
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
          visible: root.history.length > 0
          width: parent.width
          height: Math.min(contentHeight, Style.space(260))
          clip: true
          spacing: Style.space(6)
          model: root.history.slice(0, 30)
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
                  : (entryItem.modelData.note || "No description") + (entryItem.modelData.by ? "  󰚩 " + entryItem.modelData.by : "")
                color: entryItem.armed ? Color.urgent : entryItem.modelData.note ? root.foreground : root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.body
                font.italic: !entryItem.modelData.note
                font.strikeout: entryItem.modelData.undone === true
                elide: Text.ElideRight
                textFormat: Text.PlainText
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
            text: !root.source ? "" : root.source.agentsConnected
              ? "󰚩 " + root.source.agents.map(function(a) { return a.name }).join(", ") + " describe the changes they make"
              : "󰚩 Let your AI agents describe the system changes they make"
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
            text: "Connect " + (root.source ? root.source.agents.filter(function(a) { return !a.connected }).map(function(a) { return a.name }).join(" + ") : "")
            bordered: true
            tooltipText: "Adds the syschange skill and hooks. Undo: syschange agents remove"
            onClicked: root.source.connectAgents()
          }
        }

        Text {
          width: parent.width
          text: root.editingKey !== "" ? "Enter save · Esc cancel" : "↑↓ select · Enter describe · s keep · v diff · r refresh"
          color: root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
          horizontalAlignment: Text.AlignHCenter
        }
      }
    }
  }
}
