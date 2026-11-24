import QtQuick
import Hostd.Shell as Core

// The menu: a full-screen window over the apps (its app_id is hostd-menu,
// so hostd knows it). Shown while the shell says so; keys, clicks and the
// wheel go to the shell, which decides what they do.
Window {
    id: window

    // The app's shell: the menu's state, the pointers, and the rest.
    required property Core.Shell shell

    title: "hostd"
    color: Theme.colorBg
    visibility: !window.shell.menu.visible ? Window.Hidden : window.shell.menu.windowed ? Window.Windowed : Window.FullScreen
    onActiveChanged: window.shell.menu.setMenuActive(active)
    onClosing: (close) => { close.accepted = false; window.shell.menu.hideMenu() }

    Backdrop {
        look: window.shell.backdrop
        anchors.fill: parent
    }

    Item {
        id: scene
        anchors.fill: parent
        focus: true

        Keys.onPressed: (event) => {
            event.accepted = window.shell.menu.keyCode(event.key, event.text, (event.modifiers & Qt.ShiftModifier) !== 0)
        }

        Cross {
            menu: window.shell.menu
            pointers: window.shell.pointers
            anchors.fill: parent
        }

        DeviceStrip {
            pointers: window.shell.pointers
            menu: window.shell.menu
            x: Theme.layoutSafeMargin
            y: Theme.layoutSafeMargin
        }

        // Typing on Apps filters it.
        Row {
            objectName: "filter"
            x: Theme.layoutSafeMargin
            y: Theme.layoutSafeMargin
            spacing: Theme.spaceM
            visible: window.shell.menu.filter !== ""

            Txt { role: "label"; caps: true; muted: true; text: "Filter"; anchors.baseline: typed.baseline }
            Txt { id: typed; role: "title"; text: window.shell.menu.filter }
            Rectangle {
                width: Theme.lineFocus
                height: typed.height
                color: Theme.colorAccent
                SequentialAnimation on opacity {
                    loops: Animation.Infinite
                    NumberAnimation { to: 0; duration: 500 }
                    NumberAnimation { to: 1; duration: 500 }
                }
            }
        }

        Clock {
            ticker: window.shell.clock
            menu: window.shell.menu
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: Theme.layoutSafeMargin
        }

        Txt {
            objectName: "message"
            anchors.left: parent.left
            anchors.bottom: parent.bottom
            anchors.margins: Theme.layoutSafeMargin
            role: "label"
            text: window.shell.menu.message
            color: window.shell.menu.messageError ? Theme.colorDanger : Theme.colorTextMuted
        }

        // Over the menu: a dimmed screen under the panel and the question.
        Rectangle {
            anchors.fill: parent
            color: Theme.colorBg
            opacity: window.shell.menu.panel !== "" || window.shell.menu.question !== "" ? 0.6 : 0
            visible: opacity > 0
            Behavior on opacity { NumberAnimation { duration: Theme.motionBase } }

            MouseArea {  // a click outside the panel or the question closes it
                anchors.fill: parent
                enabled: parent.visible
                cursorShape: Qt.BlankCursor
                onClicked: window.shell.menu.dismiss()
            }
        }

        Panel { menu: window.shell.menu; pointers: window.shell.pointers }

        Dialog { menu: window.shell.menu }

        Hints {
            menu: window.shell.menu
            anchors.right: parent.right
            anchors.bottom: parent.bottom
            anchors.margins: Theme.layoutSafeMargin
        }

        Calibrate { pointers: window.shell.pointers; anchors.fill: parent }

        // The pointers: the mouse's, and one for each Wii Remote (hostd's
        // display.pointer); a remote's finds the row it is over and clicks it.
        PointerHits { pointers: window.shell.pointers; menu: window.shell.menu; scene: scene }
        Pointers { source: window.shell.pointers; anchors.fill: parent; z: 1000 }

        // The mouse, over everything but taking nothing: where it is, the wheel,
        // and the buttons that are on no row (the rows have their own).
        HoverHandler {
            objectName: "pointerTracker"
            cursorShape: Qt.BlankCursor
            // Only a mouse that is over the window: with none, Qt still reports (0, 0).
            onPointChanged: if (hovered) window.shell.pointers.mouseAt(point.position.x, point.position.y, window.width, window.height)
        }
        WheelHandler {
            onWheel: (event) => window.shell.menu.wheel(event.angleDelta.x, event.angleDelta.y)
        }
        TapHandler {
            acceptedButtons: Qt.RightButton | Qt.MiddleButton | Qt.BackButton | Qt.ForwardButton
            onTapped: (point, button) => window.shell.menu.mouseButton(
                button === Qt.RightButton ? "right" : button === Qt.MiddleButton ? "middle"
                : button === Qt.BackButton ? "back" : "forward")
        }
        TapHandler {
            acceptedButtons: Qt.LeftButton
            onPressedChanged: window.shell.pointers.mouseDown(pressed)
        }
    }
}
