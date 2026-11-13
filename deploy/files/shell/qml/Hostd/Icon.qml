import QtQuick

// An icon: an app's own (app), else one of hostd's line icons (name) in
// tint, else a letter in a hairline square.
Item {
    id: icon

    property string name: ""
    property string app: ""
    property string letter: ""
    property color tint: Theme.colorText
    property int size: Theme.layoutIconItem

    readonly property bool hasApp: app !== "" // the shell passes only icons that exist

    implicitWidth: size
    implicitHeight: size

    Image {
        anchors.fill: parent
        visible: icon.hasApp || icon.name !== ""
        sourceSize: Qt.size(icon.size, icon.size)
        source: icon.hasApp ? "image://icons/app/" + icon.app
              : icon.name !== "" ? "image://icons/hostd/" + icon.name + "/" + icon.tint.toString().substring(1)
              : ""
        fillMode: Image.PreserveAspectFit
        smooth: true
        mipmap: true
    }

    Rectangle {
        anchors.fill: parent
        anchors.margins: icon.size / 8
        visible: !icon.hasApp && icon.name === ""
        color: "transparent"
        border.width: Theme.lineHair
        border.color: Theme.colorTextMuted

        Txt {
            anchors.centerIn: parent
            role: "label"
            font.pixelSize: Math.max(1, Math.round(icon.size * 0.4)) // in proportion to its square
            text: icon.letter.charAt(0).toUpperCase()
        }
    }
}
