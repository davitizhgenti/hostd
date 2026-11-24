"""The shell's behaviour, without drawing: verbs in, state and requests to
hostd out (Shell, the one place input turns into behaviour). And the
background's guarantee: text reads on it at every month and hour."""

import colorsys
import datetime
import os
import tempfile
import unittest

import fixtures
import harness  # the Qt application the shell's timers need
from hostd_shell import background, views
from hostd_shell.core.nav import APPS, SETTINGS, STATUS, USERS
from hostd_shell.shell import Shell


def shell(menu=None):
    """A shell on the fixture data; menu: the menu file's text (tabs)."""
    menu_path = "/nonexistent/menu.toml"
    if menu is not None:
        menu_path = os.path.join(tempfile.mkdtemp(), "menu.toml")
        with open(menu_path, "w") as f:
            f.write(menu)
    s = Shell(fixtures.FakeAPI(), harness.TOKENS, now=lambda: fixtures.NOW,
              look_path=os.path.join(tempfile.mkdtemp(), "ui.toml"), menu_path=menu_path)
    s.pointers.mouse_ms = 0  # the tests' moves are applied at once
    for c in s.menu.nav.cats:
        s.menu.refresh(c.key)
    return s


def titles(s):
    return [r.title for r in s.menu.page().rows]


def with_status_service():
    """A shell with tabs where Home Assistant says what it is doing, on its tab."""
    s = shell(fixtures.MENU)
    for a in s.api.reads["/v1/apps"]:
        if a["spec"]["id"] == "homeassistant":
            a["spec"]["status"] = {"url": "http://127.0.0.1:8481/status"}
    s.api.reads["/v1/catalog/homeassistant/status"] = fixtures.STATUS
    s.menu.refresh("tab:Services")
    s.menu.handle("right")
    s.menu.handle("right")  # the Services tab
    while s.menu.current_row().title != "Home Assistant":
        s.menu.handle("down")
    return s


class OsdShown(unittest.TestCase):
    """The OSD is an object of its own, shown for a moment."""

    def test_shown_then_hidden(self):
        osd = shell().osd
        self.assertFalse(osd.visible)
        changes = []
        osd.changed.connect(lambda: changes.append(osd.visible))
        osd.show("Volume", "70%", 70)
        self.assertEqual((osd.visible, osd.name, osd.value, osd.level), (True, "Volume", "70%", 70))
        osd.show("Screen", "guest", None)  # no bar
        self.assertEqual((osd.name, osd.level), ("Screen", -1))
        self.assertTrue(osd.timer.isActive())
        osd.hide()
        self.assertEqual(changes, [True, True, False])

    def test_an_event_shows_it(self):
        s = shell()
        s.on_event({"type": "audio.volume.changed", "data": {"percent": 40}})
        self.assertEqual((s.osd.visible, s.osd.value, s.osd.level), (True, "40%", 40))


class ServicePage(unittest.TestCase):
    """A service that says what it is doing opens a page of it when chosen."""

    def test_choosing_it_opens_the_page_and_back_returns_to_the_list(self):
        s = with_status_service()
        self.assertEqual(s.menu.state()["category"], "tab:Services")
        s.menu.handle("activate")
        self.assertTrue(s.menu.in_page())
        self.assertEqual(titles(s), ["Running now", "Fetching eurodesk", "Processing", "Next runs", "Process", "SIROP search",
                                     "Train", "Counts", "Items stored", "New today", "Sources with errors"])
        self.assertEqual(s.menu.summary, "Opportunities")
        self.assertEqual(s.menu.view, "list")
        self.assertEqual({r.kind for r in s.menu.page().rows}, {"header", "fact"})  # a fact shows its value on every row
        fetching = s.menu.page().rows[1]
        self.assertEqual((fetching.subtitle, fetching.badge, fetching.tone), ("started 2 min ago", "running", "positive"))
        self.assertEqual(s.menu.page().rows[5].tone, "danger")  # the failed source
        self.assertIn(("Esc", "back"), [(h["button"], h["what"]) for h in s.menu.hintList])
        s.menu.handle("back")
        self.assertFalse(s.menu.in_page())
        self.assertEqual((s.menu.current_row().title, s.menu.state()["category"]), ("Home Assistant", "tab:Services"))
        self.assertIn("Syncthing", titles(s))

    def test_its_options_are_still_there(self):
        s = with_status_service()
        s.menu.handle("actions")
        self.assertEqual(s.menu.state()["panel"], "options")
        self.assertEqual([r.title for r in s.menu.options_model.rows], ["Stop", "Restart"])

    def test_the_page_asks_again_while_it_is_open(self):
        s = with_status_service()
        s.menu.handle("activate")
        self.assertTrue(s.menu.page_timer.isActive())
        s.api.reads["/v1/catalog/homeassistant/status"] = {"ok": True, "document": {"title": "Opportunities", "sections": [
            {"title": "Running now", "rows": [{"title": "Idle"}]}]}}
        s.menu.window.visible = True
        s.menu._refresh_page()
        self.assertEqual(titles(s), ["Running now", "Idle"])
        s.menu.handle("back")
        self.assertFalse(s.menu.page_timer.isActive())

    def test_a_service_that_does_not_answer_is_said_so(self):
        s = with_status_service()
        s.api.reads["/v1/catalog/homeassistant/status"] = {"ok": False, "error": "does not answer"}
        s.menu.handle("activate")
        row = s.menu.page().rows[0]
        self.assertEqual((row.title, row.subtitle, row.tone), ("Not answering", "does not answer", "warning"))
        s.api.reads["/v1/catalog/homeassistant/status"] = {"ok": True, "document": {"sections": []}}
        s.menu.window.visible = True
        s.menu._refresh_page()
        self.assertEqual(titles(s), ["Nothing to report"])

    def test_leaving_the_tab_or_going_home_closes_it(self):
        s = with_status_service()
        s.menu.handle("activate")
        s.menu.handle("right")
        self.assertFalse(s.menu.in_page())
        s.menu.handle("left")
        self.assertEqual((s.menu.state()["category"], s.menu.in_page(), "Syncthing" in titles(s)), ("tab:Services", False, True))
        while s.menu.current_row().title != "Home Assistant":
            s.menu.handle("down")
        s.menu.handle("activate")
        self.assertTrue(s.menu.in_page())
        s.menu.handle("home")
        self.assertFalse(s.menu.in_page())
        self.assertEqual(s.menu.state()["category"], APPS)

    def test_a_service_with_no_status_opens_its_options_as_before(self):
        s = with_status_service()
        s.menu.handle("down")  # Syncthing: says nothing
        self.assertEqual(s.menu.current_row().title, "Syncthing")
        s.menu.handle("activate")
        self.assertEqual((s.menu.in_page(), s.menu.state()["panel"]), (False, "options"))


