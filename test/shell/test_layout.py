"""The layout contract (docs/design.md, "The grid"), checked on the real
QML at every supported size: alignment to the axis and the text column,
nothing in the list entering the category row, a steady rhythm, the corners
clear of the content, text that fits. A failure names the item and how far
off it is."""

import unittest

from harness import SIZES, Screen

TOL = 1.0  # px


def cx(r):
    return r[0] + r[2] / 2


def cy(r):
    return r[1] + r[3] / 2


def intersects(a, b):
    return a[0] < b[0] + b[2] and b[0] < a[0] + a[2] and a[1] < b[1] + b[3] and b[1] < a[1] + a[3]


def inside(inner, outer, slack=TOL):
    return (inner[0] >= outer[0] - slack and inner[1] >= outer[1] - slack
            and inner[0] + inner[2] <= outer[0] + outer[2] + slack
            and inner[1] + inner[3] <= outer[1] + outer[3] + slack)


class Contract(unittest.TestCase):
    """Checks one screen state against the grid."""

    def grid(self, s):
        g = s.engine.singletonInstance("Hostd", "Place")
        t = s.engine.singletonInstance("Hostd", "Theme")
        names = ("axis", "rowLeft", "iconAxis", "columnWidth", "text", "trailEnd", "rowTop", "rowBottom",
                 "sectionTop", "listTop", "bottomReserve")
        return {k: g.property(k) for k in names} | {
            "safe": t.property("layoutSafeMargin"),
            "rowHeight": t.property("layoutItemHeight"),
            "sectionHeight": t.property("layoutSection")}

    def near(self, a, b, what):
        self.assertLessEqual(abs(a - b), TOL, f"{what}: {a:.1f} vs {b:.1f}")

    def rows(self, s, tag="item"):
        """The rows drawn in the list (or the panel): (index, item) in order."""
        area = s.rect("listArea" if tag == "item" else "panelList")
        out = []
        for name, it in s.items().items():
            if name.startswith(tag + "/") and s.shown(it) and intersects(s.rect(it), area):
                out.append((int(name.split("/")[1]), it))
        return sorted(out, key=lambda p: p[0])

    def check(self, s, where):
        g, (w, h) = self.grid(s), s.size
        self.assertEqual(s.warnings, [], f"{where}: QML warnings")

        # The category row: every name centred on its icon; the current
        # icon on the axis; all of the row inside its band.
        band = s.rect("rowBand")
        self.near(band[1], g["rowTop"], f"{where}: row band top")
        n = len(s.shell.property("categories"))
        for c in range(n):
            icon, name = s.rect(f"categoryIcon/{c}"), s.rect(f"categoryName/{c}")
            self.near(cx(name), cx(icon), f"{where}: category {c} name centred on its icon")
            self.assertTrue(inside((icon[0], icon[1], 1, icon[3]), (-1e9, band[1], 2e9, band[3])),
                            f"{where}: category {c} icon outside the band")
        for c in range(n):  # a category shown is wholly inside the safe margins, never cut
            if s.shown(f"category/{c}"):
                icon = s.rect(f"categoryIcon/{c}")
                self.assertTrue(icon[0] >= g["safe"] - TOL and icon[0] + icon[2] <= w - g["safe"] + TOL,
                                f"{where}: category {c} shown across the safe margin: {icon}")
        # The axis is the screen's centre; the list is a block centred on it.
        self.near(g["axis"], w / 2, f"{where}: the axis is the screen's centre")
        self.near(g["rowLeft"] + g["columnWidth"] / 2, w / 2, f"{where}: the list block is centred")
        current = s.shell.property("category")
        self.near(cx(s.rect(f"categoryIcon/{current}")), g["axis"], f"{where}: current category on the axis")

        # The section label and the list: under the band, on the text column.
        section = s.rect("section")
        self.assertGreaterEqual(section[1], band[1] + band[3] - TOL, f"{where}: section enters the band")
        self.near(section[0], g["text"], f"{where}: section on the text column")
        area = s.rect("listArea")
        self.near(area[1], g["listTop"], f"{where}: list top")
        self.near(area[0], g["rowLeft"], f"{where}: list left")
        self.assertLessEqual(area[1] + area[3], h - g["bottomReserve"] + TOL, f"{where}: list too low")

        rows = self.rows(s)
        self.assertTrue(rows, f"{where}: no rows drawn")
        prev = None
        for i, it in rows:
            r = s.rect(it)
            label = f"{where}: row {i}"
            self.assertGreaterEqual(r[1], area[1] - TOL, f"{label} above the list's top")
            self.assertGreaterEqual(r[1], band[1] + band[3], f"{label} enters the category row")
            if prev is not None:  # a steady rhythm: each row starts where the last ends
                self.near(r[1], prev[1] + prev[3], f"{label} follows row {i - 1}")
            prev = r
            kind = it.property("kind")
            self.near(r[3], g["sectionHeight"] if kind == "header" else g["rowHeight"], f"{label} height")
            if kind == "header":
                self.near(s.rect(f"itemSection/{i}")[0], g["text"], f"{label} section label on the text column")
                continue
            icon, title = s.rect(f"itemIcon/{i}"), s.rect(f"itemTitle/{i}")
            self.near(cx(icon), g["iconAxis"], f"{label} icon on the icon column's axis")
            self.near(title[0], g["text"], f"{label} title on the text column")
            self.near(cy(icon), cy(title), f"{label} icon and title on one line")
            trail = s.rect(f"itemTrail/{i}")
            if trail[2] > 0:
                self.near(trail[0] + trail[2], g["trailEnd"], f"{label} trail ends at the column's edge")
                self.near(cy(trail), cy(title), f"{label} trail on the title line")
            if it.property("current"):
                self.assertEqual(i, s.shell.state()["current"], f"{label} current but not the shell's")
            truncated = s.item(f"itemTitle/{i}").property("truncated")
            long_title = "very long title" in it.property("title")
            self.assertEqual(truncated, long_title, f"{label} title elided: {truncated}")

        # The current row is the first drawn, at the list's top.
        first_i, first = rows[0]
        if s.shell.state()["current"] >= 0 and not s.shell.state()["filter"]:
            self.assertEqual(first_i, s.shell.state()["current"], f"{where}: the current row is not at the top")
            self.near(s.rect(first)[1], area[1], f"{where}: the current row's top")

        # The corners: inside the safe margins, clear of the row and the list.
        safe = (g["safe"], g["safe"], w - 2 * g["safe"], h - 2 * g["safe"])
        for name in ("clock", "hints"):
            r = s.rect(name)
            self.assertTrue(inside(r, safe), f"{where}: {name} outside the safe margins: {r}")
            self.assertFalse(intersects(r, area), f"{where}: {name} overlaps the list")
            self.assertFalse(intersects(r, (g["rowLeft"], band[1], w, band[3])) and r[1] < band[1] + band[3]
                             and r[1] + r[3] > band[1], f"{where}: {name} overlaps the category row")
        self.near(s.rect("clock")[0] + s.rect("clock")[2], w - g["safe"], f"{where}: clock right edge")
        self.near(s.rect("hints")[0] + s.rect("hints")[2], w - g["safe"], f"{where}: hints right edge")


