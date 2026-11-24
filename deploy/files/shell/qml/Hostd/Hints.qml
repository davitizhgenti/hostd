pragma ComponentBehavior: Bound
import QtQuick
import Hostd.Shell as Core

// What the buttons do now, named for the input used last.
Row {
    id: hints

    required property Core.Menu menu
    objectName: "hints"
    spacing: Theme.spaceXl

    Repeater {
        model: hints.menu.hintList

        Row {
            id: hint
            required property var modelData
            spacing: Theme.spaceS

            // The button as a square key cap: a hairline box, so it reads as a key.
            Item {
                width: cap.implicitWidth + 2 * Theme.spaceS
                height: cap.implicitHeight + Theme.spaceXs
                anchors.verticalCenter: what.verticalCenter
                Rectangle {
                    anchors.fill: parent
                    color: "transparent"
                    border.width: Theme.lineHair
                    border.color: Theme.colorTextMuted
                }
                Txt { id: cap; anchors.centerIn: parent; role: "label"; text: hint.modelData.button }
            }
            Txt { id: what; role: "label"; muted: true; text: hint.modelData.what }
        }
    }
}
