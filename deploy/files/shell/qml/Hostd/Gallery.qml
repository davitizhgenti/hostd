pragma ComponentBehavior: Bound
import QtQuick

// The component sheet: every token and control in its states, on one
// screen. `hostd-shell --gallery` shows it; the design review starts here.
Window {
    id: gallery

    // The shell (Python): what is current, the data, and what input does.
    required property var shell

    title: "hostd components"
    color: Theme.colorBg
    visible: true

    readonly property var colorNames: ["bg", "surface", "surfaceRaised", "border", "text", "textMuted",
                                       "accent", "positive", "warning", "danger"]
    readonly property var iconNames: ["layout-grid", "users", "sliders-horizontal", "activity", "app-window",
                                      "play", "arrow-up-right", "x", "user", "info", "volume-2", "volume-x",
                                      "speaker", "monitor", "power", "palette", "image", "history", "memory-stick"]

    Backdrop { anchors.fill: parent; shell: gallery.shell }

    Row {
        anchors.fill: parent
        anchors.margins: Theme.layoutSafeMargin
        spacing: Theme.spaceXxxl

        Column {
            width: (parent.width - Theme.spaceXxxl) * 0.42
            spacing: Theme.spaceXl

            Txt { role: "label"; caps: true; muted: true; text: "Type" }
            Column {
                spacing: Theme.spaceS
                Txt { role: "display"; text: "21:40 Display" }
                Txt { role: "title"; text: "Title: the current item" }
                Txt { text: "Body: items, options, notices" }
                Txt { role: "label"; muted: true; text: "Label: second lines, hints, states" }
                Txt { role: "label"; mono: true; muted: true; text: "steam-570  (an id, monospace)" }
            }

            Txt { role: "label"; caps: true; muted: true; text: "Color" }
            Flow {
                width: parent.width
                spacing: Theme.spaceM
                Repeater {
                    model: gallery.colorNames
                    Column {
                        id: swatch
                        required property string modelData
                        spacing: Theme.spaceXs
                        Rectangle {
                            width: Theme.layoutIconCategory * 1.5
                            height: Theme.layoutIconCategory
                            color: Theme["color" + swatch.modelData.charAt(0).toUpperCase() + swatch.modelData.slice(1)]
                            border.width: Theme.lineHair
                            border.color: Theme.colorBorder
                        }
                        Txt { role: "label"; muted: true; text: swatch.modelData }
                    }
                }
            }

            Txt { role: "label"; caps: true; muted: true; text: "Icons" }
            Flow {
                width: parent.width
                spacing: Theme.spaceL
                Repeater {
                    model: gallery.iconNames
                    Icon { required property string modelData; name: modelData }
                }
                Icon { letter: "Kodi" }
            }

            Txt { role: "label"; caps: true; muted: true; text: "States, buttons, levels" }
            Row {
                spacing: Theme.spaceXl
                Badge { text: "running"; tone: "positive" }
                Badge { text: "starting"; tone: "warning" }
                Badge { text: "failed"; tone: "danger" }
                Badge { text: "on screen"; tone: "accent" }
                Badge { text: "plain" }
            }
            Row {
                spacing: Theme.spaceXxl
                Button { text: "No"; current: true }
                Button { text: "Yes" }
            }
            Level { width: parent.width * 0.6; value: 70; maximum: 150 }
        }

        Column {
            width: (parent.width - Theme.spaceXxxl) * 0.58
            spacing: Theme.spaceS

            Txt { role: "label"; caps: true; muted: true; text: "Rows" }
            ListRow { width: parent.width; kind: "header"; title: "Running" }
            ListRow { width: parent.width; title: "Steam"; subtitle: "steam"; mono: true; badge: "running"; tone: "positive"; app: "steam"; current: true; value: "1 action  2 apps" }
            ListRow { width: parent.width; title: "Chromium"; badge: "starting"; tone: "warning"; app: "chromium"; opacity: 0.6 }
            ListRow { width: parent.width; title: "Volume"; icon: "volume-2"; kind: "slider"; level: 70; value: "70%"; current: true }
            ListRow { width: parent.width; title: "Close"; icon: "x"; tone: "danger"; compact: true }
            ListRow { width: parent.width; title: "Bring to front"; icon: "arrow-up-right"; compact: true; current: true }

            Txt { role: "label"; caps: true; muted: true; text: "Notices and the OSD" }
            Notice { app: "Steam"; summary: "Download finished"; body: "Dota 2 is ready to play" }
            Osd { name: "Volume"; value: "70%"; level: 70 }
        }
    }
}