class Moving(unittest.TestCase):
    def test_starts_on_the_first_item(self):
        s = shell()
        self.assertEqual(s.menu.state()["category"], APPS)
        self.assertEqual(s.menu.current_row().title, "Steam")
        self.assertEqual(s.menu.section, "")  # Apps has no headers
        self.assertEqual((s.menu.offset()), (0, 0))

    def test_running_apps_folders_then_apps_once_each(self):
        s = shell()
        self.assertEqual(titles(s)[:5], ["Steam", "Chromium", "Games", "Services", "Kodi"])
        self.assertEqual(titles(s).count("Steam"), 1)
        self.assertEqual([r.mark for r in s.menu.page().rows[:3]], ["positive", "warning", ""])

    def test_down_stops_at_the_end(self):
        s = shell()
        s.menu.handle("down")
        s.menu.handle("down")
        self.assertEqual(s.menu.current_row().title, "Games")
        self.assertEqual(s.menu.offset(), (0, 2))
        for _ in range(30):
            s.menu.handle("down")
        self.assertEqual(s.menu.current_row().title, "VLC")
        for _ in range(30):
            s.menu.handle("up")
        self.assertEqual(s.menu.state()["current"], 0)

    def test_left_and_right_change_category_and_wrap(self):
        s = shell()
        for want in (USERS, SETTINGS, STATUS, APPS):
            s.menu.handle("right")
            self.assertEqual(s.menu.state()["category"], want)
        s.menu.handle("left")
        self.assertEqual(s.menu.state()["category"], STATUS)

    def test_each_category_remembers_its_place(self):
        s = shell()
        s.menu.handle("down")
        s.menu.handle("right")
        s.menu.handle("left")
        self.assertEqual(s.menu.current_row().title, "Chromium")


class Choosing(unittest.TestCase):
    def test_choose_sends_its_call(self):
        s = shell()
        s.menu.handle("activate")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/switch", None))

    def test_options_panel(self):
        s = shell()
        s.menu.handle("actions")
        self.assertEqual(s.menu.state()["panel"], "options")
        self.assertEqual([r.title for r in s.menu.options_model.rows], ["Bring to front", "Close", "Big Picture", "Dota 2"])
        s.menu.handle("down")
        s.menu.handle("down")
        s.menu.handle("activate")  # Big Picture
        self.assertEqual(s.menu.state()["panel"], "")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/apps/steam/open", {"action": "bigpicture"}))

    def test_options_close_with_back_and_left(self):
        s = shell()
        for verb in ("back", "left", "actions"):
            s.menu.handle("actions")
            s.menu.handle(verb)
            self.assertEqual(s.menu.state()["panel"], "", verb)
            self.assertEqual(s.menu.state()["category"], APPS, verb)

    def test_question_defaults_to_no(self):
        s = shell()
        s.menu.handle("right")
        s.menu.handle("down")  # guest
        s.menu.handle("activate")
        self.assertIn("guest", s.menu.state()["question"])
        self.assertFalse(s.menu.state()["yes"])
        s.menu.handle("activate")  # No: nothing sent
        self.assertEqual(s.api.sent, [])
        s.menu.handle("activate")
        s.menu.handle("right")
        s.menu.handle("activate")  # Yes
        self.assertEqual(s.api.sent[-1][2], {"type": "seat.switch", "args": {"user": "guest"}})

    def test_volume_panel_changes_with_up_and_down(self):
        s = shell()
        s.menu.handle("right")
        s.menu.handle("right")
        self.assertEqual(s.menu.current_row().title, "Volume")
        s.menu.handle("activate")
        self.assertEqual(s.menu.state()["panel"], "level")
        s.menu.handle("up")
        self.assertEqual(s.api.sent[-1][2], {"type": "audio.volume.set", "args": {"percent": "+5"}})
        s.menu.handle("down")
        self.assertEqual(s.api.sent[-1][2], {"type": "audio.volume.set", "args": {"percent": "-5"}})
        s.menu.handle("left")  # left closes the panel, it does not change category
        self.assertEqual((s.menu.state()["panel"], s.menu.state()["category"]), ("", SETTINGS))

    def test_back_goes_to_apps_then_to_the_app(self):
        s = shell()
        s.menu.handle("right")
        s.menu.handle("back")
        self.assertEqual(s.menu.state()["category"], APPS)
        s.menu.handle("back")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/windows/back", None))

    def test_close_only_on_running_apps(self):
        s = shell()
        s.menu.handle("close")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/close", None))
        s.menu.handle("down")
        s.menu.handle("down")  # a catalog app: nothing to close
        sent = len(s.api.sent)
        s.menu.handle("close")
        self.assertEqual(len(s.api.sent), sent)


class Filtering(unittest.TestCase):
    def test_typing_filters_apps_and_back_clears(self):
        s = shell()
        for ch in "dol":
            self.assertTrue(s.menu.key("Key_" + ch.upper(), ch, False))
        # Dolphin, and the games in its folder (the folder is searched too).
        self.assertEqual(titles(s), ["Dolphin", "Mario Kart Wii", "Super Mario Galaxy"])
        self.assertTrue(s.menu.key("Key_Backspace", "", False))
        self.assertEqual(s.menu.state()["filter"], "do")
        s.menu.handle("back")
        self.assertEqual(s.menu.state()["filter"], "")
        self.assertIn("Games", titles(s))

    def test_typing_elsewhere_is_ignored(self):
        s = shell()
        s.menu.handle("right")
        self.assertFalse(s.menu.key("Key_A", "a", False))
        self.assertEqual(s.menu.state()["filter"], "")


