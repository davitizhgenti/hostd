"""The shell's behaviour, without drawing: verbs in, state and requests to
hostd out (Shell, the one place input turns into behaviour). And the
background's guarantee: text reads on it at every month and hour."""

import colorsys
import datetime
import os
import tempfile
import unittest

import fixtures
import harness  # noqa: F401 - the Qt application the shell's timers need
from hostd_shell import background, views
from hostd_shell.shell import APPS, SETTINGS, STATUS, USERS, Shell


def shell(menu=None):
    """A shell on the fixture data; menu: the menu file's text (tabs)."""
    menu_path = "/nonexistent/menu.toml"
    if menu is not None:
        menu_path = os.path.join(tempfile.mkdtemp(), "menu.toml")
        with open(menu_path, "w") as f:
            f.write(menu)
    s = Shell(fixtures.FakeAPI(), harness.TOKENS, now=lambda: fixtures.NOW,
              look_path=os.path.join(tempfile.mkdtemp(), "ui.toml"), menu_path=menu_path)
    for c in s.cats:
        s.refresh(c.key)
    return s


def titles(s):
    return [r.title for r in s.page().rows]


class Moving(unittest.TestCase):
    def test_starts_on_the_first_item(self):
        s = shell()
        self.assertEqual(s.state()["category"], APPS)
        self.assertEqual(s.current_row().title, "Steam")
        self.assertEqual(s.property("section"), "")  # Apps has no headers
        self.assertEqual((s.offset()), (0, 0))

    def test_running_apps_folders_then_apps_once_each(self):
        s = shell()
        self.assertEqual(titles(s)[:5], ["Steam", "Chromium", "Games", "Services", "Kodi"])
        self.assertEqual(titles(s).count("Steam"), 1)
        self.assertEqual([r.mark for r in s.page().rows[:3]], ["positive", "warning", ""])

    def test_down_stops_at_the_end(self):
        s = shell()
        s.handle("down")
        s.handle("down")
        self.assertEqual(s.current_row().title, "Games")
        self.assertEqual(s.offset(), (0, 2))
        for _ in range(30):
            s.handle("down")
        self.assertEqual(s.current_row().title, "VLC")
        for _ in range(30):
            s.handle("up")
        self.assertEqual(s.state()["current"], 0)

    def test_left_and_right_change_category_and_wrap(self):
        s = shell()
        for want in (USERS, SETTINGS, STATUS, APPS):
            s.handle("right")
            self.assertEqual(s.state()["category"], want)
        s.handle("left")
        self.assertEqual(s.state()["category"], STATUS)

    def test_each_category_remembers_its_place(self):
        s = shell()
        s.handle("down")
        s.handle("right")
        s.handle("left")
        self.assertEqual(s.current_row().title, "Chromium")


class Choosing(unittest.TestCase):
    def test_choose_sends_its_call(self):
        s = shell()
        s.handle("activate")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/switch", None))

    def test_options_panel(self):
        s = shell()
        s.handle("actions")
        self.assertEqual(s.state()["panel"], "options")
        self.assertEqual([r.title for r in s.options_model.rows], ["Bring to front", "Close", "Big Picture", "Dota 2"])
        s.handle("down")
        s.handle("down")
        s.handle("activate")  # Big Picture
        self.assertEqual(s.state()["panel"], "")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/apps/steam/open", {"action": "bigpicture"}))

    def test_options_close_with_back_and_left(self):
        s = shell()
        for verb in ("back", "left", "actions"):
            s.handle("actions")
            s.handle(verb)
            self.assertEqual(s.state()["panel"], "", verb)
            self.assertEqual(s.state()["category"], APPS, verb)

    def test_question_defaults_to_no(self):
        s = shell()
        s.handle("right")
        s.handle("down")  # guest
        s.handle("activate")
        self.assertIn("guest", s.state()["question"])
        self.assertFalse(s.state()["yes"])
        s.handle("activate")  # No: nothing sent
        self.assertEqual(s.api.sent, [])
        s.handle("activate")
        s.handle("right")
        s.handle("activate")  # Yes
        self.assertEqual(s.api.sent[-1][2], {"type": "seat.switch", "args": {"user": "guest"}})

    def test_volume_panel_changes_with_up_and_down(self):
        s = shell()
        s.handle("right")
        s.handle("right")
        self.assertEqual(s.current_row().title, "Volume")
        s.handle("activate")
        self.assertEqual(s.state()["panel"], "level")
        s.handle("up")
        self.assertEqual(s.api.sent[-1][2], {"type": "audio.volume.set", "args": {"percent": "+5"}})
        s.handle("down")
        self.assertEqual(s.api.sent[-1][2], {"type": "audio.volume.set", "args": {"percent": "-5"}})
        s.handle("left")  # left closes the panel, it does not change category
        self.assertEqual((s.state()["panel"], s.state()["category"]), ("", SETTINGS))

    def test_back_goes_to_apps_then_to_the_app(self):
        s = shell()
        s.handle("right")
        s.handle("back")
        self.assertEqual(s.state()["category"], APPS)
        s.handle("back")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/windows/back", None))

    def test_close_only_on_running_apps(self):
        s = shell()
        s.handle("close")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/close", None))
        s.handle("down")
        s.handle("down")  # a catalog app: nothing to close
        sent = len(s.api.sent)
        s.handle("close")
        self.assertEqual(len(s.api.sent), sent)


