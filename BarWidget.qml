import QtQuick
import Quickshell
import qs.Commons
import qs.Ui

BarWidget {
  id: root
  moduleName: "dan4a.syschange"

  readonly property bool opened: panelLoader.item ? panelLoader.item.opened === true : false
  readonly property real openPanelIndicatorWidth: button.implicitWidth
  readonly property real openPanelIndicatorHeight: Math.max(Style.space(10), Math.round(Style.bar.iconSlot * 0.55))
  readonly property bool popoutSwitchClosing: panelLoader.item ? panelLoader.item.popoutSwitchClosing === true : false

  // One service instance shared by the widgets on every monitor.
  readonly property var source: bar && bar.shell ? bar.shell.serviceFor("dan4a.syschange") : null
  readonly property int count: source ? source.count : 0
  readonly property string error: source ? source.error : ""

  function open() { if (panelLoader.item) panelLoader.item.open() }
  function close() { if (panelLoader.item) panelLoader.item.close() }
  function togglePanel() { if (panelLoader.item) panelLoader.item.toggle() }
  function closeForPopoutSwitch() { if (panelLoader.item) panelLoader.item.closeForPopoutSwitch() }

  function injectPanel() {
    var target = panelLoader.item
    if (!target) return
    target.bar = root.bar
    target.settings = root.settings
    target.anchorItem = button
    target.hostWidget = root
    target.source = root.source
  }

  // Settings live on the bar widget entry; push them into the shared service.
  function configureSource() {
    if (!source) return
    source.binary = String(setting("binary", "syschange")).replace(/^~(?=\/)/, Quickshell.env("HOME"))
    source.intervalSec = Number(setting("refreshIntervalSec", 30))
    source.exportPath = String(setting("exportPath", "~/Documents/System changes.md")).replace(/^~(?=\/)/, Quickshell.env("HOME"))
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight
  onBarChanged: injectPanel()
  onSettingsChanged: { configureSource(); injectPanel() }
  onSourceChanged: { configureSource(); injectPanel() }

  Loader {
    id: panelLoader
    active: true
    source: Qt.resolvedUrl("Panel.qml")
    visible: false
    onLoaded: { root.injectPanel(); Qt.callLater(root.injectPanel) }
  }

  WidgetButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    active: root.opened
    // nf-md-history
    text: root.count > 0 ? "󰋚 " + root.count : "󰋚"
    foreground: root.error !== "" ? Color.urgent : root.count > 0 ? Color.accent : (root.bar ? root.bar.barForeground : Color.foreground)
    dimmed: root.count === 0 && root.error === ""
    tooltipText: root.error !== "" ? "System changes: " + root.error
      : root.count === 0 ? "System changes: everything is recorded"
      : "System changes: " + root.count + " new, click to describe or undo"
    onPressed: root.togglePanel()
  }
}
