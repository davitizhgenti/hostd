import QtQuick

// The background: the user's setting (the shell's bg*), a gradient, or an image
// darkened so text reads on it. The same behind the apps and the menu.
Rectangle {
    id: backdrop

    // The shell (Python): what is current, the data, and what input does.
    required property var shell
    // The current app's own picture over it (the menu; not behind the apps).
    property bool showArt: false
    color: Theme.colorBg

    gradient: Gradient {
        GradientStop {
            position: 0
            color: backdrop.shell.bgTop !== "" ? backdrop.shell.bgTop : Theme.colorBg
            Behavior on color { ColorAnimation { duration: 1000 } }
        }
        GradientStop {
            position: 1
            color: backdrop.shell.bgBottom !== "" ? backdrop.shell.bgBottom : Theme.colorBg
            Behavior on color { ColorAnimation { duration: 1000 } }
        }
    }

    Image {
        anchors.fill: parent
        visible: backdrop.shell.bgImage !== ""
        source: backdrop.shell.bgImage
        fillMode: Image.PreserveAspectCrop
        asynchronous: true
    }

    Rectangle {
        anchors.fill: parent
        visible: backdrop.shell.bgImage !== ""
        color: Theme.colorBg
        opacity: 0.5
    }

    // The current game's picture, cross-fading from one to the next, under
    // a share of the background color (art.dim) so text reads on any
    // picture. Two images take turns: the new one fades in over the old.
    Item {
        id: art
        objectName: "art"
        anchors.fill: parent
        readonly property string source: backdrop.showArt ? backdrop.shell.artImage : ""
        property bool first: true
        onSourceChanged: { art.first = !art.first; (art.first ? one : two).source = art.source }
        Component.onCompleted: one.source = art.source

        Image {
            id: one
            objectName: "artOne"
            anchors.fill: parent
            fillMode: Image.PreserveAspectCrop
            asynchronous: true
            opacity: art.first && art.source !== "" ? 1 : 0
            Behavior on opacity { NumberAnimation { duration: Theme.motionBase } }
        }
        Image {
            id: two
            objectName: "artTwo"
            anchors.fill: parent
            fillMode: Image.PreserveAspectCrop
            asynchronous: true
            opacity: !art.first && art.source !== "" ? 1 : 0
            Behavior on opacity { NumberAnimation { duration: Theme.motionBase } }
        }
        Rectangle {
            objectName: "artDim"
            anchors.fill: parent
            color: Theme.colorBg
            opacity: art.source !== "" ? Theme.artDim : 0
            Behavior on opacity { NumberAnimation { duration: Theme.motionBase } }
        }
    }
}
