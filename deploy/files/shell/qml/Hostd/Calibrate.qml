import QtQuick
import Hostd.Shell as Core

// Calibrating a Wii Remote: the screen dimmed, a marker at the corner to
// point at (in the remote's color), and what to do. A at the marker keeps it;
// four corners measure the screen as the remote sees it. B gives up.
Rectangle {
    id: calibrate
    objectName: "calibrate"

    required property Core.Pointers pointers
    readonly property var now: pointers.calibrating
    readonly property int step: now.step === undefined ? 0 : now.step
    readonly property color tint: Devices.colour(now.colour)
    // The markers sit inside the safe margin, where a remote can reach.
    readonly property real inset: Theme.layoutSafeMargin * 2

    visible: now.corner !== undefined && now.corner !== ""
    color: Theme.colorBg
    opacity: visible ? 0.92 : 0
    z: 900

    // The marker: a square with a cross through it, flat and hairline.
    Item {
        id: marker
        objectName: "calibrateMarker"
        readonly property bool atRight: calibrate.step % 2 === 1
        readonly property bool atBottom: calibrate.step >= 2
        width: Theme.layoutPointer * 4
        height: width
        x: atRight ? calibrate.width - calibrate.inset - width : calibrate.inset
        y: atBottom ? calibrate.height - calibrate.inset - height : calibrate.inset

        Rectangle { anchors.fill: parent; color: "transparent"; border.width: Theme.lineFocus; border.color: calibrate.tint }
        Rectangle { x: 0; y: parent.height / 2; width: parent.width; height: Theme.lineHair; color: calibrate.tint }
        Rectangle { x: parent.width / 2; y: 0; width: Theme.lineHair; height: parent.height; color: calibrate.tint }
    }

    Column {
        anchors.centerIn: parent
        spacing: Theme.spaceM

        Txt {
            objectName: "calibrateTitle"
            anchors.horizontalCenter: parent.horizontalCenter
            role: "title"
            font.weight: Theme.weightLight
            text: "Calibrate " + (calibrate.now.name || "the remote")
        }
        Txt {
            objectName: "calibrateWhat"
            anchors.horizontalCenter: parent.horizontalCenter
            role: "body"
            text: "Point at the marker in the " + (calibrate.now.corner || "") + " corner and press A"
        }
        Txt {
            anchors.horizontalCenter: parent.horizontalCenter
            role: "label"
            muted: true
            text: (calibrate.step + 1) + " of 4   ·   B to cancel"
        }
        Txt {
            objectName: "calibrateReason"
            anchors.horizontalCenter: parent.horizontalCenter
            role: "label"
            visible: text !== ""
            color: Theme.colorWarning
            text: calibrate.now.reason || ""
        }
    }
}
