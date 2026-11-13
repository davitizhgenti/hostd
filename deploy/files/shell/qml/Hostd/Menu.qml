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
        }

        Panel { shell: menu.shell }

        Dialog { shell: menu.shell }

        Hints {
            shell: menu.shell
            anchors.right: parent.right
            anchors.bottom: parent.bottom
            anchors.margins: Theme.layoutSafeMargin
        }

        // The mouse, with no pointer (hostd_shell/glide.py): over everything,
        // so movement glides the selection and a click acts on what is current.
        MouseArea {
            objectName: "glide"
            anchors.fill: parent
            hoverEnabled: true
            cursorShape: Qt.BlankCursor
            acceptedButtons: Qt.AllButtons
            onPositionChanged: (mouse) => menu.shell.pointer(mouse.x, mouse.y, width, height, Date.now() % 2000000000)
            onClicked: (mouse) => menu.shell.mouseButton(
                mouse.button === Qt.RightButton ? "right"
                : mouse.button === Qt.MiddleButton ? "middle"
                : mouse.button === Qt.BackButton ? "back"
                : mouse.button === Qt.ForwardButton ? "forward" : "left")
            onWheel: (wheel) => menu.shell.wheel(wheel.angleDelta.x, wheel.angleDelta.y)
        }
    }
}
