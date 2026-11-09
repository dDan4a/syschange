pragma ComponentBehavior: Bound

import QtQuick
import qs.Commons
import qs.Ui

Item {
  id: cpItem
  required property var panel
  required property var modelData
  readonly property bool armed: cpItem.panel.armed === "restore:" + modelData.commit
  height: Math.max(cpText.implicitHeight, cpRestore.implicitHeight)
  Text {
    id: cpText
    anchors.left: parent.left
    anchors.right: cpRestore.left
    anchors.verticalCenter: parent.verticalCenter
    text: cpItem.armed ? "Click again to restore config files to “" + cpItem.modelData.name + "”" : cpItem.modelData.name + "  ·  " + cpItem.panel.ago(
                           cpItem.modelData.time)
    color: cpItem.armed ? Color.urgent : cpItem.panel.foreground
    font.family: cpItem.panel.fontFamily
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
    foreground: cpItem.armed ? Color.urgent : cpItem.panel.dim
    onClicked: cpItem.panel.restoreCheckpoint(cpItem.modelData)
  }
}
