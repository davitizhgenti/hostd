import QtQuick

// One pointer: a small flat square, a mouse's or a Wii Remote's, drawn in
// its device's color over everything. A dark line outside keeps it visible
// on any background; it is filled while a button is held. It is shown only
// while the shell says it is in use (a mouse's fades after a few seconds
// without moving, and goes while the keyboard or a pad is used).
Item {
    id: pointer

    // The pointer's state (Python): {id, x, y (shares of the screen), visible,
    // down, colour}; first: the one the tests and the layout contract name.
    required property var now
    property bool first: false
    readonly property color tint: Devices.colour(now.colour)

    objectName: first ? "pointer" : "pointer/" + now.id
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
        color: pointer.now.down ? pointer.tint : "transparent"
        border.width: Theme.lineFocus
        border.color: pointer.tint
    }
    Rectangle {
        anchors.fill: parent
        anchors.margins: Theme.lineFocus
        color: "transparent"
        border.width: Theme.lineHair
        border.color: Theme.colorBg
    }
}
