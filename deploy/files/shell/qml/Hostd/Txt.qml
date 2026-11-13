import QtQuick

// The only text there is: one of the four roles (display, title, body,
// label), in Theme's family, sizes and weights. Nothing else styles text.
Text {
    property string role: "body"
    property bool muted: false
    property bool mono: false
    property bool caps: false // a section label: upper case, spaced

    color: muted ? Theme.colorTextMuted : Theme.colorText
    font.family: mono ? Theme.fontMono : Theme.fontText
    font.pixelSize: role === "display" ? Theme.typeDisplay
                  : role === "title" ? Theme.typeTitle
                  : role === "label" ? Theme.typeLabel
                  : Theme.typeBody
    font.weight: role === "display" || role === "title" ? Theme.weightLight : Theme.weightRegular
    font.capitalization: caps ? Font.AllUppercase : Font.MixedCase
    font.letterSpacing: caps ? Theme.spaceXs / 2 : 0
    textFormat: Text.PlainText
    elide: Text.ElideRight
    maximumLineCount: 1
    verticalAlignment: Text.AlignVCenter
}
