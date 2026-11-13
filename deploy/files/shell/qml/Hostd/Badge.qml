import QtQuick

// A state in a word, in its role's color: running, starting, failed.
Txt {
    property string tone: ""

    role: "label"
    color: tone === "positive" ? Theme.colorPositive
         : tone === "warning" ? Theme.colorWarning
         : tone === "danger" ? Theme.colorDanger
         : tone === "accent" ? Theme.colorAccent
         : Theme.colorTextMuted
}