class Filtering(unittest.TestCase):
    def test_typing_filters_apps_and_back_clears(self):
        s = shell()
        for ch in "dol":
            self.assertTrue(s.key("Key_" + ch.upper(), ch, False))
        # Dolphin, and the games in its folder (the folder is searched too).
        self.assertEqual(titles(s), ["Dolphin", "Mario Kart Wii", "Super Mario Galaxy"])
        self.assertTrue(s.key("Key_Backspace", "", False))
        self.assertEqual(s.state()["filter"], "do")
        s.handle("back")
        self.assertEqual(s.state()["filter"], "")
        self.assertIn("Games", titles(s))

    def test_typing_elsewhere_is_ignored(self):
        s = shell()
        s.handle("right")
        self.assertFalse(s.key("Key_A", "a", False))
        self.assertEqual(s.state()["filter"], "")


class Hints(unittest.TestCase):
    def test_hints_name_only_what_the_row_does(self):
        s = shell()
        what = lambda: [h["what"] for h in s._hints()]  # noqa: E731
        self.assertEqual(what(), ["open", "options", "close"])  # Steam, running
        s.handle("down")
        s.handle("down")
        self.assertEqual(what(), ["open"])  # the Games folder
        s.handle("activate")
        self.assertEqual(what(), ["open", "back"])  # a folder open: back goes up
        s.handle("home")
        s.handle("down")
        s.handle("down")
        s.handle("down")
        s.handle("activate")  # Services
        self.assertEqual(what(), ["options", "back"])  # a service acts through its options
        s.handle("right")
        s.handle("down")
        self.assertEqual(what(), ["choose"])  # Users: guest

    def test_hints_follow_the_device(self):
        s = shell()
        s.on_event({"type": "display.nav", "data": {"what": "down"}})  # the menu is not in front: ignored
        self.assertEqual(s.current_row().title, "Steam")
        s.showMenu()
        s.setMenuActive(True)
        s.on_event({"type": "display.nav", "data": {"what": "down"}})
        self.assertEqual(s.current_row().title, "Chromium")
        self.assertEqual([h["button"] for h in s._hints()][:2], ["A", "Menu"])


def nav(s, what, family="xbox"):
    s.on_event({"type": "display.nav", "data": {"what": what, "family": family}})


