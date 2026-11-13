"""The shell's behaviour, without drawing: verbs in, state and requests to
hostd out (Shell, the one place input turns into behaviour). And the
background's guarantee: text reads on it at every month and hour."""

import colorsys
import datetime
import unittest

import fixtures
import harness  # noqa: F401 - the Qt application the shell's timers need
from hostd_shell import background, views
from hostd_shell.shell import APPS, SETTINGS, STATUS, USERS, Shell


def shell():
    s = Shell(fixtures.FakeAPI(), harness.TOKENS, now=lambda: fixtures.NOW, look_path="/nonexistent/ui.toml")
    for c in range(4):
        s.refresh(c)
    return s


def titles(s):
    return [r.title for r in s.page().rows]


class Moving(unittest.TestCase):
    def test_starts_on_the_first_item_skipping_headers(self):
        s = shell()
        self.assertEqual(s.state()["category"], APPS)
        self.assertEqual(s.current_row().title, "Steam")
        self.assertEqual(s.property("section"), "Running")
        self.assertEqual((s.offset()), (1, 0))

    def test_down_skips_headers_and_stops_at_the_end(self):
        s = shell()
        s.handle("down")
        s.handle("down")  # over the "Apps" header
        self.assertEqual(s.current_row().title, "Steam")
        self.assertEqual(s.property("section"), "Apps")
        self.assertEqual(s.offset(), (2, 2))
        for _ in range(30):
            s.handle("down")
        self.assertEqual(s.current_row().title, "VLC")
        for _ in range(30):
            s.handle("up")
        self.assertEqual(s.state()["current"], 1)

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
        self.assertEqual(titles(s), ["Dolphin"])
        self.assertTrue(s.key("Key_Backspace", "", False))
        self.assertEqual(s.state()["filter"], "do")
        s.handle("back")
        self.assertEqual(s.state()["filter"], "")
        self.assertIn("Running", titles(s))

    def test_typing_elsewhere_is_ignored(self):
        s = shell()
        s.handle("right")
        self.assertFalse(s.key("Key_A", "a", False))
        self.assertEqual(s.state()["filter"], "")


class Hints(unittest.TestCase):
    def test_hints_follow_the_state_and_the_device(self):
        s = shell()
        self.assertEqual([h["what"] for h in s._hints()], ["open", "options", "close", "category", "back"])
        s.on_event({"type": "display.nav", "data": {"what": "down"}})  # the menu is not in front: ignored
        self.assertEqual(s.current_row().title, "Steam")
        s.showMenu()
        s.setMenuActive(True)
        s.on_event({"type": "display.nav", "data": {"what": "down"}})
        self.assertEqual(s.current_row().title, "Chromium")
        self.assertEqual([h["button"] for h in s._hints()][:2], ["A", "Menu"])


def nav(s, what, family="xbox"):
    s.on_event({"type": "display.nav", "data": {"what": what, "family": family}})


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
        want = {"xbox": ["A", "Menu", "Y", "◀ ▶", "B"], "playstation": ["✕", "Options", "△", "◀ ▶", "○"],
                "nintendo": ["A", "+", "X", "◀ ▶", "B"], "wii": ["A", "+", "2", "◀ ▶", "B"],
                "generic": ["A", "Menu", "Y", "◀ ▶", "B"]}
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
        s.api.reads["/v1/instances"] = []
        s.refresh(APPS)
        self.assertEqual(resets, [1])
        self.assertNotIn("Running", titles(s))
        self.assertEqual(s.current_row().title, "Steam")  # kept the place it could

    def test_roles(self):
        s = shell()
        model = s.pages[APPS]
        names = {bytes(v).decode(): k for k, v in model.roleNames().items()}
        steam = model.index(1)
        self.assertEqual(model.data(steam, names["title"]), "Steam")
        self.assertEqual(model.data(steam, names["appIcon"]), fixtures.APP_ICON)
        self.assertTrue(model.data(steam, names["mono"]))
        self.assertEqual(model.data(model.index(0), names["kind"]), views.HEADER)


if __name__ == "__main__":
    unittest.main()
