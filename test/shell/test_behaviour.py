"""The shell's behaviour, without drawing: verbs in, state and requests to
hostd out (Shell, the one place input turns into behaviour). And the
background's guarantee: text reads on it at every month and hour."""

import colorsys
import datetime
import unittest

import fixtures
import harness  # noqa: F401 - the Qt application the shell's timers need
from hostd_shell import background, views
from hostd_shell.glide import Glide
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
        s.handle("down")  # over the "Apps" header, onto the Games folder
        self.assertEqual(s.current_row().title, "Games")
        self.assertEqual(s.property("section"), "Apps")
        self.assertEqual(s.offset(), (2, 2))
        s.handle("down")
        self.assertEqual(s.current_row().title, "Steam")
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
        # Dolphin, and the games in its folder (the folder is searched too).
        self.assertEqual(titles(s), ["Dolphin", "Mario Kart Wii", "Super Mario Galaxy"])
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
        self.assertEqual([r.title for r in s.page().rows], ["Games", "Dolphin", "Steam"])
        self.assertEqual(s.current_row().title, "Dolphin")
        s.handle("activate")
        self.assertEqual([r.title for r in s.page().rows], ["Games / Dolphin", "Wii", "Dolphin"])
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
        self.assertEqual(s.current_row().title, "Steam")
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
        found = sorted(r.title for r in views.visible(s.all_rows[0], s._filter))
        self.assertEqual(found, ["Mario Kart Wii", "Super Mario Galaxy"])


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


def path(g, points, t0=0, dt=10):
    """Feeds positions dt ms apart; returns the verbs."""
    verbs = []
    for i, (x, y) in enumerate(points):
        verbs += g.motion(x, y, t0 + i * dt)
    return verbs


def line(x0, y0, x1, y1, n):
    return [(x0 + (x1 - x0) * i / n, y0 + (y1 - y0) * i / n) for i in range(n + 1)]


class Gliding(unittest.TestCase):
    """The mouse with no pointer: movement is felt, not aimed."""

    def g(self):
        return Glide(step=40, cross=100, rest_ms=250, floor=60, burst=3)

    def test_one_push_one_item(self):
        self.assertEqual(path(self.g(), line(500, 500, 500, 545, 9)), ["down"])
        self.assertEqual(path(self.g(), line(500, 500, 500, 455, 9)), ["up"])

    def test_a_long_push_several(self):
        self.assertEqual(path(self.g(), line(500, 300, 500, 470, 17)), ["down"] * 4)

    def test_a_resting_hand_does_nothing(self):
        # 1 px every 50 ms: 20 px/s, under the floor, for ten seconds.
        self.assertEqual(path(self.g(), [(500, 500 + i) for i in range(200)], dt=50), [])

    def test_a_diagonal_moves_one_way(self):
        verbs = path(self.g(), line(500, 500, 540, 590, 9))
        self.assertEqual(set(verbs), {"down"})

    def test_sideways_changes_category_and_needs_more(self):
        self.assertEqual(path(self.g(), line(500, 500, 560, 505, 6)), [])  # 60 px: not yet
        self.assertEqual(path(self.g(), line(500, 500, 610, 505, 11)), ["right"])

    def test_a_pause_forgets_half_a_step(self):
        g = self.g()
        self.assertEqual(path(g, line(500, 500, 500, 530, 3)), [])  # 30 of 40 px
        self.assertEqual(path(g, line(500, 530, 500, 545, 3), t0=1000), [])  # after a pause: 15, not 45

    def test_a_fling_is_bounded(self):
        self.assertEqual(path(self.g(), [(500, 0), (500, 2000)]), ["down"] * 3)

    def test_the_edge_recentres_and_the_jump_is_no_move(self):
        g = self.g()
        self.assertIsNone(g.recentre(500, 500, 1920, 1080, 100))
        self.assertEqual(g.recentre(500, 1050, 1920, 1080, 100), (960, 540))
        path(g, [(500, 1000), (500, 1050)])
        self.assertEqual(g.motion(960, 540, 100), [])  # the jump up is not "up" x10
        self.assertEqual(path(g, line(960, 540, 960, 590, 5), t0=110), ["down"])


class Mouse(unittest.TestCase):
    def setUp(self):
        self.s = shell()
        self.s.glide = Glide(step=40, cross=100)
        self.warped = []
        self.s.warp = lambda x, y: self.warped.append((x, y))

    def move(self, points, t0=0):
        for i, (x, y) in enumerate(points):
            self.s.pointer(x, y, 1920, 1080, t0 + i * 10)

    def test_gliding_moves_the_selection_and_category(self):
        s = self.s
        self.move(line(900, 400, 900, 445, 9))
        self.assertEqual(s.current_row().title, "Chromium")
        self.move(line(900, 445, 1015, 450, 11), t0=1000)
        self.assertEqual(s.state()["category"], USERS)

    def test_clicks_act_on_what_is_current(self):
        s = self.s
        s.mouseButton("right")
        self.assertEqual(s.state()["panel"], "options")
        s.mouseButton("back")
        self.assertEqual(s.state()["panel"], "")
        s.mouseButton("left")
        self.assertEqual(s.api.sent[-1], ("POST", "/v1/instances/steam/switch", None))
        s.mouseButton("forward")
        self.assertEqual(s.state()["category"], USERS)
        s.mouseButton("middle")
        self.assertEqual(s.state()["category"], APPS)

    def test_sideways_in_a_panel_does_not_close_it(self):
        s = self.s
        s.mouseButton("right")
        self.move(line(900, 400, 1100, 400, 20))
        self.assertEqual(s.state()["panel"], "options")
        self.move(line(1100, 400, 1100, 445, 9), t0=1000)
        self.assertEqual(s.state()["option"], 1)

    def test_the_wheel(self):
        s = self.s
        s.wheel(0, -120)
        self.assertEqual(s.current_row().title, "Chromium")
        s.wheel(-120, 0)
        self.assertEqual(s.state()["category"], USERS)

    def test_the_pointer_is_put_back_from_the_edge(self):
        self.move([(900, 1060), (900, 1070)])
        self.assertEqual(self.warped[-1], (960.0, 540.0))


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
        self.assertEqual(s.current_row().title, "Games")  # the first thing it can choose now

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
