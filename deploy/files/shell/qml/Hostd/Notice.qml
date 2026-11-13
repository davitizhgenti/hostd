import QtQuick

// A notice: the app, a summary and a body, on a flat panel with a thin
// edge in its urgency's color.
Rectangle {
    id: notice

    property string app: ""
    property string summary: ""
    property string body: ""
    property string tone: ""

    implicitWidth: Theme.layoutNoticeWidth
    implicitHeight: column.implicitHeight + 2 * Theme.spaceL
    color: Theme.colorSurfaceRaised

    Rectangle {
        width: Theme.lineFocus
        height: parent.height
        color: notice.tone === "danger" ? Theme.colorDanger
             : notice.tone === "low" ? Theme.colorBorder
             : Theme.colorAccent
    }

    Column {
        id: column
        x: Theme.spaceL + Theme.lineFocus
        y: Theme.spaceL
        width: parent.width - 2 * Theme.spaceL - Theme.lineFocus
        spacing: Theme.spaceXs

        Txt { width: parent.width; role: "label"; muted: true; text: notice.app; visible: text !== "" }
        Txt { width: parent.width; text: notice.summary; font.weight: Theme.weightStrong }
        Txt {
            width: parent.width
            role: "label"
            muted: true
            text: notice.body
            visible: text !== ""
            wrapMode: Text.Wrap
            maximumLineCount: 3
        }
    }
}
