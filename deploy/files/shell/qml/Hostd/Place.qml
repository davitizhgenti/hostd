pragma Singleton
import QtQuick

// The layout contract ("Place", as QtQuick has its own Grid): every position on the menu comes from here, and the
// layout tests check the screens against it (docs/design.md, "The grid").
//
//   axis       the screen's vertical centre line. The current category's
//              icon sits on it, the other categories fan out either side,
//              and the list is a block centred on it.
//   block      the list: columnWidth wide, centred on the axis. Item icons
//              are centred in the icon column at its left edge; states and
//              values end at its right edge.
//   text       where every title, second line and section label starts.
//   row band   crossY .. rowBottom: the category icons and the current
//              category's name. Nothing from the list enters it.
//   section    the current item's section ("RUNNING"), between the band
//              and the list.
//   list       listTop .. the bottom margin, clipped: the current item at
//              its top, the rest below it.
QtObject {
    readonly property int axis: Math.round(Theme.screenWidth / 2)
    readonly property int columnWidth: Theme.layoutColumnWidth
    readonly property int iconColumn: Theme.layoutIconCategory // as wide as a category icon
    readonly property int rowLeft: axis - Math.round(columnWidth / 2)
    readonly property int iconAxis: rowLeft + iconColumn / 2 // where every item icon is centred
    readonly property int text: rowLeft + iconColumn + Theme.spaceL
    readonly property int trailEnd: rowLeft + columnWidth

    readonly property int rowTop: Theme.layoutCrossY
    readonly property int nameHeight: Math.round(Theme.typeTitle * 1.35)
    readonly property int rowBottom: rowTop + Theme.layoutIconCategory + Theme.spaceS + nameHeight

    readonly property int sectionTop: rowBottom + Theme.spaceL
    readonly property int listTop: sectionTop + Theme.layoutSection

    // The tiles view: a column of wide tiles (a library header's shape,
    // 460:215) at the block's left edge, the current one big, the others
    // small under it, all centred on tileAxis; the current one's title,
    // banner and details on the right, from tileText to the block's edge.
    readonly property real tileShape: 215 / 460
    readonly property int tileWidth: Theme.layoutTile
    readonly property int tileHeight: Math.round(tileWidth * tileShape)
    readonly property int tileSmallWidth: Theme.layoutTileSmall
    readonly property int tileSmallHeight: Math.round(tileSmallWidth * tileShape)
    readonly property int tileAxis: rowLeft + Math.round(tileWidth / 2)
    readonly property int tileText: rowLeft + tileWidth + Theme.spaceL

    // Kept clear at the bottom for the hints and the message.
    readonly property int bottomReserve: Theme.layoutSafeMargin + Math.round(Theme.typeLabel * 1.5) + Theme.spaceL
}
