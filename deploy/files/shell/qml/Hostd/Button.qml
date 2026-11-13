import QtQuick

// A choice in a word; the current one is bright and underlined.
Item {
    id: button

    property string text: ""
    property bool current: false
    signal clicked()

    implicitWidth: label.implicitWidth
    implicitHeight: label.implicitHeight + Theme.spaceS + Theme.lineFocus

    Txt {
        id: label
        text: button.text
        role: "body"
        color: button.current ? Theme.colorText : Theme.colorTextMuted
        Behavior on color { ColorAnimation { duration: Theme.motionFast } }
    }

    Rectangle {
        anchors.bottom: parent.bottom
        width: parent.width
        height: Theme.lineFocus
        color: Theme.colorAccent
        opacity: button.current ? 1 : 0
        Behavior on opacity { NumberAnimation { duration: Theme.motionFast } }
    }

    MouseArea {
        anchors.fill: parent
        onClicked: button.clicked()
    }
}
