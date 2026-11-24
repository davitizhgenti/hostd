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

    // The tiles view: a wall. Games are tiles in a library header's shape
    // (460:215), tileColumns across, in cells one tile and one gap wide;
    // a section label or a launcher is a line of its own. The wall starts at
    // the block's left edge; under it, kept clear of the hints, the current
    // tile's title and details (infoHeight high).
    readonly property int tileColumns: 4 // core/grid.py's COLUMNS
    readonly property int tileGap: Theme.spaceM
    readonly property real tileShape: 215 / 460
    readonly property int cellWidth: Math.floor(columnWidth / tileColumns)
    readonly property int tileWidth: cellWidth - tileGap
    readonly property int tileHeight: Math.round(tileWidth * tileShape)
    readonly property int cellHeight: tileHeight + tileGap
    readonly property int infoHeight: Theme.layoutItemLine + Math.round(Theme.typeLabel * 1.5) + Theme.spaceM

    // The status strip: services and devices at the top left, inside the
    // safe margin, never wider than stripWidth (so never over the clock).
    readonly property int stripLeft: Theme.layoutSafeMargin
    readonly property int stripTop: Theme.layoutSafeMargin
    readonly property int stripWidth: Theme.layoutStripWidth
    readonly property int stripMark: Theme.spaceS * 2 // a service's or device's square
    readonly property int stripMax: 6 // squares per line before "+N"

    // Kept clear at the bottom for the hints and the message.
    readonly property int bottomReserve: Theme.layoutSafeMargin + Math.round(Theme.typeLabel * 1.5) + Theme.spaceL
}