class Hints(unittest.TestCase):
    def test_hints_name_only_what_the_row_does(self):
        s = shell()
        what = lambda: [h["what"] for h in s.menu.hintList]  # noqa: E731
        self.assertEqual(what(), ["open", "options", "close"])  # Steam, running
        s.menu.handle("down")
        s.menu.handle("down")
        self.assertEqual(what(), ["open"])  # the Games folder
        s.menu.handle("activate")
        self.assertEqual(what(), ["open", "back"])  # a folder open: back goes up
        s.menu.handle("home")
        s.menu.handle("down")
        s.menu.handle("down")
        s.menu.handle("down")
        s.menu.handle("activate")  # Services
        self.assertEqual(what(), ["options", "back"])  # a service acts through its options
        s.menu.handle("right")
        s.menu.handle("down")
        self.assertEqual(what(), ["choose"])  # Users: guest

    def test_hints_follow_the_device(self):
        s = shell()
        s.on_event({"type": "display.nav", "data": {"what": "down"}})  # the menu is not in front: ignored
        self.assertEqual(s.menu.current_row().title, "Steam")
        s.showMenu()
        s.menu.setMenuActive(True)
        s.on_event({"type": "display.nav", "data": {"what": "down"}})
        self.assertEqual(s.menu.current_row().title, "Chromium")
        self.assertEqual([h["button"] for h in s.menu.hintList][:2], ["A", "Menu"])


def nav(s, what, family="xbox"):
    s.on_event({"type": "display.nav", "data": {"what": what, "family": family}})


class FolderNavigation(unittest.TestCase):
    """Folders open like lists of their own; back goes up."""

    def setUp(self):
        self.s = shell()

    def go_to_games(self):
        s = self.s
        for _ in range(40):
            if s.menu.current_row().kind == "folder":
                break
            s.menu.handle("down")
        self.assertEqual(s.menu.current_row().title, "Games")
        s.menu.handle("activate")

    def test_open_drill_in_and_back_out(self):
        s = self.s
        self.go_to_games()
        self.assertEqual((list(s.menu.folderPath), s.menu.section), (["Games"], "Games"))
        self.assertEqual(titles(s)[:2], ["Dolphin", "Steam"])
        self.assertEqual(s.menu.current_row().title, "Dolphin")
        s.menu.handle("activate")
        self.assertEqual(titles(s)[:2], ["Wii", "Dolphin"])
        s.menu.handle("activate")  # Wii
        self.assertEqual(s.menu.section, "Games / Dolphin / Wii")
        self.assertEqual(s.menu.current_row().title, "Mario Kart Wii")
        s.menu.handle("back")  # up to Games / Dolphin, on Wii
        self.assertEqual((list(s.menu.folderPath), s.menu.current_row().title), (["Games", "Dolphin"], "Wii"))
        s.menu.handle("back")
        s.menu.handle("back")
        self.assertEqual((list(s.menu.folderPath), s.menu.current_row().title), ([], "Games"))
        self.assertEqual(s.api.sent, [])  # folders never asked hostd to do anything

    def test_choosing_an_app_in_a_folder_opens_it(self):
        s = self.s
        self.go_to_games()
        s.menu.handle("down")  # the Steam folder
        s.menu.handle("activate")
        self.assertEqual(titles(s)[:2], ["Dota 2", "Steam Link"])
        s.menu.handle("down")
        s.menu.handle("activate")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/apps/steam-launcher/open", None))

    def test_leaving_apps_and_home_close_folders(self):
        s = self.s
        self.go_to_games()
        s.menu.handle("right")
        s.menu.handle("left")
        self.assertEqual(list(s.menu.folderPath), [])
        self.go_to_games()
        s.menu.handle("home")
        self.assertEqual((list(s.menu.folderPath), s.menu.category), ([], 0))

    def test_back_at_the_top_still_goes_back(self):
        self.s.menu.handle("back")
        self.assertEqual(self.s.api.sent[-1], ("POST", "/v1/windows/back", None))

    def test_a_filter_searches_inside_folders(self):
        s = self.s
        for ch in "mario":
            s.menu.key("Key_" + ch.upper(), ch, False)
        found = sorted(r.title for r in views.visible(s.menu.nav.all_rows[APPS], s.menu.nav.filter))
        self.assertEqual(found, ["Mario Kart Wii", "Super Mario Galaxy"])


class Services(unittest.TestCase):
    """Background apps are in Services; they start and stop through their
    options, never on one press."""

    def open_services(self, s):
        while s.menu.current_row().title != "Services":
            s.menu.handle("down")
        s.menu.handle("activate")

    def test_states_and_options(self):
        s = shell()
        self.open_services(s)
        self.assertEqual([(r.title, r.badge) for r in s.menu.page().rows if not r.under],
                         [("Home Assistant", "running"), ("Syncthing", "stopped"), ("Pi-hole", "failed")])
        s.menu.handle("activate")  # opens the options: nothing sent
        self.assertEqual((s.menu.state()["panel"], s.api.sent), ("options", []))
        self.assertEqual([r.title for r in s.menu.options_model.rows], ["Stop", "Restart"])
        s.menu.handle("down")
        s.menu.handle("activate")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/homeassistant/restart", None))

    def test_delete_does_not_stop_a_service(self):
        s = shell()
        self.open_services(s)
        s.menu.handle("close")
        self.assertEqual(s.api.sent, [])

    def test_a_failed_one_starts_again(self):
        s = shell()
        self.open_services(s)
        s.menu.handle("down")
        s.menu.handle("down")
        s.menu.handle("activate")
        s.menu.handle("activate")  # Start
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/apps/pihole/open", None))


