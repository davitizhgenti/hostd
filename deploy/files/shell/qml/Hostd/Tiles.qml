pragma ComponentBehavior: Bound
import QtQuick
import Hostd.Shell as Core

// A tab of tiles, as a wall: games are tiles in a library header's shape,
// tileColumns across from the list block's left edge, in groups under
// their launchers. Under the wall, the current tile's title, state and
// details. The current tile has a square frame; the others are dimmed.
//
// A launcher (Steam) heads its group and looks unlike a game: no tile,
// its icon and its name in capitals, as a section label you can choose.
// Up and down move by line, left and right along it (the shell decides;
// this only draws). The wall scrolls by whole lines to put the current
// line at the top; passed lines fade before it. Flat and square: hairline
// edges, no radius, no shadow.
Item {
    id: tiles
    objectName: "tilesArea"

    required property Core.Menu menu
    required property Core.Pointers pointers

    x: Place.rowLeft
    y: Place.listTop
    width: Place.columnWidth
    clip: true

    // A new category's tiles appear in place, fading in; within one they slide.
    property int shown: tiles.menu.category
    property bool sliding: true
    onShownChanged: { sliding = false; enter.restart(); Qt.callLater(() => { tiles.sliding = true }) }
    NumberAnimation { id: enter; target: wall; property: "opacity"; from: 0; to: 1; duration: Theme.motionBase }

    // The wall's own room: what is left above the current tile's details.
    Item {
        id: room
        width: parent.width
        height: tiles.height - Place.infoHeight
        clip: true

        Flow {
            id: wall
            width: Place.columnWidth
            // Headings are section-high, every tile line a cell high (the shell counts them).
            y: -(tiles.menu.headersBefore * Theme.layoutSection + tiles.menu.rowsBefore * Place.cellHeight)
            Behavior on y {
                enabled: tiles.sliding
                NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic }
            }

            Repeater {
                model: tiles.menu.pageModels[tiles.menu.category]

                Item {
                    id: item
                    required property int index
                    required property string title
                    required property string kind
                    required property string appIcon
                    required property string icon
                    required property string tile

                    readonly property bool current: index === tiles.menu.current
                    readonly property bool heading: kind === "header" || kind === "launcher"
                    // How far above the room's top it is: passed lines fade as they go.
                    readonly property real above: -(wall.y + y)

                    objectName: "tile/" + index
                    width: heading ? Place.columnWidth : Place.cellWidth
                    height: heading ? Theme.layoutSection : Place.cellHeight
                    // A line that would be cut by the wall's foot is not shown: no slivers.
                    readonly property bool fits: wall.y + y + height <= room.height + 1
                    opacity: (current || kind === "header" ? 1 : 0.6) * Math.max(0, Math.min(1, 1 - above / Place.cellHeight)) * (fits ? 1 : 0)
                    Behavior on opacity { NumberAnimation { duration: Theme.motionFast } }

                    MouseArea {
                        objectName: "tileHit/" + item.index
                        anchors.fill: parent
                        hoverEnabled: true
                        cursorShape: Qt.BlankCursor
                        onClicked: tiles.menu.clickItem(item.index, "left")
                        onEntered: tiles.pointers.hover(item.index)
                        onExited: tiles.pointers.hover(-1)
                    }

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
                        width: Place.tileWidth
                        height: Place.tileHeight
                        color: Theme.colorSurface
                        border.width: Theme.lineHair
                        border.color: Theme.colorBorder

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

                        // The current tile's frame: square, inside its edge.
                        Rectangle {
                            objectName: "tileFrame/" + item.index
                            visible: item.current
                            anchors.fill: parent
                            color: "transparent"
                            border.width: Theme.lineFocus
                            border.color: Theme.colorAccent
                        }
                    }
                }
            }
        }
    }

    // The current row, under the wall: its title and state, then its
    // second line and details on one line.
    Item {
        id: info
        objectName: "tilesInfo"
        readonly property var now: tiles.menu.focus
        y: tiles.height - Place.infoHeight
        width: tiles.width
        height: Place.infoHeight
        visible: info.now.title !== undefined

        Rectangle {  // the wall's foot
            width: parent.width
            height: Theme.lineHair
            color: Theme.colorBorder
        }

        Txt {
            id: infoTitle
            objectName: "tilesTitle"
            y: Theme.spaceM
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
            y: Theme.spaceM
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
            objectName: "tilesLine"
            y: Theme.spaceM + Theme.layoutItemLine - Theme.spaceXs
            width: info.width
            role: "label"
            muted: true
            elide: Text.ElideRight
            text: [info.now.subtitle || "", info.now.details || ""].filter(t => t !== "").join("  ·  ")
        }
    }
}
