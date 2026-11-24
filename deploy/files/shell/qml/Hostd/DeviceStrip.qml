import QtQuick
import Hostd.Shell as Core

// The devices at the top left: a square in each one's color and its name,
// bright while it is being used. Shown when there are two or more, so each
// can be told apart by its color (the pointer and the underline have it).
Row {
    id: strip
    objectName: "deviceStrip"

    required property Core.Pointers pointers
    required property Core.Menu menu
    readonly property var devices: pointers.strip

    visible: devices.length > 0 && menu.filter === "" // the filter has this place while typing
    spacing: Theme.spaceXl

    Repeater {
        model: strip.devices

        Row {
            id: entry
            required property var modelData
            spacing: Theme.spaceS
            opacity: modelData.active ? 1 : 0.6
            Behavior on opacity { NumberAnimation { duration: Theme.motionFast } }

            Rectangle {
                objectName: "deviceMark/" + entry.modelData.id
                anchors.verticalCenter: parent.verticalCenter
                width: Theme.spaceS * 2
                height: Theme.spaceS * 2
                color: Devices.colour(entry.modelData.colour)
            }
            Txt {
                objectName: "deviceName/" + entry.modelData.id
                anchors.verticalCenter: parent.verticalCenter
                role: "label"
                text: entry.modelData.label
            }
        }
    }
}
