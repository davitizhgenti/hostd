pragma ComponentBehavior: Bound
import QtQuick

// What the buttons do now, named for the input used last.
Row {
    id: hints

    // The shell (Python): what is current, the data, and what input does.
    required property var shell
    objectName: "hints"
    spacing: Theme.spaceXl

    Repeater {
        model: hints.shell.hints

        Row {
            id: hint
            required property var modelData
            spacing: Theme.spaceS

            Txt { role: "label"; text: hint.modelData.button }
            Txt { role: "label"; muted: true; text: hint.modelData.what }
        }
    }
}
