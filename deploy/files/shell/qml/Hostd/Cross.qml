pragma ComponentBehavior: Bound
import QtQuick

// The cross: the categories on one row, the current one centred on the
// axis; under it, the current item's section, then its items in one column
// on the same axis, the current item at the top of the list. Passed items
// slide up and fade before the list's top: nothing enters the row band.
//
// Every place comes from Place and the shell's state, by arithmetic: the
// rows are plain positioners (not virtualizing views, which estimate and
// correct positions on their own), and moving is one animated x or y.
Item {
    id: cross

    // The shell (Python): what is current, the data, and what input does.
    required property var shell

    readonly property int step: Theme.layoutCategoryStep

    // The category row.
    Item {
        objectName: "rowBand"
        anchors.left: parent.left
        anchors.right: parent.right
        y: Place.rowTop
        height: Place.rowBottom - Place.rowTop

        Row {
            id: categories
            x: Place.axis - cross.step / 2 - cross.shell.category * cross.step
            Behavior on x { NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic } }

            Repeater {
                model: cross.shell.categories

                Item {
                    id: category
                    required property var modelData
                    required property int index
                    readonly property bool isCurrent: index === cross.shell.category
                    // Where its icon would be on screen once the row has
                    // settled: a category that would cross the safe margin is
                    // not shown at all, never cut by the screen's edge.
                    readonly property real iconX: Place.axis + (index - cross.shell.category) * cross.step
                                                  - Theme.layoutIconCategory / 2
                    readonly property bool fits: iconX >= Theme.layoutSafeMargin
                                                 && iconX + Theme.layoutIconCategory <= cross.width - Theme.layoutSafeMargin

                    objectName: "category/" + index
                    width: cross.step
                    height: Place.rowBottom - Place.rowTop
                    opacity: isCurrent ? 1 : fits ? 0.4 : 0
                    Behavior on opacity { NumberAnimation { duration: Theme.motionBase } }

                    Icon {
                        id: categoryIcon
                        objectName: "categoryIcon/" + category.index
                        anchors.horizontalCenter: parent.horizontalCenter
                        name: category.modelData.icon
                        size: Theme.layoutIconCategory
                    }

                    // Its space is always there; it shows on the current one only.
                    Txt {
                        objectName: "categoryName/" + category.index
                        anchors.horizontalCenter: categoryIcon.horizontalCenter
                        y: Theme.layoutIconCategory + Theme.spaceS
                        width: parent.width
                        height: Place.nameHeight
                        horizontalAlignment: Text.AlignHCenter
                        role: "title"
                        text: category.modelData.name
                        opacity: category.isCurrent ? 1 : 0
                        Behavior on opacity { NumberAnimation { duration: Theme.motionBase } }
                    }
                }
            }
        }
    }

    // The current item's section.
    Txt {
        objectName: "section"
        x: Place.text
        y: Place.sectionTop
        width: Place.columnWidth - (Place.text - Place.rowLeft)
        height: Theme.layoutSection
        role: "label"
        caps: true
        muted: true
        text: cross.shell.section
    }

    // The list: the current item at its top.
    Item {
        id: list
        objectName: "listArea"
        x: Place.rowLeft
        y: Place.listTop
        width: Place.columnWidth
        height: parent.height - Place.listTop - Place.bottomReserve
        clip: true

        // A new category's items appear in place, fading in; within one
        // category they slide.
        property int shown: cross.shell.category
        property bool sliding: true
        onShownChanged: { sliding = false; enter.restart(); Qt.callLater(() => { list.sliding = true }) }
        NumberAnimation { id: enter; target: rowsColumn; property: "opacity"; from: 0; to: 1; duration: Theme.motionBase }

        Column {
            id: rowsColumn
            width: parent.width
            y: -(cross.shell.headersBefore * Theme.layoutSection + cross.shell.rowsBefore * Theme.layoutItemHeight)
            Behavior on y {
                enabled: list.sliding
                NumberAnimation { duration: Theme.motionBase; easing.type: Easing.OutCubic }
            }

            Repeater {
                model: cross.shell.pageModels[cross.shell.category]

                ListRow {
                    id: item
                    required index
                    required title
                    required subtitle
                    required value
                    required badge
                    required tone
                    required mark
                    required kind
                    required icon
                    required appIcon
                    required app
                    required level
                    required mono

                    // How far above the list's top it is: passed items fade as they go.
                    readonly property real above: -(rowsColumn.y + y)

                    width: rowsColumn.width
                    current: index === cross.shell.current
                    opacity: (current ? 1 : 0.6) * Math.max(0, Math.min(1, 1 - above / Theme.layoutItemHeight))
                }
            }
        }
    }
}