class Tabs(unittest.TestCase):
    """Folders the menu file marks are categories of their own."""

    def test_the_marked_folders_are_tabs_after_apps(self):
        s = shell(fixtures.MENU)
        self.assertEqual([c["name"] for c in s.menu.categories],
                         ["Apps", "Games", "Services", "Users", "Settings", "Status"])
        self.assertEqual(s.menu.categories[1]["icon"], "gamepad-2")
        self.assertEqual(titles(s)[:3], ["Steam", "Chromium", "Kodi"])  # Apps has no Games or Services folder
        s.menu.handle("right")
        self.assertEqual((s.menu.state()["category"], s.menu.category), ("tab:Games", 1))
        # A tiles tab: every game in it, in groups each headed by its launcher.
        self.assertEqual((s.menu.view, titles(s), s.menu.section),
                         ("tiles", ["Dolphin", "Mario Kart Wii", "Super Mario Galaxy", "Steam", "Dota 2",
                                    "Other games", "Steam Link"], ""))
        self.assertEqual([r.kind for r in s.menu.page().rows][:4], ["launcher", "item", "item", "launcher"])
        s.menu.handle("right")
        self.assertEqual(titles(s), ["Home Assistant", "Syncthing", "Pi-hole"])

    def test_back_stops_at_the_tab(self):
        s = shell('[[tab]]\nfolder = "Games"\n')  # a list tab: its folders open
        s.menu.handle("right")
        s.menu.handle("activate")  # Dolphin
        self.assertEqual((s.menu.section, titles(s)), ("Games / Dolphin", ["Wii", "Dolphin"]))
        s.menu.handle("back")
        self.assertEqual((s.menu.state()["category"], s.menu.current_row().title), ("tab:Games", "Dolphin"))
        s.menu.handle("back")  # at the tab's top: back to Apps, as from any category
        self.assertEqual(s.menu.state()["category"], APPS)

    def test_a_launcher_with_no_games_yet_still_heads_its_group(self):
        s = shell(fixtures.MENU)
        s.api.reads["/v1/apps"].append({"spec": {"id": "ppsspp", "name": "PPSSPP", "surface": "window",
                                                 "folder": "Games/PPSSPP", "launcher": True}})
        s.api.reads["/v1/apps"].append({"spec": {"id": "plain", "name": "Plain Game", "surface": "window", "folder": "Games"}})
        s.menu.refresh("tab:Games")
        s.menu.handle("right")
        rows = s.menu.page().rows
        self.assertEqual([(r.title, r.kind, r.value) for r in rows if r.title in ("PPSSPP", "Plain Game")],
                         [("PPSSPP", "launcher", "0 games"), ("Plain Game", "item", "")])
        self.assertLess(titles(s).index("PPSSPP"), titles(s).index("Other games"))  # a group, not among the others

    def test_moving_through_groups_and_opening_a_launcher(self):
        s = shell(fixtures.MENU)
        s.menu.handle("right")
        self.assertEqual((s.menu.current_row().title, s.menu.focus["subtitle"]), ("Dolphin", "2 games"))
        s.menu.handle("down")  # Mario Kart Wii: its banner and details on the right
        focus = s.menu.focus
        self.assertEqual((focus["title"], focus["banner"], focus["details"], focus["launcher"]),
                         ("Mario Kart Wii", "file://" + fixtures.BANNER_A, "Last played 12 February 2026 · 3.1 GB", False))
        self.assertEqual(s.menu.offset(), (1, 0))  # the launcher above, as high as a header
        for _ in range(3):
            s.menu.handle("down")  # onto Steam, down its line of games, over the Other games label
        self.assertEqual((s.menu.current_row().title, s.menu.offset()), ("Steam Link", (3, 2)))  # a wall: lines, not rows
        s.menu.handle("up")
        s.menu.handle("up")  # Steam, the launcher
        self.assertEqual(s.menu.current_row().title, "Steam")
        s.menu.handle("activate")  # running: brought to the front
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/switch", None))
        self.assertEqual([h["what"] for h in s.menu.hintList], ["open", "options", "close"])

    def test_left_and_right_walk_along_a_line_and_leave_at_its_end(self):
        s = shell(fixtures.MENU)
        s.menu.handle("right")
        s.menu.handle("down")  # Mario Kart Wii, the line's first tile
        s.menu.handle("right")
        self.assertEqual((s.menu.current_row().title, s.menu.state()["category"]), ("Super Mario Galaxy", "tab:Games"))
        s.menu.handle("left")
        self.assertEqual(s.menu.current_row().title, "Mario Kart Wii")
        s.menu.handle("left")  # the line's start: on to the previous category
        self.assertEqual(s.menu.state()["category"], "apps")
        s.menu.handle("right")  # back on Games, where it was: Mario Kart Wii
        s.menu.handle("right")
        s.menu.handle("right")  # the line's end: the next category
        self.assertEqual(s.menu.state()["category"], "tab:Services")

    def test_typing_in_a_tab_searches_every_app(self):
        s = shell(fixtures.MENU)
        s.menu.handle("right")
        for ch in "kodi":
            self.assertTrue(s.menu.key("Key_" + ch.upper(), ch, False))
        self.assertEqual(titles(s), ["Kodi"])
        s.menu.handle("right")  # leaving clears it
        self.assertEqual((s.menu.state()["filter"], s.menu.state()["category"]), ("", "tab:Services"))

    def test_changing_the_file_changes_the_tabs(self):
        s = shell(fixtures.MENU)
        changed = []
        s.menu.categoriesChanged.connect(lambda: changed.append(1))
        s.menu.handle("right")
        s.menu.handle("right")  # Services
        with open(s.menu.menu_path, "w") as f:
            f.write('[[tab]]\nfolder = "Games"\n')
        os.utime(s.menu.menu_path, ns=(1, 1))  # a change the clock cannot miss
        s.menu.reload_menu()
        self.assertEqual(changed, [1])
        self.assertEqual([c["name"] for c in s.menu.categories], ["Apps", "Games", "Users", "Settings", "Status"])
        self.assertEqual(s.menu.state()["category"], APPS)  # its tab went: back to Apps
        self.assertEqual(s.menu.categories[1]["icon"], "folder")
        self.assertIn("Services", titles(s))
        with open(s.menu.menu_path, "w") as f:
            f.write("[[tab]]\nfolder = 3\n")
        os.utime(s.menu.menu_path, ns=(2, 2))
        s.menu.reload_menu()
        self.assertEqual(len(s.menu.categories), 4)
        self.assertIn("menu.toml", s.menu.message)


