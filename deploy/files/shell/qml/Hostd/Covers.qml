pragma ComponentBehavior: Bound
import QtQuick

// A tab of covers, as the PSP showed games: one column of covers at the
// list block's left edge, the current one big at the list's top with its
// title, where it is from and its state beside it; the others small under
// it, on the same axis. Moving slides the column; passed covers fade
// before the list's top, as rows do. Flat and square: a hairline edge, no
// radius, no shadow. An app with no cover shows its icon on a plain tile.
Item {
    id: covers
    objectName: "coversArea"

    // The shell (Python): what is current, the data, and what input does.
    required property var shell

    readonly property int gap: Theme.spaceS
    readonly property int smallStep: Place.coverSmallHeight + gap

    x: Place.rowLeft
    y: Place.listTop
    width: Place.columnWidth
    clip: true

    // A new category's covers appear in place, fading in; within one they slide.
    property int shown: covers.shell.category
    property bool sliding: true
    onShownChanged: { sliding = false; enter.restart(); Qt.callLater(() => { covers.sliding = true }) }
    NumberAnimation { id: enter; target: column; property: "opacity"; from: 0; to: 1; duration: Theme.motionBase }

    Column {
        id: column
        width: parent.width
        spacing: 0
        // Every cover before the current one is small.
        y: -Math.max(0, covers.shell.current) * covers.smallStep
        Behavior on y {
            enabled: covers.sliding
            NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic }
        }

        Repeater {
            model: covers.shell.pageModels[covers.shell.category]

            Item {
                id: item
                required property int index
                required property string title
                required property string subtitle
                required property string badge
                required property string tone
                required property string mark
                required property string appIcon
                required property string cover
                required property string icon

                readonly property bool current: index === covers.shell.current
                readonly property int tileHeight: current ? Place.coverHeight : Place.coverSmallHeight
                readonly property int tileWidth: current ? Place.coverWidth : Place.coverSmallWidth
                // How far above the area's top it is: passed covers fade as they go.
                readonly property real above: -(column.y + y)

                objectName: "cover/" + index
                width: column.width
                height: tileHeight + covers.gap
                opacity: (current ? 1 : 0.6) * Math.max(0, Math.min(1, 1 - above / covers.smallStep))
                Behavior on height { enabled: covers.sliding; NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic } }

                Rectangle {
                    id: tile
                    objectName: "coverTile/" + item.index
                    x: Math.round((Place.coverWidth - width) / 2)
                    width: item.tileWidth
                    height: item.tileHeight
                    color: Theme.colorSurface
                    border.width: Theme.lineHair
                    border.color: Theme.colorBorder
                    Behavior on width { enabled: covers.sliding; NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic } }
                    Behavior on height { enabled: covers.sliding; NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic } }

                    Icon {
                        anchors.centerIn: parent
                        visible: picture.status !== Image.Ready
                        size: Math.round(parent.width / 2)
                        name: item.appIcon === "" ? item.icon : ""
                        app: item.appIcon
                        letter: item.title
                    }

                    Image {
                        id: picture
                        objectName: "coverImage/" + item.index
                        anchors.fill: parent
                        anchors.margins: Theme.lineHair
                        source: item.cover
                        fillMode: Image.PreserveAspectCrop
                        asynchronous: true
                        sourceSize: Qt.size(Place.coverWidth, Place.coverHeight)
                    }
                }

                // The current cover's words, on the text column beside it.
                Txt {
                    id: name
                    objectName: "coverTitle/" + item.index
                    visible: item.current
                    x: Place.coverText - Place.rowLeft
                    width: trail.x - x - Theme.spaceL
                    height: Theme.layoutItemLine
                    role: "title"
                    font.weight: Theme.weightLight
                    text: item.title
                }

                Txt {
                    objectName: "coverSubtitle/" + item.index
                    visible: item.current && item.subtitle !== ""
                    x: name.x
                    y: Theme.layoutItemLine - Theme.spaceXs
                    width: column.width - x
                    role: "label"
                    muted: true
                    text: item.subtitle
                }

                Row {
                    id: trail
                    objectName: "coverTrail/" + item.index
                    visible: item.current
                    x: column.width - width
                    height: Theme.layoutItemLine
                    spacing: Theme.spaceXl

                    Rectangle {
                        anchors.verticalCenter: parent.verticalCenter
                        visible: item.mark !== ""
                        width: Theme.spaceS
                        height: Theme.spaceS
                        color: item.mark === "warning" ? Theme.colorWarning
                             : item.mark === "danger" ? Theme.colorDanger
                             : Theme.colorPositive
                    }

                    Badge {
                        anchors.verticalCenter: parent.verticalCenter
                        visible: item.badge !== ""
                        text: item.badge
                        tone: item.tone
                    }
                }
            }
        }
    }
}
