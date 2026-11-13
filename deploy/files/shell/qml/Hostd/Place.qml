pragma Singleton
import QtQuick

// The layout contract ("Place", as QtQuick has its own Grid): every position on the menu comes from here, and the
// layout tests check the screens against it (docs/design.md, "The grid").
//
//   axis       a vertical line through the cross point. The current
//              category's icon, every item icon and its column are centred
//              on it.
//   text       where every title, second line and section label starts.
//   row band   crossY .. rowBottom: the category icons and the current
//              category's name. Nothing from the list enters it.
//   section    the current item's section ("RUNNING"), between the band
//              and the list.
//   list       listTop .. the bottom margin, clipped: the current item at
//              its top, the rest below it.
QtObject {
    readonly property int axis: Theme.layoutCrossX
    readonly property int iconColumn: Theme.layoutIconCategory // as wide as a category icon
    readonly property int rowLeft: axis - iconColumn / 2
    readonly property int text: rowLeft + iconColumn + Theme.spaceL
    readonly property int trailEnd: rowLeft + Theme.layoutColumnWidth
    readonly property int columnWidth: Theme.layoutColumnWidth

    readonly property int rowTop: Theme.layoutCrossY
    readonly property int nameHeight: Math.round(Theme.typeTitle * 1.35)
    readonly property int rowBottom: rowTop + Theme.layoutIconCategory + Theme.spaceS + nameHeight

    readonly property int sectionTop: rowBottom + Theme.spaceL
    readonly property int listTop: sectionTop + Theme.layoutSection

    // Kept clear at the bottom for the hints and the message.
    readonly property int bottomReserve: Theme.layoutSafeMargin + Math.round(Theme.typeLabel * 1.5) + Theme.spaceL
}
