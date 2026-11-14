import QtQuick

// What each Wii Remote's pointer is over, found in the scene as a mouse
// would find it: the topmost enabled MouseArea under it. It tells the shell
// which row that is (so the row is underlined in the pointer's color) and
// clicks the area when the remote's button goes down. The mouse needs none
// of this: Qt does it.
Item {
    id: hits

    required property var shell
    required property Item scene

    // The topmost enabled MouseArea at x, y of item (a mouse's view of it).
    function find(item, x, y) {
        const kids = item.children
        for (let i = kids.length - 1; i >= 0; --i) {
            const c = kids[i]
            if (!c.visible || c.opacity === 0 || c === hits)
                continue
            const p = c.mapFromItem(item, x, y)
            if (p.x < 0 || p.y < 0 || p.x > c.width || p.y > c.height)
                continue
            const inner = find(c, p.x, p.y)
            if (inner)
                return inner
            if (c.containsMouse !== undefined && c.enabled)
                return c
        }
        return null
    }

    function areaOf(p) {
        return find(hits.scene, p.x * hits.scene.width, p.y * hits.scene.height)
    }

    // The row a MouseArea belongs to: its name is "itemHit/3" or "tileHit/3".
    function rowOf(area) {
        const m = area ? /^(item|tile)Hit\/(\d+)$/.exec(area.objectName) : null
        return m ? parseInt(m[2]) : -1
    }

    function sync() {
        const all = hits.shell.pointers
        for (let i = 0; i < all.length; ++i) {
            const p = all[i]
            if (p.id === "mouse")
                continue
            const row = p.visible ? rowOf(areaOf(p)) : -1
            if (row !== p.hover)
                hits.shell.pointerHover(p.id, row)
        }
    }

    Connections {
        target: hits.shell
        function onPointerChanged() { hits.sync() }
        function onChanged() { hits.sync() }
        function onPointerPressed(id) {
            const all = hits.shell.pointers
            for (let i = 0; i < all.length; ++i) {
                if (all[i].id !== id)
                    continue
                const area = hits.areaOf(all[i])
                if (area)
                    area.clicked(null)
            }
        }
    }
}