class Controllers(unittest.TestCase):
    """Every verb hostd's controller reader sends moves the menu, and the
    hints name the buttons printed on the controller in hand."""

    def setUp(self):
        self.s = shell()
        self.s.showMenu()
        self.s.menu.setMenuActive(True)

    def test_bumpers_change_category_and_triggers_page(self):
        s = self.s
        nav(s, "next_tab")
        self.assertEqual(s.menu.state()["category"], USERS)
        nav(s, "next_tab")
        nav(s, "prev_tab")
        self.assertEqual(s.menu.state()["category"], USERS)
        nav(s, "prev_tab")
        self.assertEqual(s.menu.state()["category"], APPS)
        before = s.menu.state()["current"]
        nav(s, "page_down")
        self.assertGreater(s.menu.state()["current"], before + 3)
        nav(s, "page_up")
        self.assertEqual(s.menu.state()["current"], before)

    def test_close_and_actions(self):
        s = self.s
        nav(s, "close")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/close", None))
        nav(s, "actions")
        self.assertEqual(s.menu.state()["panel"], "options")

    def test_home_goes_back_to_the_first_app(self):
        s = self.s
        for _ in range(6):
            nav(s, "down")
        nav(s, "next_tab")
        nav(s, "next_tab")
        nav(s, "home")
        self.assertEqual((s.menu.state()["category"], s.menu.current_row().title, s.menu.state()["filter"]), (APPS, "Steam", ""))
        s.menu.set_filter("dol")
        nav(s, "home")
        self.assertEqual((s.menu.state()["filter"], s.menu.current_row().title), ("", "Steam"))

    def test_the_hints_follow_the_controller_in_hand(self):
        s = self.s
        want = {"xbox": ["A", "Menu", "Y"], "playstation": ["✕", "Options", "△"],
                "nintendo": ["A", "+", "X"], "wii": ["A", "+", "2"], "generic": ["A", "Menu", "Y"]}
        for family, buttons in want.items():
            nav(s, "up", family)
            self.assertEqual([h["button"] for h in s.menu.hintList], buttons, family)
        nav(s, "up", "not-a-family")  # an unknown one falls back to the generic labels
        self.assertEqual(s.menu.hintList[0]["button"], "A")
        nav(s, "up", None)  # as does a missing or odd one
        self.assertEqual(s.menu.hintList[0]["button"], "A")

    def test_a_controller_does_nothing_while_the_menu_is_away(self):
        s = shell()
        nav(s, "next_tab")
        self.assertEqual(s.menu.state()["category"], APPS)


WII1, WII2 = "057e:0306-hidraw3", "057e:0306-hidraw4"


def point(s, device, x, y, visible=True, down=False, name="", colour=""):
    s.on_event({"type": "display.pointer", "data": {"x": x, "y": y, "visible": visible, "down": down, "family": "wii",
                                                    "device": device, "name": name, "colour": colour}})


class SeveralPointers(unittest.TestCase):
    """Every pointing device has a pointer of its own: none moves, hides or
    fades another's, and each says whose it is."""

    def setUp(self):
        self.s = shell()
        self.s.showMenu()
        self.s.menu.setMenuActive(True)
        self.s.pointers.core.devices = [
            {"id": "mouse-1", "kind": "pointer", "label": "Mouse", "colour": "blue", "pointer": True},
            {"id": WII1, "kind": "gamepad", "label": "Wii Remote 1", "colour": "orange", "pointer": True},
            {"id": WII2, "kind": "gamepad", "label": "Mia", "colour": "violet", "pointer": True}]

    def by_id(self):
        return {p["id"]: p for p in self.s.pointers.pointers}

    def test_each_device_has_its_own_pointer(self):
        s = self.s
        s.pointers.mouseAt(90, 90, 1000, 1000)
        s.pointers.mouseAt(100, 100, 1000, 1000)
        point(s, WII1, 0.2, 0.3)
        point(s, WII2, 0.8, 0.6, down=True)
        p = self.by_id()
        self.assertEqual({k: (v["x"], v["y"], v["visible"], v["colour"]) for k, v in p.items()}, {
            "mouse": (0.1, 0.1, True, "blue"), WII1: (0.2, 0.3, True, "orange"), WII2: (0.8, 0.6, True, "violet")})
        self.assertEqual((p[WII1]["name"], p[WII2]["name"], p[WII2]["down"], p["mouse"]["down"]),
                         ("Wii Remote 1", "Mia", True, False))
        # One remote moving, going or clicking leaves the others as they were.
        point(s, WII1, 0.25, 0.35)
        point(s, WII2, 0.0, 0.0, visible=False)
        p = self.by_id()
        self.assertEqual((p["mouse"]["x"], p["mouse"]["visible"], p[WII1]["x"], p[WII2]["visible"]), (0.1, True, 0.25, False))

    def test_the_mouse_fading_or_a_key_leaves_the_remotes_pointing(self):
        s = self.s
        s.pointers.mouseAt(90, 90, 1000, 1000)
        s.pointers.mouseAt(100, 100, 1000, 1000)
        point(s, WII1, 0.2, 0.3)
        s.pointers._fade.timeout.emit()
        self.assertEqual((self.by_id()["mouse"]["visible"], self.by_id()[WII1]["visible"]), (False, True))
        s.pointers.mouseAt(110, 110, 1000, 1000)
        s.menu.key("Key_Down", "", False)
        self.assertEqual((self.by_id()["mouse"]["visible"], self.by_id()[WII1]["visible"]), (False, True))
        self.assertFalse(s.pointers._fade.isActive())

    def test_colours_tell_them_apart_only_when_there_are_several(self):
        s = self.s
        point(s, WII1, 0.2, 0.3)
        self.assertEqual(self.by_id()[WII1]["colour"], "orange")
        alone = [d for d in s.pointers.core.devices if d["id"] != WII2 and d["kind"] != "pointer"]  # alone: plain
        s.pointers.core.devices = alone
        s.pointers.pointerChanged.emit()
        self.assertEqual(self.by_id()[WII1]["colour"], "")

    def test_a_remote_button_going_down_clicks_once_where_it_points(self):
        s = self.s
        pressed = []
        s.pointers.pressed.connect(pressed.append)
        point(s, WII1, 0.2, 0.3)
        point(s, WII1, 0.2, 0.3, down=True)
        point(s, WII1, 0.21, 0.3, down=True)  # held and moved: not another click
        point(s, WII1, 0.21, 0.3)
        point(s, WII2, 0.5, 0.5, visible=False, down=True)  # pointing away: nothing to click
        point(s, WII1, 0.21, 0.3, down=True)
        self.assertEqual(pressed, [WII1, WII1])

    def test_a_remote_a_button_clicks_where_it_points_not_the_highlighted_row(self):
        s = self.s
        nav(s, "choose", "wii")  # no pointer showing: it opens the highlighted row
        self.assertEqual(s.api.sent[-1][1], "/v1/instances/steam/switch")
        s.api.sent.clear()
        point(s, WII1, 0.2, 0.3)
        s.on_event({"type": "display.nav", "data": {"what": "choose", "family": "wii", "device": WII1}})
        self.assertEqual(s.api.sent, [])  # the click comes from pointerPressed
        nav(s, "down", "wii")  # its other buttons still move the highlight
        self.assertEqual(s.menu.current_row().title, "Chromium")

    def test_hover_is_each_pointers_own_and_underlines_in_its_colour(self):
        s = self.s
        s.pointers.mouseAt(90, 90, 1000, 1000)
        s.pointers.mouseAt(100, 100, 1000, 1000)
        s.pointers.hover(1)
        point(s, WII1, 0.2, 0.3)
        s.pointers.pointerHover(WII1, 1)
        point(s, WII2, 0.2, 0.3)
        s.pointers.pointerHover(WII2, 0)
        self.assertEqual(sorted((h["row"], h["colour"]) for h in s.pointers.hovers),
                         [(0, "violet"), (1, "blue"), (1, "orange")])
        point(s, WII1, 0, 0, visible=False)
        self.assertEqual(sorted(h["row"] for h in s.pointers.hovers), [0, 1])
        s.menu.mouseButton("right")  # the mouse's row, not a remote's
        self.assertEqual(s.menu.current_row().title, "Chromium")

    def test_devices_come_from_hostd_and_follow_its_events(self):
        s = self.s
        s.api.reads["/v1/devices"] = [{"id": WII1, "kind": "gamepad", "label": "Dad", "colour": "teal", "pointer": True},
                                      {"id": WII2, "kind": "gamepad", "label": "Mia", "colour": "pink", "pointer": True}]
        s.on_event({"type": "device.changed", "data": {}})
        self.assertEqual([d["label"] for d in s.pointers.deviceList], ["Dad", "Mia"])
        point(s, WII1, 0.2, 0.3)  # a pointer event that carries no name: the device's
        self.assertEqual((self.by_id()[WII1]["name"], self.by_id()[WII1]["colour"]), ("Dad", "teal"))
        s.api.reads["/v1/devices"][0]["label"] = "Granddad"  # renamed: the pointer follows
        s.on_event({"type": "device.changed", "data": {}})
        self.assertEqual(self.by_id()[WII1]["name"], "Granddad")
        del s.api.reads["/v1/devices"]  # an older hostd
        s.on_event({"type": "device.disconnected", "data": {}})
        self.assertEqual(s.pointers.deviceList, [])


