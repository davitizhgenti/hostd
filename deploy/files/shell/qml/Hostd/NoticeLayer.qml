pragma ComponentBehavior: Bound
import QtQuick
import Hostd.Shell as Core
import org.kde.layershell 1.0 as LayerShell

// Notices at the top right, over everything (fullscreen games too), under
// where the menu has its clock. Takes no keys and no clicks.
Window {
    id: layer

    required property Core.Notices queue

    readonly property int margin: Theme.layoutSafeMargin

    width: Theme.layoutNoticeWidth + margin
    height: Math.max(1, stack.implicitHeight + margin + clockRoom)
    readonly property int clockRoom: Theme.typeTitle * 2 + Theme.typeLabel * 3
    color: "transparent"
    flags: Qt.WindowTransparentForInput
    visible: notices.count > 0

    // org.kde.layershell ships no type information: qmllint cannot check these.
    // qmllint disable unresolved-type unqualified missing-property
    LayerShell.Window.layer: LayerShell.Window.LayerOverlay
    LayerShell.Window.anchors: LayerShell.Window.AnchorTop | LayerShell.Window.AnchorRight
    LayerShell.Window.keyboardInteractivity: LayerShell.Window.KeyboardInteractivityNone
    LayerShell.Window.exclusionZone: -1
    LayerShell.Window.scope: "hostd-notices"
    // qmllint enable unresolved-type unqualified missing-property

    Column {
        id: stack
        y: layer.margin + layer.clockRoom
        width: Theme.layoutNoticeWidth
        spacing: Theme.spaceM

        Repeater {
            id: notices
            model: layer.queue.model

            Notice {
                required property string title
                required property string subtitle
                required property string value
                required tone

                width: stack.width
                app: value
                summary: title
                body: subtitle
            }
        }
    }
}