class FolderNavigation(unittest.TestCase):
    """Folders open like lists of their own; back goes up."""

    def setUp(self):
        self.s = shell()

    def go_to_games(self):
        s = self.s
        for _ in range(40):
            if s.current_row().kind == "folder":
                break
            s.handle("down")
        self.assertEqual(s.current_row().title, "Games")
        s.handle("activate")

    def test_open_drill_in_and_back_out(self):
        s = self.s
        self.go_to_games()
        self.assertEqual((list(s.property("folderPath")), s.property("section")), (["Games"], "Games"))
        self.assertEqual(titles(s)[:2], ["Dolphin", "Steam"])
        self.assertEqual(s.current_row().title, "Dolphin")
        s.handle("activate")
        self.assertEqual(titles(s)[:2], ["Wii", "Dolphin"])
        s.handle("activate")  # Wii
        self.assertEqual(s.property("section"), "Games / Dolphin / Wii")
        self.assertEqual(s.current_row().title, "Mario Kart Wii")
        s.handle("back")  # up to Games / Dolphin, on Wii
        self.assertEqual((list(s.property("folderPath")), s.current_row().title), (["Games", "Dolphin"], "Wii"))
        s.handle("back")
        s.handle("back")
        self.assertEqual((list(s.property("folderPath")), s.current_row().title), ([], "Games"))
        self.assertEqual(s.api.sent, [])  # folders never asked hostd to do anything

    def test_choosing_an_app_in_a_folder_opens_it(self):
        s = self.s
        self.go_to_games()
        s.handle("down")  # the Steam folder
        s.handle("activate")
        self.assertEqual(titles(s)[:2], ["Dota 2", "Steam Link"])
        s.handle("down")
        s.handle("activate")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/apps/steam-launcher/open", None))

    def test_leaving_apps_and_home_close_folders(self):
        s = self.s
        self.go_to_games()
        s.handle("right")
        s.handle("left")
        self.assertEqual(list(s.property("folderPath")), [])
        self.go_to_games()
        s.handle("home")
        self.assertEqual((list(s.property("folderPath")), s.property("category")), ([], 0))

    def test_back_at_the_top_still_goes_back(self):
        self.s.handle("back")
        self.assertEqual(self.s.api.sent[-1], ("POST", "/v1/windows/back", None))

    def test_a_filter_searches_inside_folders(self):
        s = self.s
        for ch in "mario":
            s.key("Key_" + ch.upper(), ch, False)
        found = sorted(r.title for r in views.visible(s.all_rows[APPS], s._filter))
        self.assertEqual(found, ["Mario Kart Wii", "Super Mario Galaxy"])


class Services(unittest.TestCase):
    """Background apps are in Services; they start and stop through their
    options, never on one press."""

    def open_services(self, s):
        while s.current_row().title != "Services":
            s.handle("down")
        s.handle("activate")

    def test_states_and_options(self):
        s = shell()
        self.open_services(s)
        self.assertEqual([(r.title, r.badge) for r in s.page().rows if not r.under],
                         [("Home Assistant", "running"), ("Syncthing", "stopped"), ("Pi-hole", "failed")])
        s.handle("activate")  # opens the options: nothing sent
        self.assertEqual((s.state()["panel"], s.api.sent), ("options", []))
        self.assertEqual([r.title for r in s.options_model.rows], ["Stop", "Restart"])
        s.handle("down")
        s.handle("activate")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/homeassistant/restart", None))

    def test_delete_does_not_stop_a_service(self):
        s = shell()
        self.open_services(s)
        s.handle("close")
        self.assertEqual(s.api.sent, [])

    def test_a_failed_one_starts_again(self):
        s = shell()
        self.open_services(s)
        s.handle("down")
        s.handle("down")
        s.handle("activate")
        s.handle("activate")  # Start
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/apps/pihole/open", None))