class DevicesShown(unittest.TestCase):
    """The strip of connected devices, and the Devices group of Status."""

    def test_the_strip_lists_pointing_and_playing_devices_from_two_up(self):
        s = shell()
        self.assertEqual(s.pointers.strip, [])  # no devices
        s.pointers.core.devices = fixtures.DEVICES[:1]
        self.assertEqual(s.pointers.strip, [])  # one needs no telling apart
        s.pointers.core.devices = fixtures.DEVICES
        self.assertEqual([d["label"] for d in s.pointers.strip], ["Mouse", "Wii Remote 1", "Mia"])  # not the keyboard
        self.assertEqual([d["colour"] for d in s.pointers.strip], ["blue", "orange", "violet"])

    def test_hostd_being_unreachable_keeps_what_was_known(self):
        s = shell()
        s.pointers.core.devices = fixtures.DEVICES

        def down(path):
            raise ConnectionRefusedError(path)
        s.api.get = down
        s.menu.loader.refresh_watched({"/v1/devices"})  # at start, or on an event while hostd restarts: no crash
        self.assertEqual(len(s.pointers.deviceList), 4)

    def test_the_one_in_use_is_marked(self):
        s = shell()
        s.showMenu()
        s.menu.setMenuActive(True)
        s.pointers.core.devices = fixtures.DEVICES
        s.pointers.mouseAt(10, 10, 100, 100)
        s.pointers.mouseAt(20, 20, 100, 100)
        point(s, WII2, 0.5, 0.5)
        self.assertEqual({d["label"]: d["active"] for d in s.pointers.strip},
                         {"Mouse": True, "Wii Remote 1": False, "Mia": True})
        s.pointers._fade.timeout.emit()
        point(s, WII2, 0, 0, visible=False)
        self.assertEqual([d["active"] for d in s.pointers.strip], [False, False, False])

    def test_status_lists_the_devices_and_offers_their_colours(self):
        s = shell()
        s.api.reads["/v1/devices"] = fixtures.DEVICES
        s.menu.refresh(STATUS)
        rows = {r.title: r for r in s.menu.nav.all_rows[STATUS]}
        self.assertIn("Devices", rows)
        self.assertNotIn("Keyboard", rows)
        mia = rows["Mia"]
        self.assertEqual((mia.subtitle, mia.value), ("Wii Remote  ·  player 2  ·  calibrated", "violet"))
        orange = next(r for r in mia.sheet if r.title == "Orange")
        self.assertEqual(orange.call.body, {"type": "device.colour", "args": {"id": WII2, "colour": "orange"}})
        self.assertEqual(next(r for r in mia.sheet if r.value == "now").title, "Violet")
        del s.api.reads["/v1/devices"]  # an older hostd: no group
        s.menu.refresh(STATUS)
        self.assertNotIn("Devices", [r.title for r in s.menu.nav.all_rows[STATUS]])


