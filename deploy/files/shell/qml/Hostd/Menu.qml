import QtQuick

// The menu: a full-screen window over the apps (its app_id is hostd-menu,
// so hostd knows it). Shown while the shell says so; keys, clicks and the
// wheel go to the shell, which decides what they do.
Window {
    id: menu

    // The shell (Python): what is current, the data, and what input does.
    required property var shell

    title: "hostd"
    color: Theme.colorBg
    visibility: !menu.shell.menuVisible ? Window.Hidden : menu.shell.windowed ? Window.Windowed : Window.FullScreen
    onActiveChanged: menu.shell.setMenuActive(active)
    onClosing: (close) => { close.accepted = false; menu.shell.hideMenu() }

    Backdrop {
        shell: menu.shell
        anchors.fill: parent
    }

    Item {
        anchors.fill: parent
        focus: true

        Keys.onPressed: (event) => {
            event.accepted = menu.shell.keyCode(event.key, event.text, (event.modifiers & Qt.ShiftModifier) !== 0)
        }

        Cross {
            shell: menu.shell
            anchors.fill: parent
        }

        // Typing on Apps filters it.
        Row {
            objectName: "filter"
            x: Theme.layoutSafeMargin
            y: Theme.layoutSafeMargin
            spacing: Theme.spaceM
            visible: menu.shell.filter !== ""

            Txt { role: "label"; caps: true; muted: true; text: "Filter"; anchors.baseline: typed.baseline }
            Txt { id: typed; role: "title"; text: menu.shell.filter }
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
            shell: menu.shell
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
            text: menu.shell.message
            color: menu.shell.messageError ? Theme.colorDanger : Theme.colorTextMuted
        }

        // Over the menu: a dimmed screen under the panel and the question.
        Rectangle {
            anchors.fill: parent
            color: Theme.colorBg
            opacity: menu.shell.panel !== "" || menu.shell.question !== "" ? 0.6 : 0
            visible: opacity > 0
            Behavior on opacity { NumberAnimation { duration: Theme.motionBase } }

            MouseArea {  // a click outside the panel or the question closes it
                anchors.fill: parent
                enabled: parent.visible
                cursorShape: Qt.BlankCursor
                onClicked: menu.shell.dismiss()
            }
        }

        Panel { shell: menu.shell }

        Dialog { shell: menu.shell }

        Hints {
            shell: menu.shell
            anchors.right: parent.right
            anchors.bottom: parent.bottom
            anchors.margins: Theme.layoutSafeMargin
        }

        // The pointer: a mouse's, or a Wii Remote's (hostd's display.pointer).
        Pointer { shell: menu.shell }

        // The mouse, over everything but taking nothing: where it is, the wheel,
        // and the buttons that are on no row (the rows have their own).
        HoverHandler {
            objectName: "pointerTracker"
            cursorShape: Qt.BlankCursor
            onPointChanged: menu.shell.mouseAt(point.position.x, point.position.y, menu.width, menu.height)
        }
        WheelHandler {
            onWheel: (event) => menu.shell.wheel(event.angleDelta.x, event.angleDelta.y)
        }
        TapHandler {
            acceptedButtons: Qt.RightButton | Qt.MiddleButton | Qt.BackButton | Qt.ForwardButton
            onTapped: (point, button) => menu.shell.mouseButton(
                button === Qt.RightButton ? "right" : button === Qt.MiddleButton ? "middle"
                : button === Qt.BackButton ? "back" : "forward")
        }
        TapHandler {
            acceptedButtons: Qt.LeftButton
            onPressedChanged: menu.shell.mouseDown(pressed)
        }
    }
}