class Tabs(unittest.TestCase):
    """Folders the menu file marks are categories of their own."""

    def test_the_marked_folders_are_tabs_after_apps(self):
        s = shell(fixtures.MENU)
        self.assertEqual([c["name"] for c in s.property("categories")],
                         ["Apps", "Games", "Services", "Users", "Settings", "Status"])
        self.assertEqual(s.property("categories")[1]["icon"], "gamepad-2")
        self.assertEqual(titles(s)[:3], ["Steam", "Chromium", "Kodi"])  # Apps has no Games or Services folder
        s.handle("right")
        self.assertEqual((s.state()["category"], s.property("category")), ("tab:Games", 1))
        # A tiles tab: every game in it, in groups each headed by its launcher.
        self.assertEqual((s.property("view"), titles(s), s.property("section")),
                         ("tiles", ["Dolphin", "Mario Kart Wii", "Super Mario Galaxy", "Steam", "Dota 2",
                                    "Other games", "Steam Link"], ""))
        self.assertEqual([r.kind for r in s.page().rows][:4], ["launcher", "item", "item", "launcher"])
        s.handle("right")
        self.assertEqual(titles(s), ["Home Assistant", "Syncthing", "Pi-hole"])

    def test_back_stops_at_the_tab(self):
        s = shell('[[tab]]\nfolder = "Games"\n')  # a list tab: its folders open
        s.handle("right")
        s.handle("activate")  # Dolphin
        self.assertEqual((s.property("section"), titles(s)), ("Games / Dolphin", ["Wii", "Dolphin"]))
        s.handle("back")
        self.assertEqual((s.state()["category"], s.current_row().title), ("tab:Games", "Dolphin"))
        s.handle("back")  # at the tab's top: back to Apps, as from any category
        self.assertEqual(s.state()["category"], APPS)

    def test_moving_through_groups_and_opening_a_launcher(self):
        s = shell(fixtures.MENU)
        s.handle("right")
        self.assertEqual((s.current_row().title, s.property("focus")["subtitle"]), ("Dolphin", "2 games"))
        s.handle("down")  # Mario Kart Wii: its banner and details on the right
        focus = s.property("focus")
        self.assertEqual((focus["title"], focus["banner"], focus["details"], focus["launcher"]),
                         ("Mario Kart Wii", "file://" + fixtures.BANNER_A, "Last played 12 February 2026 · 3.1 GB", False))
        self.assertEqual(s.offset(), (1, 0))  # the launcher above, as high as a header
        for _ in range(4):
            s.handle("down")  # over the Other games label, onto Steam Link
        self.assertEqual((s.current_row().title, s.offset()), ("Steam Link", (3, 3)))
        s.handle("up")
        s.handle("up")  # Steam, the launcher
        self.assertEqual(s.current_row().title, "Steam")
        s.handle("activate")  # running: brought to the front
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/switch", None))
        self.assertEqual([h["what"] for h in s._hints()], ["open", "options", "close"])

    def test_typing_in_a_tab_searches_every_app(self):
        s = shell(fixtures.MENU)
        s.handle("right")
        for ch in "kodi":
            self.assertTrue(s.key("Key_" + ch.upper(), ch, False))
        self.assertEqual(titles(s), ["Kodi"])
        s.handle("right")  # leaving clears it
        self.assertEqual((s.state()["filter"], s.state()["category"]), ("", "tab:Services"))

    def test_changing_the_file_changes_the_tabs(self):
        s = shell(fixtures.MENU)
        changed = []
        s.categoriesChanged.connect(lambda: changed.append(1))
        s.handle("right")
        s.handle("right")  # Services
        with open(s.menu_path, "w") as f:
            f.write('[[tab]]\nfolder = "Games"\n')
        os.utime(s.menu_path, ns=(1, 1))  # a change the clock cannot miss
        s.reload_menu()
        self.assertEqual(changed, [1])
        self.assertEqual([c["name"] for c in s.property("categories")], ["Apps", "Games", "Users", "Settings", "Status"])
        self.assertEqual(s.state()["category"], APPS)  # its tab went: back to Apps
        self.assertEqual(s.property("categories")[1]["icon"], "folder")
        self.assertIn("Services", titles(s))
        with open(s.menu_path, "w") as f:
            f.write("[[tab]]\nfolder = 3\n")
        os.utime(s.menu_path, ns=(2, 2))
        s.reload_menu()
        self.assertEqual(len(s.property("categories")), 4)
        self.assertIn("menu.toml", s.property("message"))


