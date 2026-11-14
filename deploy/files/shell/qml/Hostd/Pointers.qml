pragma ComponentBehavior: Bound
import QtQuick

// Every pointer, each its own: the mouse's and one for each Wii Remote. The
// one that moved last is named "pointer" (the tests' and the layout's), the
// others "pointer/<device>". The list of them changes only when a device's
// first pointer appears; each one follows its own state.
Item {
    id: pointers
    required property var shell

    Repeater {
        model: pointers.shell.pointerIds

        Pointer {
            id: one
            required property string modelData
            now: pointers.shell.pointers.find(p => p.id === one.modelData) || ({id: one.modelData, x: 0, y: 0, visible: false, down: false, colour: ""})
            first: modelData === pointers.shell.pointer.id // the one that moved last
        }
    }
}