class Calibrating(unittest.TestCase):
    """A Wii Remote's calibration: the corner to aim at, and how it ended."""

    def setUp(self):
        self.s = shell()
        self.s.pointers.core.devices = fixtures.DEVICES

    def event(self, **data):
        self.s.on_event({"type": "display.calibrate", "data": {"device": WII2, "name": "Mia", "colour": "violet", **data}})

    def test_the_corner_to_aim_at_shows_until_it_is_done(self):
        s = self.s
        self.assertEqual(s.pointers.calibrating, {})
        self.event(state="aim", step=2, corner="bottom left")
        c = s.pointers.calibrating
        self.assertEqual((c["name"], c["step"], c["corner"], c["colour"]), ("Mia", 2, "bottom left", "violet"))
        self.event(state="aim", step=2, corner="bottom left", reason="the remote does not see the sensor bar")
        self.assertIn("sensor bar", s.pointers.calibrating["reason"])
        self.event(state="done", calibration={"W": 4})
        self.assertEqual(s.pointers.calibrating, {})
        self.assertEqual((s.menu.message, s.menu.messageError), ("Mia is calibrated", False))

    def test_cancelled_failed_and_reset(self):
        s = self.s
        self.event(state="aim", step=0, corner="top left")
        self.event(state="cancelled")
        self.assertEqual(s.pointers.calibrating, {})
        self.event(state="failed", reason="those four points are not a screen")
        self.assertEqual((s.menu.message, s.menu.messageError), ("those four points are not a screen", True))
        self.event(state="reset")
        self.assertEqual(s.menu.message, "Mia's calibration is cleared")

    def test_status_offers_calibrating_a_remote_and_not_a_mouse(self):
        s = self.s
        s.api.reads["/v1/devices"] = fixtures.DEVICES
        s.menu.refresh(STATUS)
        rows = {r.title: r for r in s.menu.nav.all_rows[STATUS]}
        self.assertEqual([r.title for r in rows["Mia"].sheet][:2], ["Calibrate", "Reset calibration"])
        self.assertEqual(rows["Mia"].sheet[0].call.body, {"type": "device.calibrate", "args": {"id": WII2}})
        self.assertEqual(rows["Mia"].sheet[1].call.body["args"], {"id": WII2, "reset": True})
        self.assertEqual([r.title for r in rows["Wii Remote 1"].sheet][:1], ["Calibrate"])  # not measured yet: no reset
        self.assertNotIn("Calibrate", [r.title for r in rows["Mouse"].sheet])


class Mouse(unittest.TestCase):
    """The mouse has a pointer: hovering never moves the selection, a click
    opens the row clicked, the wheel moves the selection."""

    def setUp(self):
        self.s = shell()

    def test_moving_shows_the_pointer_and_it_fades(self):
        s = self.s
        self.assertFalse(s.pointers.pointer["visible"])
        s.pointers.mouseAt(900, 270, 1920, 1080)  # where it was when the menu opened: no pointer yet
        self.assertFalse(s.pointers.pointer["visible"])
        s.pointers.mouseAt(901, 270, 1920, 1080)  # a hover with no movement
        self.assertFalse(s.pointers.pointer["visible"])
        s.pointers.mouseAt(960, 270, 1920, 1080)
        p = s.pointers.pointer
        self.assertEqual((p["x"], p["y"], p["visible"], p["source"]), (0.5, 0.25, True, "mouse"))
        self.assertTrue(s.pointers._fade.isActive())
        s.pointers._fade.timeout.emit()  # three seconds without moving
        self.assertFalse(s.pointers.pointer["visible"])

    def test_a_flood_of_moves_is_applied_once_per_frame_and_the_last_one_wins(self):
        s = self.s
        s.pointers.mouse_ms = 50
        s.pointers.mouseAt(0, 0, 1000, 1000)  # the first sample is where it already was
        s.pointers.mouseAt(100, 100, 1000, 1000)  # applied at once
        self.assertEqual(s.pointers.pointer["x"], 0.1)
        for x in range(110, 200, 10):  # a thousand a second: the rest wait their turn
            s.pointers.mouseAt(x, 100, 1000, 1000)
        self.assertEqual(s.pointers.pointer["x"], 0.1)
        self.assertTrue(s.pointers._mouse_timer.isActive())
        s.pointers._mouse_timer.timeout.emit()
        self.assertEqual(s.pointers.pointer["x"], 0.19)  # where it ended up, not where it was

    def test_moving_changes_neither_the_hover_lists_nor_the_strip(self):
        s = self.s
        told = []
        s.pointers.hoversChanged.connect(lambda: told.append("hovers"))
        s.pointers.stripChanged.connect(lambda: told.append("strip"))
        s.pointers.mouseAt(0, 0, 1000, 1000)
        for x in range(100, 140, 10):
            s.pointers.mouseAt(x, 100, 1000, 1000)
        self.assertEqual(told, [])  # nothing under it, no devices: rows and strip are not rebuilt
        s.pointers.hover(1)
        self.assertEqual(told, ["hovers"])
        s.pointers.mouseAt(500, 100, 1000, 1000)
        self.assertEqual(told, ["hovers"])  # moved over the same row

    def test_keys_and_pads_put_it_away_and_a_wii_remote_does_not(self):
        s = self.s
        s.showMenu()
        s.menu.setMenuActive(True)
        s.pointers.mouseAt(90, 90, 1000, 1000)
        s.pointers.mouseAt(100, 100, 1000, 1000)
        self.assertTrue(s.pointers.pointer["visible"])
        s.menu.key("Key_Down", "", False)
        self.assertFalse(s.pointers.pointer["visible"])
        s.pointers.mouseAt(110, 110, 1000, 1000)
        s.on_event({"type": "display.nav", "data": {"what": "down", "family": "xbox"}})
        self.assertFalse(s.pointers.pointer["visible"])
        s.on_event({"type": "display.pointer", "data": {"x": 0.25, "y": 0.5, "visible": True, "down": False, "family": "wii"}})
        s.on_event({"type": "display.nav", "data": {"what": "choose", "family": "wii"}})
        p = s.pointers.pointer
        self.assertEqual((p["x"], p["y"], p["visible"], p["source"]), (0.25, 0.5, True, "wii"))
        self.assertFalse(s.pointers._fade.isActive())  # a Wii pointer shows while it points at the screen
        s.on_event({"type": "display.pointer", "data": {"x": 2, "y": -1, "visible": False, "family": "wii"}})
        self.assertEqual((s.pointers.pointer["visible"], s.pointers.pointer["x"]), (False, 1.0))
        s.on_event({"type": "display.pointer", "data": {"x": "no"}})  # junk is ignored

    def test_a_pointer_event_while_the_menu_is_away_is_ignored(self):
        s = self.s
        s.on_event({"type": "display.pointer", "data": {"x": 0.5, "y": 0.5, "visible": True}})
        self.assertFalse(s.pointers.pointer["visible"])

    def test_the_button_down_fills_it(self):
        s = self.s
        s.pointers.mouseAt(10, 10, 100, 100)
        s.pointers.mouseAt(20, 20, 100, 100)
        s.pointers.mouseDown(True)
        self.assertTrue(s.pointers.pointer["down"])
        s.pointers.mouseDown(False)
        self.assertFalse(s.pointers.pointer["down"])

    def test_clicking_a_row_makes_it_current_and_opens_it(self):
        s = self.s
        s.pointers.hover(1)  # hovering moves nothing
        self.assertEqual(s.menu.current_row().title, "Steam")
        s.menu.clickItem(1, "left")
        self.assertEqual(s.menu.current_row().title, "Chromium")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/chromium/switch", None))

    def test_a_right_click_opens_the_options_of_the_row_under_the_pointer(self):
        s = self.s
        s.pointers.hover(1)
        s.menu.mouseButton("right")
        self.assertEqual((s.menu.state()["panel"], s.menu.current_row().title), ("options", "Chromium"))
        s.menu.mouseButton("back")
        self.assertEqual(s.menu.state()["panel"], "")
        s.pointers.hover(-1)
        s.menu.mouseButton("right")  # over no row: the current one's options
        self.assertEqual((s.menu.state()["panel"], s.menu.current_row().title), ("options", "Chromium"))

    def test_clicks_on_headers_and_during_a_panel_do_nothing(self):
        s = self.s
        s.menu.clickItem(99, "left")
        s.menu.handle("actions")
        sent = len(s.api.sent)
        s.menu.clickItem(2, "left")
        self.assertEqual((len(s.api.sent), s.menu.state()["panel"]), (sent, "options"))

    def test_the_other_buttons(self):
        s = self.s
        s.menu.mouseButton("forward")
        self.assertEqual(s.menu.state()["category"], USERS)
        s.menu.mouseButton("middle")
        self.assertEqual(s.menu.state()["category"], APPS)

    def test_the_wheel(self):
        s = self.s
        s.menu.wheel(0, -120)
        self.assertEqual(s.menu.current_row().title, "Chromium")
        s.menu.wheel(-120, 0)
        self.assertEqual(s.menu.state()["category"], USERS)


