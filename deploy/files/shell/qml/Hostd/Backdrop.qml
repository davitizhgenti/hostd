import QtQuick

// The background: the user's setting (the shell's bg*), a gradient, or an image
// darkened so text reads on it. The same behind the apps and the menu.
Rectangle {
    id: backdrop

    // The shell (Python): what is current, the data, and what input does.
    required property var shell
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
}
