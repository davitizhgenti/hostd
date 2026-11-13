import QtQuick

// A value being changed (the volume), or a short state (whose screen).
Rectangle {
    id: osd

    property string name: ""
    property string value: ""
    property int level: -1

    implicitWidth: Theme.layoutOsdWidth
    implicitHeight: column.implicitHeight + 2 * Theme.spaceL
    color: Theme.colorSurfaceRaised

    Column {
        id: column
        x: Theme.spaceXl
        y: Theme.spaceL
        width: parent.width - 2 * Theme.spaceXl
        spacing: Theme.spaceM

        Item {
            width: parent.width
            height: valueText.implicitHeight

            Txt { anchors.verticalCenter: parent.verticalCenter; role: "label"; caps: true; muted: true; text: osd.name }
            Txt { id: valueText; anchors.right: parent.right; role: "title"; text: osd.value }
        }

        Level {
            width: parent.width
            visible: osd.level >= 0
            value: osd.level
            maximum: 150
        }
    }
}
