import QtQuick
import org.kde.layershell 1.0 as LayerShell

// The OSD at the bottom centre: the volume as it changes, whose screen it is.
Window {
    id: osdLayer

    // The shell (Python): what is current, the data, and what input does.
    required property var shell
    width: Theme.layoutOsdWidth
    height: Math.max(1, osd.implicitHeight + Theme.layoutSafeMargin * 2)
    color: "transparent"
    flags: Qt.WindowTransparentForInput
    visible: osdLayer.shell.osdVisible

    // org.kde.layershell ships no type information: qmllint cannot check these.
    // qmllint disable unresolved-type unqualified missing-property
    LayerShell.Window.layer: LayerShell.Window.LayerOverlay
    LayerShell.Window.anchors: LayerShell.Window.AnchorBottom
    LayerShell.Window.keyboardInteractivity: LayerShell.Window.KeyboardInteractivityNone
    LayerShell.Window.exclusionZone: -1
    LayerShell.Window.scope: "hostd-osd"
    // qmllint enable unresolved-type unqualified missing-property

    Osd {
        id: osd
        width: parent.width
        name: osdLayer.shell.osdName
        value: osdLayer.shell.osdValue
        level: osdLayer.shell.osdLevel
    }
}