class Controllers(unittest.TestCase):
    """Every verb hostd's controller reader sends moves the menu, and the
    hints name the buttons printed on the controller in hand."""

    def setUp(self):
        self.s = shell()
        self.s.showMenu()
        self.s.setMenuActive(True)

    def test_bumpers_change_category_and_triggers_page(self):
        s = self.s
        nav(s, "next_tab")
        self.assertEqual(s.state()["category"], USERS)
        nav(s, "next_tab")
        nav(s, "prev_tab")
        self.assertEqual(s.state()["category"], USERS)
        nav(s, "prev_tab")
        self.assertEqual(s.state()["category"], APPS)
        before = s.state()["current"]
        nav(s, "page_down")
        self.assertGreater(s.state()["current"], before + 3)
        nav(s, "page_up")
        self.assertEqual(s.state()["current"], before)

    def test_close_and_actions(self):
        s = self.s
        nav(s, "close")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/close", None))
        nav(s, "actions")
        self.assertEqual(s.state()["panel"], "options")

    def test_home_goes_back_to_the_first_app(self):
        s = self.s
        for _ in range(6):
            nav(s, "down")
        nav(s, "next_tab")
        nav(s, "next_tab")
        nav(s, "home")
        self.assertEqual((s.state()["category"], s.current_row().title, s.state()["filter"]), (APPS, "Steam", ""))
        s.set_filter("dol")
        nav(s, "home")
        self.assertEqual((s.state()["filter"], s.current_row().title), ("", "Steam"))

    def test_the_hints_follow_the_controller_in_hand(self):
        s = self.s
        want = {"xbox": ["A", "Menu", "Y"], "playstation": ["✕", "Options", "△"],
                "nintendo": ["A", "+", "X"], "wii": ["A", "+", "2"], "generic": ["A", "Menu", "Y"]}
        for family, buttons in want.items():
            nav(s, "up", family)
            self.assertEqual([h["button"] for h in s._hints()], buttons, family)
        nav(s, "up", "not-a-family")  # an unknown one falls back to the generic labels
        self.assertEqual(s._hints()[0]["button"], "A")
        nav(s, "up", None)  # as does a missing or odd one
        self.assertEqual(s._hints()[0]["button"], "A")

    def test_a_controller_does_nothing_while_the_menu_is_away(self):
        s = shell()
        nav(s, "next_tab")
        self.assertEqual(s.state()["category"], APPS)


class Mouse(unittest.TestCase):
    """The mouse has a pointer: hovering never moves the selection, a click
    opens the row clicked, the wheel moves the selection."""

    def setUp(self):
        self.s = shell()

    def test_moving_shows_the_pointer_and_it_fades(self):
        s = self.s
        self.assertFalse(s.property("pointer")["visible"])
        s.mouseAt(900, 270, 1920, 1080)  # where it was when the menu opened: no pointer yet
        self.assertFalse(s.property("pointer")["visible"])
        s.mouseAt(901, 270, 1920, 1080)  # a hover with no movement
        self.assertFalse(s.property("pointer")["visible"])
        s.mouseAt(960, 270, 1920, 1080)
        p = s.property("pointer")
        self.assertEqual((p["x"], p["y"], p["visible"], p["source"]), (0.5, 0.25, True, "mouse"))
        self.assertTrue(s.pointer_timer.isActive())
        s.pointer_timer.timeout.emit()  # three seconds without moving
        self.assertFalse(s.property("pointer")["visible"])

    def test_keys_and_pads_put_it_away_and_a_wii_remote_does_not(self):
        s = self.s
        s.showMenu()
        s.setMenuActive(True)
        s.mouseAt(90, 90, 1000, 1000)
        s.mouseAt(100, 100, 1000, 1000)
        self.assertTrue(s.property("pointer")["visible"])
        s.key("Key_Down", "", False)
        self.assertFalse(s.property("pointer")["visible"])
        s.mouseAt(110, 110, 1000, 1000)
        s.on_event({"type": "display.nav", "data": {"what": "down", "family": "xbox"}})
        self.assertFalse(s.property("pointer")["visible"])
        s.on_event({"type": "display.pointer", "data": {"x": 0.25, "y": 0.5, "visible": True, "down": False, "family": "wii"}})
        s.on_event({"type": "display.nav", "data": {"what": "choose", "family": "wii"}})
        p = s.property("pointer")
        self.assertEqual((p["x"], p["y"], p["visible"], p["source"]), (0.25, 0.5, True, "wii"))
        self.assertFalse(s.pointer_timer.isActive())  # a Wii pointer shows while it points at the screen
        s.on_event({"type": "display.pointer", "data": {"x": 2, "y": -1, "visible": False, "family": "wii"}})
        self.assertEqual((s.property("pointer")["visible"], s.property("pointer")["x"]), (False, 1.0))
        s.on_event({"type": "display.pointer", "data": {"x": "no"}})  # junk is ignored

    def test_a_pointer_event_while_the_menu_is_away_is_ignored(self):
        s = self.s
        s.on_event({"type": "display.pointer", "data": {"x": 0.5, "y": 0.5, "visible": True}})
        self.assertFalse(s.property("pointer")["visible"])

    def test_the_button_down_fills_it(self):
        s = self.s
        s.mouseAt(10, 10, 100, 100)
        s.mouseAt(20, 20, 100, 100)
        s.mouseDown(True)
        self.assertTrue(s.property("pointer")["down"])
        s.mouseDown(False)
        self.assertFalse(s.property("pointer")["down"])

    def test_clicking_a_row_makes_it_current_and_opens_it(self):
        s = self.s
        s.hover(1)  # hovering moves nothing
        self.assertEqual(s.current_row().title, "Steam")
        s.clickItem(1, "left")
        self.assertEqual(s.current_row().title, "Chromium")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/chromium/switch", None))

    def test_a_right_click_opens_the_options_of_the_row_under_the_pointer(self):
        s = self.s
        s.hover(1)
        s.mouseButton("right")
        self.assertEqual((s.state()["panel"], s.current_row().title), ("options", "Chromium"))
        s.mouseButton("back")
        self.assertEqual(s.state()["panel"], "")
        s.hover(-1)
        s.mouseButton("right")  # over no row: the current one's options
        self.assertEqual((s.state()["panel"], s.current_row().title), ("options", "Chromium"))

    def test_clicks_on_headers_and_during_a_panel_do_nothing(self):
        s = self.s
        s.clickItem(99, "left")
        s.handle("actions")
        sent = len(s.api.sent)
        s.clickItem(2, "left")
        self.assertEqual((len(s.api.sent), s.state()["panel"]), (sent, "options"))

    def test_the_other_buttons(self):
        s = self.s
        s.mouseButton("forward")
        self.assertEqual(s.state()["category"], USERS)
        s.mouseButton("middle")
        self.assertEqual(s.state()["category"], APPS)

    def test_the_wheel(self):
        s = self.s
        s.wheel(0, -120)
        self.assertEqual(s.current_row().title, "Chromium")
        s.wheel(-120, 0)
        self.assertEqual(s.state()["category"], USERS)


