import QtQuick

// The time, the date, and the category's summary, right-aligned at the top
// right. The time comes from the shell, so tests can set it.
Column {
    id: clock

    // The shell (Python): what is current, the data, and what input does.
    required property var shell
    objectName: "clock"
    spacing: Theme.spaceXs

    Txt {
        anchors.right: parent.right
        role: "title"
        text: clock.shell.clockTime
    }

    Txt {
        anchors.right: parent.right
        role: "label"
        muted: true
        text: clock.shell.clockDate
    }

    Txt {
        anchors.right: parent.right
        role: "label"
        muted: true
        text: clock.shell.summary
        visible: text !== ""
    }
}
