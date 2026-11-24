import QtQuick
import Hostd.Shell as Core

// The time, the date, and the category's summary, right-aligned at the top
// right. The time comes from the shell, so tests can set it.
Column {
    id: clock

    required property Core.Clock ticker
    required property Core.Menu menu
    objectName: "clock"
    spacing: Theme.spaceXs

    Txt {
        anchors.right: parent.right
        role: "title"
        text: clock.ticker.time
    }

    Txt {
        anchors.right: parent.right
        role: "label"
        muted: true
        text: clock.ticker.date
    }

    Txt {
        anchors.right: parent.right
        role: "label"
        muted: true
        text: clock.menu.summary
        visible: text !== ""
    }
}
