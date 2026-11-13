pragma ComponentBehavior: Bound
import QtQuick

// The options panel, at the right edge (the PSP's triangle menu): the
// current item's options, or a level (the volume) changed with up and
// down. Its heading and list line up with the menu's section and list.
Rectangle {
    id: panel

    // The shell (Python): what is current, the data, and what input does.
    required property var shell

    readonly property bool open: panel.shell.panel !== ""
    readonly property int inset: Theme.spaceXxl

    objectName: "panel"
    width: Theme.layoutPanelWidth
    height: parent.height
    x: open ? parent.width - width : parent.width
    Behavior on x { NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic } }
    color: Theme.colorSurface

    Rectangle {
        width: Theme.lineHair
        height: parent.height
        color: Theme.colorBorder
    }

    Txt {
        objectName: "panelTitle"
        x: panel.inset
        y: Place.sectionTop
        width: parent.width - 2 * panel.inset
        height: Theme.layoutSection
        role: "label"
        caps: true
        muted: true
        text: panel.shell.panelTitle
    }

    ListView {
        id: options
        objectName: "panelList"
        visible: panel.shell.panel === "options"
        x: panel.inset
        y: Place.listTop
        width: parent.width - 2 * panel.inset
        height: parent.height - Place.listTop - Place.bottomReserve
        clip: true
        interactive: false
        model: panel.shell.options
        currentIndex: panel.shell.option
        highlightMoveDuration: Theme.motionBase
        highlightMoveVelocity: -1

        delegate: ListRow {
            id: option
            required index
            required title
            required badge
            required tone
            required icon
            required appIcon
            required app

            width: options.width
            pointing: panel.shell.pointer.visible
            onActivated: panel.shell.clickOption(option.index)
            column: Theme.layoutIconItem
            tag: "option"
            compact: true
            current: index === panel.shell.option
            opacity: current ? 1 : 0.55
            Behavior on opacity { NumberAnimation { duration: Theme.motionFast } }
        }
    }

    // A level: its value, and an upright bar filling the list's height.
    Column {
        id: levelColumn
        objectName: "panelLevel"
        visible: panel.shell.panel === "level"
        x: panel.inset
        y: Place.listTop
        height: panel.height - Place.listTop - Place.bottomReserve
        spacing: Theme.spaceXl

        Txt {
            id: levelValue
            role: "display"
            text: panel.shell.levelValue
        }

        Level {
            vertical: true
            value: panel.shell.levelLevel
            maximum: 150
            height: levelColumn.height - levelValue.height - levelColumn.spacing
        }
    }
}
