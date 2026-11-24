import QtQuick
import Hostd.Shell as Core

// The background: the user's setting (the shell's bg*), a gradient, or an image
// darkened so text reads on it. The same behind the apps and the menu.
Rectangle {
    id: backdrop

    required property Core.Backdrop look
    color: Theme.colorBg

    gradient: Gradient {
        GradientStop {
            position: 0
            color: backdrop.look.top !== "" ? backdrop.look.top : Theme.colorBg
            Behavior on color { ColorAnimation { duration: 1000 } }
        }
        GradientStop {
            position: 1
            color: backdrop.look.bottom !== "" ? backdrop.look.bottom : Theme.colorBg
            Behavior on color { ColorAnimation { duration: 1000 } }
        }
    }

    Image {
        anchors.fill: parent
        visible: backdrop.look.image !== ""
        source: backdrop.look.image
        fillMode: Image.PreserveAspectCrop
        asynchronous: true
    }

    Rectangle {
        anchors.fill: parent
        visible: backdrop.look.image !== ""
        color: Theme.colorBg
        opacity: 0.5
    }
}