class MenuWindow(unittest.TestCase):
    def test_show_hide_and_closed_signal(self):
        s = shell()
        closed = []
        s.menuClosed.connect(lambda: closed.append(True))
        s.showMenu()
        self.assertTrue(s.property("menuVisible"))
        s.handle("actions")
        s.hideMenu()
        self.assertEqual((s.property("menuVisible"), closed, s.state()["panel"]), (False, [True], ""))


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
        h, light, s = colorsys.rgb_to_hls(*background.rgb(
            background.colors({"mode": "color", "color": "#ffffff", "image": ""}, fixtures.NOW, harness.TOKENS)[0]))
        self.assertLessEqual(light, background.MAX_LIGHT + 0.01)


class Models(unittest.TestCase):
    def test_same_rows_update_in_place(self):
        s = shell()
        model = s.pages[APPS]
        resets = []
        model.modelReset.connect(lambda: resets.append(1))
        s.refresh(APPS)
        self.assertEqual(resets, [])  # same items: data changed, no reset
        s.api.reads["/v1/instances?all=true"] = []
        s.refresh(APPS)
        self.assertEqual(resets, [1])
        self.assertEqual(s.current_row().title, "Steam")  # the same app, no longer first
        self.assertEqual(titles(s)[:2], ["Games", "Services"])

    def test_roles(self):
        s = shell()
        model = s.pages[APPS]
        names = {bytes(v).decode(): k for k, v in model.roleNames().items()}
        steam = model.index(0)
        self.assertEqual(model.data(steam, names["title"]), "Steam")
        self.assertEqual(model.data(steam, names["appIcon"]), fixtures.APP_ICON)
        self.assertEqual(model.data(steam, names["mark"]), "positive")
        self.assertEqual(model.data(model.index(2), names["kind"]), views.FOLDER)


if __name__ == "__main__":
    unittest.main()
