pragma ComponentBehavior: Bound

import QtQuick
import qs.Commons
import qs.Ui

Item {
  id: entryItem
  required property var panel
  required property var modelData
  readonly property bool armed: entryItem.panel.armed === "undo:" + modelData.time
  readonly property string caption: {
    const by = entryAuthor(modelData)
    return (modelData.note || "No description") + (by ? "  󰚩 " + by : "")
  }
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
      text: entryItem.armed ? "Click again to undo this" : entryItem.caption
      color: entryItem.armed ? Color.urgent : entryItem.modelData.note ? entryItem.panel.foreground : entryItem.panel.dim
      font.family: entryItem.panel.fontFamily
      font.pixelSize: Style.font.body
      font.italic: !entryItem.modelData.note
      font.strikeout: entryItem.modelData.undone === true
      elide: Text.ElideRight
      textFormat: Text.PlainText
    }
    Item {
      readonly property bool armed: entryItem.panel.armed === "keep:" + entryItem.modelData.time
      visible: (entryItem.modelData.touched || []).length > 0
      width: parent.width
      height: visible ? Math.max(touchedText.implicitHeight, keepButton.implicitHeight) : 0
      Text {
        id: touchedText
        anchors.left: parent.left
        anchors.right: keepButton.left
        anchors.verticalCenter: parent.verticalCenter
        text: parent.armed ? "Click again to bring your version back" : "Changed your edits: " + (entryItem.modelData.touched || []).join(", ")

        color: Color.urgent
        font.family: entryItem.panel.fontFamily
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
        foreground: parent.armed ? Color.urgent : entryItem.panel.foreground
        onClicked: entryItem.panel.keepMine(entryItem.modelData)
      }
    }
    Text {
      width: parent.width
      text: entryItem.panel.ago(entryItem.modelData.time) + " · " + entryItem.modelData.title + (entryItem.modelData.undone ? " · undone" : "")
      color: entryItem.panel.dim
      font.family: entryItem.panel.fontFamily
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
    foreground: entryItem.armed ? Color.urgent : entryItem.panel.dim
    onClicked: entryItem.panel.undoEntry(entryItem.modelData)
  }

  function entryAuthor(e) {
    if (e.by)
      return e.by
    var seen = []
    for (let i = 0; i < e.changes.length; i++)
      if (e.changes[i].by && seen.indexOf(e.changes[i].by) < 0)
        seen.push(e.changes[i].by)
    return seen.join(", ")
  }
}
