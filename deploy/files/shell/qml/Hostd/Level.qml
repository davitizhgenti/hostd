import QtQuick

// A level: a hairline track and its filled part, across or upright.
Item {
    id: level

    property real value: 0
    property real maximum: 100
    property bool vertical: false
    readonly property real part: Math.max(0, Math.min(1, value / maximum))

    implicitWidth: vertical ? Theme.lineFocus : Theme.layoutPanelWidth / 2
    implicitHeight: vertical ? Theme.layoutPanelWidth / 2 : Theme.lineFocus

    Rectangle {
        anchors.fill: parent
        color: Theme.colorBorder
    }

    Rectangle {
        color: Theme.colorAccent
        x: 0
        y: level.vertical ? parent.height * (1 - level.part) : 0
        width: level.vertical ? parent.width : parent.width * level.part
        height: level.vertical ? parent.height * level.part : parent.height

        Behavior on width { NumberAnimation { duration: Theme.motionFast; easing.type: Easing.OutCubic } }
        Behavior on height { NumberAnimation { duration: Theme.motionFast; easing.type: Easing.OutCubic } }
        Behavior on y { NumberAnimation { duration: Theme.motionFast; easing.type: Easing.OutCubic } }
    }
}
