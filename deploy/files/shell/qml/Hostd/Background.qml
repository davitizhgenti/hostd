import QtQuick
import org.kde.layershell 1.0 as LayerShell

// The screen behind every app: the background setting, on the compositor's
// background layer (in place of swaybg).
Window {
    id: background

    // The shell (Python): what is current, the data, and what input does.
    required property var shell
    color: Theme.colorBg
    flags: Qt.WindowTransparentForInput

    // org.kde.layershell ships no type information: qmllint cannot check these.
    // qmllint disable unresolved-type unqualified missing-property
    LayerShell.Window.layer: LayerShell.Window.LayerBackground
    LayerShell.Window.anchors: LayerShell.Window.AnchorTop | LayerShell.Window.AnchorBottom
                             | LayerShell.Window.AnchorLeft | LayerShell.Window.AnchorRight
    LayerShell.Window.exclusionZone: -1
    LayerShell.Window.keyboardInteractivity: LayerShell.Window.KeyboardInteractivityNone
    LayerShell.Window.scope: "hostd-background"
    // qmllint enable unresolved-type unqualified missing-property

    Backdrop {
        shell: background.shell
        anchors.fill: parent
    }

    Component.onCompleted: visible = true
}
