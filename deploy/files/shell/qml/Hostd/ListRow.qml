import QtQuick

// The one list item. Its parts sit at fixed places, the same for every
// row, current or not (Place):
//   the icon column  `column` wide, the icon centred in it
//   the title line   the top itemLine of the row: icon, title and trail
//                    all centred on it
//   the second line  under the title line, on the current row only
// Every row is itemHeight high, so the rhythm never changes.
// The current row is shown by its title's size and full opacity alone:
// nothing moves sideways and nothing is scaled. A header is a section label
// at the text column.
Item {
    id: row

    property string title: ""
    property string subtitle: ""
    property string value: ""
    property string badge: ""
    property string tone: ""
    property string mark: "" // a running app: a small square in this tone
    property string kind: "item"
    property string icon: ""
    property string appIcon: ""
    property string app: ""
    property int level: 0
    property bool mono: false
    property bool current: false
    property bool compact: false // in the options panel: one size, no second line
    property int column: Place.iconColumn
    property int index: -1
    property string tag: "item" // the tests' name for its parts: item, option

    readonly property bool header: kind === "header"
    readonly property bool big: current && !compact
    readonly property int line: Theme.layoutItemLine
    readonly property int textX: column + Theme.spaceL

    objectName: tag + "/" + index
    implicitHeight: header ? Theme.layoutSection : Theme.layoutItemHeight

    Txt {
        objectName: row.tag + "Section/" + row.index
        visible: row.header
        x: row.textX
        width: row.width - row.textX
        height: Theme.layoutSection
        role: "label"
        caps: true
        muted: true
        text: row.title
    }

    Icon {
        objectName: row.tag + "Icon/" + row.index
        visible: !row.header
        size: row.compact ? Theme.layoutIconSmall : Theme.layoutIconItem
        x: (row.column - size) / 2
        y: (row.line - size) / 2
        name: row.app === "" ? row.icon : ""
        app: row.appIcon
        letter: row.title
    }

    Txt {
        objectName: row.tag + "Title/" + row.index
        visible: !row.header
        x: row.textX
        width: trail.x - row.textX - Theme.spaceL
        height: row.line
        role: row.big ? "title" : "body"
        font.weight: row.current && row.compact ? Theme.weightStrong : (row.big ? Theme.weightLight : Theme.weightRegular)
        text: row.title
        color: row.tone === "danger" && row.badge === "" ? Theme.colorDanger : Theme.colorText
    }

    Txt {
        objectName: row.tag + "Subtitle/" + row.index
        visible: row.big && row.subtitle !== ""
        x: row.textX
        y: row.line - Theme.spaceXs
        width: row.width - row.textX
        role: "label"
        muted: true
        mono: row.mono
        text: row.subtitle
    }

    Row {
        id: trail
        objectName: row.tag + "Trail/" + row.index
        visible: !row.header
        x: row.width - width
        height: row.line
        spacing: Theme.spaceXl

        Rectangle {
            objectName: row.tag + "Mark/" + row.index
            anchors.verticalCenter: parent.verticalCenter
            visible: row.mark !== ""
            width: Theme.spaceS
            height: Theme.spaceS
            color: row.mark === "warning" ? Theme.colorWarning
                 : row.mark === "danger" ? Theme.colorDanger
                 : Theme.colorPositive
        }

        Badge {
            anchors.verticalCenter: parent.verticalCenter
            visible: row.badge !== ""
            text: row.badge
            tone: row.tone
        }

        Level {
            anchors.verticalCenter: parent.verticalCenter
            visible: row.kind === "slider" && row.big
            value: row.level
            maximum: 150
        }

        Txt {
            anchors.verticalCenter: parent.verticalCenter
            visible: row.value !== "" && (row.big || row.compact)
            muted: true
            text: row.value
        }
    }
}
