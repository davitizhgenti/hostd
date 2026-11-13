pragma ComponentBehavior: Bound
import QtQuick

// A tab of tiles, as the PSP showed games: on the left, one column of wide
// tiles (a library header's shape) at the list block's left edge, grouped
// under their launchers; the current tile big at the list's top, the
// others small under it on the same axis. On the right, the current one's
// title and state, its banner (never stretched) and its details.
//
// A launcher (Steam) heads its group and looks unlike a game: no tile,
// its icon and its name in capitals, as a section label you can choose.
// Moving slides the column; passed rows fade before the list's top. Flat
// and square: hairline edges, no radius, no shadow.
Item {
    id: tiles
    objectName: "tilesArea"

    // The shell (Python): what is current, the data, and what input does.
    required property var shell

    readonly property int gap: Theme.spaceS
    readonly property int smallStep: Place.tileSmallHeight + gap

    x: Place.rowLeft
    y: Place.listTop
    width: Place.columnWidth
    clip: true

    // A new category's tiles appear in place, fading in; within one they slide.
    property int shown: tiles.shell.category
    property bool sliding: true
    onShownChanged: { sliding = false; enter.restart(); Qt.callLater(() => { tiles.sliding = true }) }
    NumberAnimation { id: enter; target: column; property: "opacity"; from: 0; to: 1; duration: Theme.motionBase }

    Column {
        id: column
        width: Place.tileWidth
        // Every game row before the current one is small; headings are
        // section-high (the shell counts them).
        y: -(tiles.shell.headersBefore * Theme.layoutSection + tiles.shell.rowsBefore * tiles.smallStep)
        Behavior on y {
            enabled: tiles.sliding
            NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic }
        }

        Repeater {
            model: tiles.shell.pageModels[tiles.shell.category]

            Item {
                id: item
                required property int index
                required property string title
                required property string kind
                required property string appIcon
                required property string icon
                required property string tile

                readonly property bool current: index === tiles.shell.current
                readonly property bool heading: kind === "header" || kind === "launcher"
                // How far above the area's top it is: passed rows fade as they go.
                readonly property real above: -(column.y + y)

                objectName: "tile/" + index
                width: column.width
                height: heading ? Theme.layoutSection
                      : (current ? Place.tileHeight : Place.tileSmallHeight) + tiles.gap
                opacity: (current || kind === "header" ? 1 : 0.6) * Math.max(0, Math.min(1, 1 - above / tiles.smallStep))
                Behavior on height { enabled: tiles.sliding; NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic } }

                // A group's heading: a label, or its launcher (an icon and its
                // name; current, underlined).
                Row {
                    objectName: "tileHead/" + item.index
                    visible: item.heading
                    height: Theme.layoutSection
                    spacing: Theme.spaceS

                    Icon {
                        anchors.verticalCenter: parent.verticalCenter
                        visible: item.kind === "launcher"
                        size: Theme.layoutIconSmall
                        name: item.appIcon === "" ? item.icon : ""
                        app: item.appIcon
                        letter: item.title
                    }
                    Txt {
                        id: headText
                        anchors.verticalCenter: parent.verticalCenter
                        role: "label"
                        caps: true
                        muted: !item.current
                        text: item.title
                        Rectangle {
                            objectName: "tileFocus/" + item.index
                            visible: item.current
                            anchors.top: parent.bottom
                            width: parent.width
                            height: Theme.lineFocus
                            color: Theme.colorAccent
                        }
                    }
                }

                // A game's tile: its picture, fitted to the tile's shape (a
                // library header fills it exactly; any other is cropped
                // centred, never stretched), or its icon on a plain tile.
                Rectangle {
                    objectName: "tileBox/" + item.index
                    visible: !item.heading
                    x: Math.round((Place.tileWidth - width) / 2)
                    width: item.current ? Place.tileWidth : Place.tileSmallWidth
                    height: item.current ? Place.tileHeight : Place.tileSmallHeight
                    color: Theme.colorSurface
                    border.width: Theme.lineHair
                    border.color: Theme.colorBorder
                    Behavior on width { enabled: tiles.sliding; NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic } }
                    Behavior on height { enabled: tiles.sliding; NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic } }

                    Icon {
                        anchors.centerIn: parent
                        visible: picture.status !== Image.Ready
                        size: Math.round(parent.height * 0.6)
                        name: item.appIcon === "" ? item.icon : ""
                        app: item.appIcon
                        letter: item.title
                    }

                    Image {
                        id: picture
                        objectName: "tilePicture/" + item.index
                        anchors.fill: parent
                        anchors.margins: Theme.lineHair
                        source: item.tile
                        fillMode: Image.PreserveAspectCrop
                        asynchronous: true
                        sourceSize: Qt.size(Place.tileWidth, Place.tileHeight)
                    }
                }
            }
        }
    }

    // The current row, on the right: its title and state, its banner, its
    // details. Always at the list's top, beside the current tile.
    Item {
        id: info
        objectName: "tilesInfo"
        readonly property var now: tiles.shell.focus
        x: Place.tileText - Place.rowLeft
        width: tiles.width - x
        height: tiles.height
        visible: info.now.title !== undefined

        Txt {
            id: infoTitle
            objectName: "tilesTitle"
            width: infoTrail.x - Theme.spaceL
            height: Theme.layoutItemLine
            role: "title"
            font.weight: Theme.weightLight
            text: info.now.title || ""
        }

        Row {
            id: infoTrail
            objectName: "tilesTrail"
            x: info.width - width
            height: Theme.layoutItemLine
            spacing: Theme.spaceXl

            Rectangle {
                anchors.verticalCenter: parent.verticalCenter
                visible: (info.now.mark || "") !== ""
                width: Theme.spaceS
                height: Theme.spaceS
                color: info.now.mark === "warning" ? Theme.colorWarning
                     : info.now.mark === "danger" ? Theme.colorDanger
                     : Theme.colorPositive
            }
            Badge {
                anchors.verticalCenter: parent.verticalCenter
                visible: (info.now.badge || "") !== ""
                text: info.now.badge || ""
                tone: info.now.tone || ""
            }
        }

        Txt {
            id: infoSubtitle
            objectName: "tilesSubtitle"
            y: Theme.layoutItemLine - Theme.spaceXs
            width: info.width
            role: "label"
            muted: true
            text: info.now.subtitle || ""
        }

        // The banner: as wide as the right side, as high as its own shape
        // makes it (never stretched); nothing when there is none.
        Image {
            id: banner
            objectName: "tilesBanner"
            y: infoSubtitle.y + infoSubtitle.height + Theme.spaceM
            width: info.width
            height: status === Image.Ready && implicitWidth > 0 ? Math.round(width * implicitHeight / implicitWidth) : 0
            source: info.now.banner || ""
            fillMode: Image.PreserveAspectFit
            asynchronous: true
            sourceSize.width: info.width
            visible: height > 0
        }

        Txt {
            objectName: "tilesDetails"
            y: banner.y + banner.height + (banner.height > 0 ? Theme.spaceM : 0)
            width: info.width
            role: "label"
            muted: true
            wrapMode: Text.WordWrap
            maximumLineCount: 3
            elide: Text.ElideRight
            text: info.now.details || ""
        }
    }
}
