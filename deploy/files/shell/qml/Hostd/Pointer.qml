import QtQuick

// The pointer: a small flat square, a mouse's or a Wii Remote's, drawn over
// everything. A dark line outside the light one keeps it visible on any
// background; it is filled while a button is held. It is shown only while
// the shell says it is in use (it fades after a few seconds without
// moving, and goes while the keyboard or a pad is used).
Item {
    id: pointer

    // The shell (Python): pointer = {x, y (shares of the screen), visible, down}.
    required property var shell
    readonly property var now: shell.pointer

    objectName: "pointer"
    x: Math.round(now.x * (parent ? parent.width : 0)) - width / 2
    y: Math.round(now.y * (parent ? parent.height : 0)) - height / 2
    width: Theme.layoutPointer
    height: Theme.layoutPointer
    opacity: now.visible ? 1 : 0
    visible: opacity > 0
    z: 1000
    Behavior on opacity { NumberAnimation { duration: Theme.motionBase } }

    Rectangle {
        anchors.fill: parent
        anchors.margins: -Theme.lineHair
        color: "transparent"
        border.width: Theme.lineHair
        border.color: Theme.colorBg
    }
    Rectangle {
        anchors.fill: parent
        color: pointer.now.down ? Theme.colorText : "transparent"
        border.width: Theme.lineFocus
        border.color: Theme.colorText
    }
    Rectangle {
        anchors.fill: parent
        anchors.margins: Theme.lineFocus
        color: "transparent"
        border.width: Theme.lineHair
        border.color: Theme.colorBg
    }
}
