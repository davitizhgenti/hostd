import QtQuick
import Hostd.Shell as Core

// A question across the middle of the screen, answered No or Yes; No is
// chosen first.
Rectangle {
    id: dialog

    required property Core.Menu menu
    objectName: "band"

    width: parent.width
    height: column.implicitHeight + 2 * Theme.spaceXxl
    anchors.verticalCenter: parent.verticalCenter
    color: Theme.colorSurface
    visible: opacity > 0
    opacity: dialog.menu.question !== "" ? 1 : 0
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
            text: dialog.menu.question
        }

        Row {
            anchors.horizontalCenter: parent.horizontalCenter
            spacing: Theme.spaceXxl

            Button {
                text: "No"
                current: !dialog.menu.answerYes
                onClicked: dialog.menu.dismiss()
            }

            Button {
                text: "Yes"
                current: dialog.menu.answerYes
                onClicked: dialog.menu.answer(true)
            }
        }
    }
}
