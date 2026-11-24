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

            Txt { role: "label"; text: hint.modelData.button }
            Txt { role: "label"; muted: true; text: hint.modelData.what }
        }
    }
}
