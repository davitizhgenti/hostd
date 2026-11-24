pragma ComponentBehavior: Bound
import QtQuick
import Hostd.Shell as Core

// The status strip at the top left, inside the safe margin and never wider
// than Place.stripWidth (so never over the clock): two lines of squares.
// The first: services that are up, starting or failed, each a small square
// in its state's colour (the first that is not running also names itself).
// The second: the devices in use, each a square in its own colour (the one
// in use names itself). A line holds at most Place.stripMax squares, then
// "+N": the full lists are on the Status tab. Nothing here moves the cursor.
Column {
    id: strip
    objectName: "statusStrip"

    required property Core.Strip status
    required property Core.Menu menu

    x: Place.stripLeft
    y: Place.stripTop
    width: Place.stripWidth
    spacing: Theme.spaceS
    // The filter has this place while typing; an open panel or question owns the screen.
    visible: (status.services.length > 0 || status.devices.length > 0) && menu.filter === ""
             && menu.panel === "" && menu.question === ""

    readonly property int firstOff: {
        for (let i = 0; i < status.services.length; i++)
            if (status.services[i].state !== "running") return i
        return -1
    }

    Row {
        objectName: "serviceLine"
        visible: strip.status.services.length > 0
        spacing: Theme.spaceS
        height: Place.stripMark

        Repeater {
            model: strip.status.services

            Row {
                id: svc
                required property var modelData
                required property int index
                spacing: Theme.spaceS
                height: Place.stripMark

                Rectangle {
                    objectName: "serviceMark/" + svc.index
                    anchors.verticalCenter: parent.verticalCenter
                    width: Theme.spaceS
                    height: Theme.spaceS
                    color: svc.modelData.tone === "danger" ? Theme.colorDanger
                         : svc.modelData.tone === "warning" ? Theme.colorWarning
                         : Theme.colorPositive
                }
                Txt {
                    objectName: "serviceName/" + svc.index
                    anchors.verticalCenter: parent.verticalCenter
                    visible: svc.index === strip.firstOff
                    role: "label"
                    text: svc.modelData.name + " " + svc.modelData.state
                }
            }
        }
        Txt {
            objectName: "serviceMore"
            anchors.verticalCenter: parent.verticalCenter
            visible: strip.status.servicesMore > 0
            role: "label"
            muted: true
            text: "+" + strip.status.servicesMore
        }
    }

    Row {
        objectName: "deviceLine"
        visible: strip.status.devices.length > 0
        spacing: Theme.spaceS
        height: Place.stripMark

        Repeater {
            model: strip.status.devices

            Row {
                id: entry
                required property var modelData
                spacing: Theme.spaceS
                height: Place.stripMark
                opacity: modelData.active ? 1 : 0.6
                Behavior on opacity { NumberAnimation { duration: Theme.motionFast } }

                Rectangle {
                    objectName: "deviceMark/" + entry.modelData.id
                    anchors.verticalCenter: parent.verticalCenter
                    width: Place.stripMark
                    height: Place.stripMark
                    color: Devices.colour(entry.modelData.colour)
                }
                Txt {
                    objectName: "deviceName/" + entry.modelData.id
                    anchors.verticalCenter: parent.verticalCenter
                    visible: entry.modelData.active
                    role: "label"
                    text: entry.modelData.label
                }
            }
        }
        Txt {
            objectName: "deviceMore"
            anchors.verticalCenter: parent.verticalCenter
            visible: strip.status.devicesMore > 0
            role: "label"
            muted: true
            text: "+" + strip.status.devicesMore
        }
    }
}
