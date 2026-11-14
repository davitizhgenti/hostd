pragma Singleton
import QtQuick

// A device's colour by the name hostd gave it (blue, orange, green, pink,
// yellow, teal, red, violet): the shell's tokens, one for each. No name:
// the plain text colour (the shell gives none while one device points).
QtObject {
    function colour(name) {
        switch (name) {
        case "orange": return Theme.colorDeviceOrange
        case "green": return Theme.colorDeviceGreen
        case "pink": return Theme.colorDevicePink
        case "yellow": return Theme.colorDeviceYellow
        case "teal": return Theme.colorDeviceTeal
        case "red": return Theme.colorDeviceRed
        case "violet": return Theme.colorDeviceViolet
        case "blue": return Theme.colorDeviceBlue
        default: return Theme.colorText
        }
    }
}
