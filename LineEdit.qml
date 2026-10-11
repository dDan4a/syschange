import QtQuick
import Quickshell.Io
import qs.Commons

// A one-line text input built from key events. Unlike TextField it never
// watches the clipboard: Qt's text inputs read foreign clipboard text on every
// clipboard change (to know whether they could paste), with no size limit or
// deadline, which can stall the shell. Pasting here happens only on Ctrl+V,
// through wl-paste in its own process, bounded in time and size.
FocusScope {
  id: root

  property string text: ""
  property string placeholderText: ""
  property color foreground: Color.foreground
  property string fontFamily: Style.font.family
  // The whole text is selected: the next character replaces it (like selectAll()).
  property bool selected: false
  readonly property int maxLength: 500

  signal accepted()
  signal canceled()

  function selectAll() { selected = text !== "" }

  implicitHeight: label.implicitHeight + Style.space(12)
  activeFocusOnTab: true

  function insert(s) {
    s = String(s).replace(/[\x00-\x1f\x7f]/g, " ")
    text = ((selected ? "" : text) + s).slice(0, maxLength)
    selected = false
  }

  Keys.onPressed: function(event) {
    var ctrl = (event.modifiers & Qt.ControlModifier) !== 0
    if (event.key === Qt.Key_Escape) { root.canceled(); event.accepted = true; return }
    if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) { root.accepted(); event.accepted = true; return }
    if (event.key === Qt.Key_Backspace) {
      if (root.selected) root.text = ""
      else if (ctrl) root.text = root.text.replace(/\S*\s*$/, "") // the last word
      else root.text = root.text.slice(0, -1)
      root.selected = false
      event.accepted = true
      return
    }
    if (ctrl && event.key === Qt.Key_U) { root.text = ""; root.selected = false; event.accepted = true; return }
    if (ctrl && event.key === Qt.Key_A) { root.selectAll(); event.accepted = true; return }
    if (ctrl && event.key === Qt.Key_V) { if (!paste.running) paste.running = true; event.accepted = true; return }
    if (event.key === Qt.Key_Tab || event.key === Qt.Key_Backtab) return
    if (!ctrl && !(event.modifiers & Qt.AltModifier) && event.text !== "" && event.text >= " " && event.text !== "\x7f") {
      root.insert(event.text)
      event.accepted = true
    }
  }

  // Explicit paste only: at most 2 seconds and maxLength bytes, outside the shell process.
  Process {
    id: paste
    command: ["sh", "-c", "timeout 2 wl-paste --no-newline --type text 2>/dev/null | head -c 500"]
    stdout: StdioCollector { id: pasted; waitForEnd: true }
    onExited: if (pasted.text !== "") root.insert(pasted.text.split("\n")[0])
  }

  TapHandler { onTapped: root.forceActiveFocus() }

  Rectangle {
    anchors.fill: parent
    radius: Style.cornerRadius
    color: Qt.rgba(root.foreground.r, root.foreground.g, root.foreground.b, root.activeFocus ? 0.10 : 0.05)
    border.width: 1
    border.color: root.activeFocus ? Color.accent : Qt.rgba(root.foreground.r, root.foreground.g, root.foreground.b, 0.2)
  }

  Row {
    anchors.left: parent.left
    anchors.right: parent.right
    anchors.leftMargin: Style.space(10)
    anchors.rightMargin: Style.space(10)
    anchors.verticalCenter: parent.verticalCenter
    Text {
      id: label
      width: Math.min(implicitWidth, parent.width - caret.width)
      text: root.text !== "" ? root.text : root.placeholderText
      color: root.text !== "" ? root.foreground : Qt.darker(root.foreground, 1.6)
      font.family: root.fontFamily
      font.pixelSize: Style.font.body
      elide: root.text !== "" ? Text.ElideLeft : Text.ElideRight // keep the end you are typing visible
      textFormat: Text.PlainText
      Rectangle {
        visible: root.selected
        anchors.fill: parent
        color: Color.accent
        opacity: 0.3
      }
    }
    Rectangle {
      id: caret
      visible: root.activeFocus && root.text !== ""
      width: Math.max(1, Style.space(1))
      height: label.implicitHeight
      color: root.foreground
      SequentialAnimation on opacity {
        running: caret.visible
        loops: Animation.Infinite
        NumberAnimation { to: 0; duration: 500 }
        NumberAnimation { to: 1; duration: 500 }
      }
    }
  }
}