class MenuWindow(unittest.TestCase):
    def test_show_hide_and_closed_signal(self):
        s = shell()
        closed = []
        s.menuClosed.connect(lambda: closed.append(True))
        s.showMenu()
        self.assertTrue(s.menu.visible)
        s.menu.handle("actions")
        s.hideMenu()
        self.assertEqual((s.menu.visible, closed, s.menu.state()["panel"]), (False, [True], ""))


def contrast(a, b):
    def lum(hex_color):
        def ch(c):
            return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4
        r, g, bl = (ch(x) for x in background.rgb(hex_color))
        return 0.2126 * r + 0.7152 * g + 0.0722 * bl
    hi, lo = sorted((lum(a), lum(b)), reverse=True)
    return (hi + 0.05) / (lo + 0.05)


class Readability(unittest.TestCase):
    """Text reads on every background the shell can draw: body text at
    4.5:1 at least, muted text at 3:1, on both ends of the gradient."""

    def check(self, look, now, where):
        text, muted = harness.TOKENS["color"]["text"], harness.TOKENS["color"]["text-muted"]
        for end in background.colors(look, now, harness.TOKENS):
            self.assertGreaterEqual(contrast(text, end), 4.5, f"{where}: text on {end}")
            self.assertGreaterEqual(contrast(muted, end), 3.0, f"{where}: muted text on {end}")

    def test_every_month_and_hour(self):
        for month in range(1, 13):
            for hour in range(24):
                now = datetime.datetime(2026, month, 1, hour, 0)
                self.check({"mode": "month", "color": "", "image": ""}, now, f"{now:%b %H}h")

    def test_every_fixed_color(self):
        look = {"mode": "month", "color": "", "image": ""}
        for name, setting in background.choices(look, harness.TOKENS):
            if setting["mode"] == "color":
                self.check(setting, fixtures.NOW, name)

    def test_a_light_color_is_darkened(self):
        self.check({"mode": "color", "color": "#ffffff", "image": ""}, fixtures.NOW, "white")
        _h, light, _s = colorsys.rgb_to_hls(*background.rgb(
            background.colors({"mode": "color", "color": "#ffffff", "image": ""}, fixtures.NOW, harness.TOKENS)[0]))
        self.assertLessEqual(light, background.MAX_LIGHT + 0.01)


class Models(unittest.TestCase):
    def test_same_rows_update_in_place(self):
        s = shell()
        model = s.menu.pages[APPS]
        resets = []
        model.modelReset.connect(lambda: resets.append(1))
        s.menu.refresh(APPS)
        self.assertEqual(resets, [])  # same items: data changed, no reset
        s.api.reads["/v1/instances?all=true"] = []
        s.menu.refresh(APPS)
        self.assertEqual(resets, [1])
        self.assertEqual(s.menu.current_row().title, "Steam")  # the same app, no longer first
        self.assertEqual(titles(s)[:2], ["Games", "Services"])

    def test_roles(self):
        s = shell()
        model = s.menu.pages[APPS]
        names = {bytes(v).decode(): k for k, v in model.roleNames().items()}
        steam = model.index(0)
        self.assertEqual(model.data(steam, names["title"]), "Steam")
        self.assertEqual(model.data(steam, names["appIcon"]), fixtures.APP_ICON)
        self.assertEqual(model.data(steam, names["mark"]), "positive")
        self.assertEqual(model.data(model.index(2), names["kind"]), views.FOLDER)


if __name__ == "__main__":
    unittest.main()
