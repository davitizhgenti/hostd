import QtQuick

// A question across the middle of the screen, answered No or Yes; No is
// chosen first.
Rectangle {
    id: dialog

    // The shell (Python): what is current, the data, and what input does.
    required property var shell
    objectName: "band"

    width: parent.width
    height: column.implicitHeight + 2 * Theme.spaceXxl
    anchors.verticalCenter: parent.verticalCenter
    color: Theme.colorSurface
    visible: opacity > 0
    opacity: dialog.shell.question !== "" ? 1 : 0
    Behavior on opacity { NumberAnimation { duration: Theme.motionFast } }

    Rectangle { width: parent.width; height: Theme.lineHair; color: Theme.colorBorder }
    Rectangle { anchors.bottom: parent.bottom; width: parent.width; height: Theme.lineHair; color: Theme.colorBorder }

    Column {
        id: column
        anchors.centerIn: parent
        spacing: Theme.spaceXl

        Txt {
            anchors.horizontalCenter: parent.horizontalCenter
            role: "title"
            text: dialog.shell.question
        }

        Row {
            anchors.horizontalCenter: parent.horizontalCenter
            spacing: Theme.spaceXxl

            Button {
                text: "No"
                current: !dialog.shell.answerYes
                onClicked: dialog.shell.dismiss()
            }

            Button {
                text: "Yes"
                current: dialog.shell.answerYes
                onClicked: { dialog.shell.handle("right"); dialog.shell.handle("activate") }
            }
        }
    }
}