class MenuLayout(Contract):
    def test_every_category_and_scroll(self):
        for size_name, size in SIZES.items():
            with Screen(size) as s:
                self.check(s, f"{size_name} apps")
                for n in range(1, 12):
                    s.verb("down")
                    self.check(s, f"{size_name} apps down {n}")
                for c in ("users", "settings", "status"):
                    s.verb("right")
                    self.check(s, f"{size_name} {c}")
                    s.verb("down", "down")
                    self.check(s, f"{size_name} {c} down 2")

    def test_the_pointer_is_drawn_where_it_is_and_rows_can_be_hit(self):
        for size_name, size in SIZES.items():
            with Screen(size) as s:
                w, h = size
                s.settle(12)  # the window's own first hover has landed
                s.shell.hide_pointer()
                self.assertFalse(s.shell.property("pointer")["visible"], size_name)
                s.shell.mouseAt(w * 0.3 - 20, h * 0.7, w, h)
                s.shell.mouseAt(w * 0.3, h * 0.7, w, h)
                s.settle()
                r = s.rect("pointer")
                self.assertTrue(s.shown("pointer"), size_name)
                self.near(cx(r), w * 0.3, f"{size_name}: pointer x")
                self.near(cy(r), h * 0.7, f"{size_name}: pointer y")
                self.assertGreater(r[2], 4, size_name)
                # Every row drawn has a hit area as big as the row.
                for i, row in self.rows(s):
                    self.assertEqual(s.rect(f"itemHit/{i}")[2:], s.rect(row)[2:], f"{size_name}: row {i} hit area")
                for c in range(4):
                    self.assertTrue(s.item(f"categoryHit/{c}") is not None, size_name)

    def test_folders_keep_the_grid(self):
        for size_name, size in SIZES.items():
            with Screen(size) as s:
                s.verb("down", "down")
                self.assertEqual(s.shell.current_row().kind, "folder")
                self.check(s, f"{size_name} a folder row")
                s.verb("activate")
                self.assertEqual(s.shell.property("section"), "Games")
                self.check(s, f"{size_name} Games")
                s.verb("activate")  # Dolphin, the first folder in Games
                self.assertEqual(s.shell.property("section"), "Games / Dolphin")
                self.check(s, f"{size_name} Games / Dolphin")
                s.verb("back", "back")
                self.check(s, f"{size_name} back at the top")

    def test_tabs_and_services_keep_the_grid(self):
        for size_name, size in SIZES.items():
            with Screen(size, tabs=True) as s:
                self.check(s, f"{size_name} apps with tabs")
                for c in ("Games", "Services", "Users", "Settings", "Status"):
                    s.verb("right")
                    if c == "Games":
                        self.check_tiles(s, f"{size_name} {c} tab")
                    else:
                        self.check(s, f"{size_name} {c} tab")

    def test_tiles_keep_the_grid(self):
        for size_name, size in SIZES.items():
            with Screen(size, tabs=True) as s:
                s.verb("right")  # Games: tiles, on the Dolphin launcher
                for n in range(7):
                    self.check_tiles(s, f"{size_name} tiles down {n}")
                    s.verb("down")

    def check_tiles(self, s, where):
        g, (w, h) = self.grid(s), s.size
        p = s.engine.singletonInstance("Hostd", "Place")
        self.assertEqual(s.warnings, [], f"{where}: QML warnings")
        area, band = s.rect("tilesArea"), s.rect("rowBand")
        self.assertFalse(s.shown("listArea"), f"{where}: the list shows too")
        self.near(area[0], g["rowLeft"], f"{where}: tiles at the block's left")
        self.near(area[1], g["listTop"], f"{where}: tiles at the list's top")
        self.assertLessEqual(area[1] + area[3], h - g["bottomReserve"] + TOL, f"{where}: tiles into the hints")
        cur = s.shell.state()["current"]
        row = s.rect(f"tile/{cur}")
        self.near(row[1], area[1], f"{where}: the current row at the list's top")
        kind = s.item(f"tile/{cur}").property("kind")
        if kind == "launcher":
            self.near(row[3], g["sectionHeight"], f"{where}: a launcher is as high as a section label")
            self.assertTrue(s.shown(f"tileFocus/{cur}"), f"{where}: the current launcher is underlined")
        else:
            box = s.rect(f"tileBox/{cur}")
            self.near(box[2], p.property("tileWidth"), f"{where}: the current tile's width")
            self.near(box[3], p.property("tileHeight"), f"{where}: the current tile's height")
            self.near(box[3] / box[2], 215 / 460, f"{where}: a library header's shape")
        title = s.rect("tilesTitle")
        self.near(title[0], p.property("tileText"), f"{where}: the title on the right")
        self.near(title[1], area[1], f"{where}: the title at the list's top")
        info = s.rect("tilesInfo")
        self.near(info[0] + info[2], g["trailEnd"], f"{where}: the right side ends at the block's edge")
        banner = s.item("tilesBanner")
        details = s.rect("tilesDetails")
        if s.shown(banner):
            b = s.rect(banner)
            self.assertLessEqual(b[2], info[2] + TOL, f"{where}: the banner wider than the right side")
            self.near(b[0], info[0], f"{where}: the banner at the right side's left")
            ratio = banner.property("implicitHeight") / banner.property("implicitWidth")
            self.near(b[3], b[2] * ratio, f"{where}: the banner keeps its shape")
            sub = s.rect("tilesSubtitle")
            self.assertGreaterEqual(b[1], sub[1] + sub[3] - TOL, f"{where}: banner over the subtitle")
            if s.item("tilesDetails").property("text"):
                self.assertGreaterEqual(details[1], b[1] + b[3] - TOL, f"{where}: details over the banner")
        # Nothing on the right is cut off by the area's edge.
        for name in ("tilesTitle", "tilesSubtitle", "tilesBanner", "tilesDetails"):
            if s.shown(name) and (name != "tilesDetails" or s.item(name).property("text")):
                r = s.rect(name)
                self.assertLessEqual(r[1] + r[3], area[1] + area[3] + TOL, f"{where}: {name} cut off at the bottom")
        if s.item("tilesDetails").property("text"):
            self.assertLessEqual(s.item("tilesDetails").property("contentHeight"), details[3] + TOL, f"{where}: details cut")
        safe = (g["safe"], g["safe"], w - 2 * g["safe"], h - 2 * g["safe"])
        drawn = 0
        for name, it in s.items().items():
            if not name.startswith("tileBox/") or not s.shown(it):
                continue
            r = s.rect(it)
            if not intersects(r, area):
                continue
            drawn += 1
            self.near(cx(r), p.property("tileAxis"), f"{where}: {name} on the tiles' axis")
            self.assertGreaterEqual(r[1], band[1] + band[3], f"{where}: {name} enters the category row")
            self.assertTrue(r[0] >= safe[0] - TOL, f"{where}: {name} outside the margins")
        self.assertGreaterEqual(drawn, 1, f"{where}: no tiles drawn")

    def test_options_panel(self):
        for size_name, size in SIZES.items():
            with Screen(size) as s:
                s.verb("actions")
                g, (w, h) = self.grid(s), s.size
                panel = s.rect("panel")
                self.near(panel[0] + panel[2], w, f"{size_name}: panel at the right edge")
                self.near(s.rect("panelTitle")[1], g["sectionTop"], f"{size_name}: panel title level with the section")
                self.near(s.rect("panelList")[1], g["listTop"], f"{size_name}: options level with the list")
                rows = self.rows(s, "option")
                self.assertGreaterEqual(len(rows), 2, f"{size_name}: options")
                axis = cx(s.rect(f"optionIcon/{rows[0][0]}"))
                text = s.rect(f"optionTitle/{rows[0][0]}")[0]
                prev = None
                for i, it in rows:
                    r = s.rect(it)
                    self.near(cx(s.rect(f"optionIcon/{i}")), axis, f"{size_name}: option {i} icon on the panel's axis")
                    self.near(s.rect(f"optionTitle/{i}")[0], text, f"{size_name}: option {i} title column")
                    if prev is not None:
                        self.near(r[1], prev[1] + prev[3], f"{size_name}: option {i} rhythm")
                    prev = r
                self.assertTrue(inside(s.rect("hints"), (0, 0, w, h)), f"{size_name}: hints")
                self.assertEqual(s.warnings, [])

    def test_level_panel(self):
        for size_name, size in SIZES.items():
            with Screen(size) as s:
                s.verb("right", "right", "activate")  # Settings: Volume
                self.assertEqual(s.shell.state()["panel"], "level", size_name)
                g, h = self.grid(s), s.size[1]
                level = s.rect("panelLevel")
                self.near(level[1], g["listTop"], f"{size_name}: level at the list's top")
                self.assertLessEqual(level[1] + level[3], h - g["bottomReserve"] + TOL, f"{size_name}: level into the hints")
                self.assertFalse(intersects(level, s.rect("hints")), f"{size_name}: level over the hints")
                self.assertEqual(s.warnings, [])

    def test_question_band(self):
        for size_name, size in SIZES.items():
            with Screen(size) as s:
                s.verb("right", "down", "activate")  # Users: guest, which asks first
                self.assertTrue(s.shell.state()["question"], f"{size_name}: no question")
                w, h = s.size
                band = s.rect("band")
                self.near(cy(band), h / 2, f"{size_name}: band centred")
                self.near(band[2], w, f"{size_name}: band full width")
                self.assertEqual(s.warnings, [])

    def test_filter_shows_matches_only(self):
        with Screen(SIZES["1920x1080"]) as s:
            for ch in "dol":
                s.key("Key_" + ch.upper(), ch)
            titles = [row.property("title") for _index, row in self.rows(s)]
            self.assertEqual(titles, ["Dolphin", "Mario Kart Wii", "Super Mario Galaxy"])  # the folder is searched too
            self.assertTrue(s.shown("filter"))
            self.check(s, "filtered")


class Overlays(Contract):
    def test_notices_and_osd(self):
        for size_name, size in SIZES.items():
            with Screen(size, "NoticeLayer") as s:
                s.shell.notify("Steam", "Download finished", "Dota 2 is ready to play", 0, 1, -1)
                s.shell.notify("hostd", "Video memory nearly full", "3.9 GB of 4 GB in use", 0, 2, -1)
                s.settle()
                self.assertEqual(s.warnings, [], size_name)
                self.assertTrue(s.window.isVisible(), size_name)
            with Screen(size, "OsdLayer") as s:
                s.shell.show_osd("Volume", "70%", 70)
                s.settle()
                self.assertEqual(s.warnings, [], size_name)


if __name__ == "__main__":
    unittest.main()
