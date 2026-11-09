pragma ComponentBehavior: Bound

import QtQuick
import qs.Commons
import qs.Ui

Rectangle {
  id: groupItem
  required property var panel
  required property var modelData
  required property int index
  readonly property bool isSelected: index === groupItem.panel.selected

  width: ListView.view.width
  height: groupColumn.implicitHeight + Style.space(12)
  radius: Style.space(6)
  color: isSelected || hover.hovered ? Qt.rgba(groupItem.panel.foreground.r, groupItem.panel.foreground.g, groupItem.panel.foreground.b, 0.08) :
                                       "transparent"

  HoverHandler {
    id: hover
  }
  TapHandler {
    onTapped: groupItem.panel.selected = groupItem.index
  }

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
        onTapped: {
          groupItem.panel.selected = groupItem.index
          groupItem.panel.toggleDiff(groupItem.modelData)
        }
      }
      Text {
        id: titleText
        anchors.verticalCenter: parent.verticalCenter
        anchors.left: parent.left
        anchors.right: actions.left
        text: groupItem.panel.armed === "ignore:" + groupItem.modelData.key ? "Click again to stop tracking " + groupItem.modelData.title :
                                                                              groupItem.modelData.title
        color: groupItem.panel.armed === "ignore:" + groupItem.modelData.key ? Color.urgent : groupItem.panel.foreground
        font.family: groupItem.panel.fontFamily
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
          foreground: groupItem.panel.foreground
          onClicked: {
            groupItem.panel.selected = groupItem.index
            groupItem.panel.describe(groupItem.modelData)
          }
        }
        PanelActionButton {
          readonly property bool working: groupItem.panel.source !== null && groupItem.panel.source.suggestingKey === groupItem.modelData.key
          iconText: working ? "󰔟" : "󰁨" // nf-md-timer_sand / nf-md-auto_fix
          tooltipText: working ? "Asking your AI agent…" : "Describe with AI (a): your default agent reads the diff"
          foreground: working ? Color.accent : groupItem.panel.foreground
          enabled: groupItem.panel.source !== null && groupItem.panel.source.suggestingKey === ""
          onClicked: groupItem.panel.suggest(groupItem.modelData)
        }
        PanelActionButton {
          iconText: "󰄬" // nf-md-check
          tooltipText: "Keep without a description (s)"
          foreground: groupItem.panel.foreground
          onClicked: groupItem.panel.source.skip(groupItem.modelData.key)
        }
        PanelActionButton {
          visible: groupItem.modelData.key !== "packages" && groupItem.modelData.key !== "services"
          iconText: "󰦓" // nf-md-file_compare
          tooltipText: "Show diff (v)"
          foreground: groupItem.panel.foreground
          onClicked: groupItem.panel.source.openDiff(groupItem.modelData.key)
        }
        PanelActionButton {
          visible: groupItem.modelData.key !== "packages" && groupItem.modelData.key !== "services"
          iconText: "󰈉" // nf-md-eye_off
          tooltipText: "Stop tracking " + groupItem.modelData.title
          foreground: groupItem.panel.armed === "ignore:" + groupItem.modelData.key ? Color.urgent : groupItem.panel.foreground
          onClicked: groupItem.panel.ignoreGroup(groupItem.modelData)
        }
      }
    }

    Repeater {
      model: groupItem.modelData.changes
      delegate: Item {
        id: changeItem
        required property var modelData
        readonly property bool armed: groupItem.panel.armed === "revert:" + modelData.id
        width: groupColumn.width
        height: Math.max(changeText.implicitHeight, undo.implicitHeight)

        Text {
          id: signText
          anchors.verticalCenter: parent.verticalCenter
          width: Style.space(14)
          text: groupItem.sign(changeItem.modelData.op)
          color: groupItem.opColor(changeItem.modelData.op)
          font.family: groupItem.panel.fontFamily
          font.pixelSize: Style.font.body
          font.bold: true
        }
        Text {
          id: changeText
          anchors.verticalCenter: parent.verticalCenter
          anchors.left: signText.right
          anchors.right: ignoreFile.left
          text: changeItem.armed ? "Click again to undo: " + changeItem.modelData.label : groupItem.changeText(changeItem.modelData)
          color: changeItem.armed ? Color.urgent : groupItem.panel.foreground
          font.family: groupItem.panel.fontFamily
          font.pixelSize: Style.font.body
          elide: Text.ElideMiddle
          textFormat: Text.PlainText
        }
        PanelActionButton {
          id: ignoreFile
          anchors.right: undo.left
          anchors.verticalCenter: parent.verticalCenter
          visible: changeItem.modelData.kind === "config"
          width: ignoreFile.visible ? ignoreFile.implicitWidth : 0
          iconText: "󰈉" // nf-md-eye_off
          tooltipText: "Stop tracking this file"
          foreground: groupItem.panel.dim
          onClicked: groupItem.panel.source.ignore(changeItem.modelData.name)
        }
        PanelActionButton {
          id: undo
          anchors.right: parent.right
          anchors.verticalCenter: parent.verticalCenter
          iconText: "󰕌" // nf-md-undo
          tooltipText: changeItem.modelData.kind === "config" ? "Undo this change" : "Show the command to undo"
          foreground: changeItem.armed ? Color.urgent : groupItem.panel.dim
          onClicked: groupItem.panel.revert(changeItem.modelData)
        }
      }
    }

    // Inline diff (o, or click the title). Long diffs: v opens a terminal.
    Column {
      readonly property bool shown: groupItem.panel.source !== null && groupItem.panel.source.diffKey === groupItem.modelData.key
      visible: shown
      width: parent.width
      topPadding: Style.space(4)
      spacing: 0
      Text {
        visible: parent.shown && groupItem.panel.source.diffLines.length === 0
        text: "Loading diff…"
        color: groupItem.panel.dim
        font.family: groupItem.panel.fontFamily
        font.pixelSize: Style.font.caption
      }
      Repeater {
        model: parent.shown ? groupItem.panel.source.diffLines.slice(0, 40) : []
        delegate: Text {
          required property var modelData
          width: groupColumn.width
          text: modelData.text
          color: groupItem.diffColor(modelData.kind)
          font.family: groupItem.panel.fontFamily
          font.pixelSize: Style.font.caption
          font.bold: modelData.kind === "file"
          elide: Text.ElideRight
          textFormat: Text.PlainText
        }
      }
      Text {
        visible: parent.shown && groupItem.panel.source.diffLines.length > 40
        text: "… " + (groupItem.panel.source ? groupItem.panel.source.diffLines.length - 40 : 0) + " more lines, press v for the full diff"
        color: groupItem.panel.dim
        font.family: groupItem.panel.fontFamily
        font.pixelSize: Style.font.caption
      }
    }
  }

  function sign(op) {
    return op === "added" ? "+" : op === "removed" ? "−" : "~"
  }
  function opColor(op) {
    return op === "added" ? Color.accent : op === "removed" ? Color.urgent : groupItem.panel.foreground
  }
  function changeText(c) {
    var text = c.label
    if (c.kind === "package")
      text = (c.op === "added" ? "Installed " : "Removed ") + c.label
    else if (c.kind === "unit")
      text = (c.op === "added" ? "Enabled " : "Disabled ") + c.label
    if (c.added || c.removed)
      text += "  +" + (c.added || 0) + " −" + (c.removed || 0)
    if (c.by)
      text += "  󰚩 " + c.by
    return text
  }
  function diffColor(kind) {
    return kind === "add" ? Color.accent : kind === "del" ? Color.urgent : kind === "file" ? groupItem.panel.foreground : groupItem.panel.dim
  }
}
