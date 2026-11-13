"""Tests of hostd-shell's parts that need no screen: run with
python3 -m unittest discover -s deploy/files/ui -p "test_*.py"."""

import datetime
import re
import http.server
import json
import os
import socket
import socketserver
import sys
import tempfile
import threading
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from hostd_shell import api, background, input as inp, menu, notices, views  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))


def tokens():
    with open(os.path.join(HERE, "tokens.json")) as f:
        return json.load(f)

INSTANCES = [
    {"id": "steam", "app": "steam", "name": "Steam", "surface": "window", "state": "running"},
    {"id": "hostd-menu", "app": "hostd-menu", "surface": "window", "state": "running"},
    {"id": "job", "app": "job", "surface": "none", "state": "running"},
    {"id": "old", "app": "old", "surface": "window", "state": "exited"},
]
APPS = [
    {"id": "steam", "name": "Steam", "surface": "window", "actions": [{"id": "bigpicture", "name": "Big Picture"}]},
    {"id": "steam-570", "name": "Dota 2", "surface": "window", "under": "steam"},
    {"id": "chromium", "name": "Chromium", "surface": "window"},
    {"id": "hostd-menu", "name": "Menu", "surface": "window"},
    {"id": "job", "surface": "none"},
]


class AppsPage(unittest.TestCase):
    def setUp(self):
        self.rows, self.summary = views.apps_page(INSTANCES, APPS)

    def titles(self, rows):
        return [r.title for r in rows]

    def test_one_row_per_app_running_first(self):
        # No list of running apps apart: a running app is marked in place.
        self.assertEqual(self.titles(views.visible(self.rows, "")), ["Steam", "Chromium"])
        self.assertEqual([r.kind for r in self.rows], [views.ITEM] * len(self.rows))
        self.assertEqual(self.summary, "1 running   2 apps")

    def test_running_row(self):
        r = self.rows[0]
        self.assertEqual((r.instance, r.mark, r.badge, r.subtitle), ("steam", "positive", "", ""))
        self.assertEqual(r.call, views.Call("POST", "/v1/instances/steam/switch"))
        self.assertEqual(self.titles(r.sheet), ["Bring to front", "Close", "Big Picture", "Dota 2"])
        self.assertEqual(r.sheet[1].tone, "danger")

    def test_several_instances_each_in_the_options(self):
        two = INSTANCES + [{"id": "steam#2", "app": "steam", "surface": "window", "state": "starting"}]
        rows, _ = views.apps_page(two, APPS)
        self.assertEqual(self.titles(rows[0].sheet)[:4],
                         ["Bring to front  steam", "Close  steam", "Bring to front  steam#2", "Close  steam#2"])

    def test_nested_apps_only_when_filtering(self):
        dota = [r for r in self.rows if r.app == "steam-570"]
        self.assertTrue(dota and dota[0].under and dota[0].value == "Steam")
        self.assertEqual(self.titles(views.visible(self.rows, "dota")), ["Dota 2"])
        self.assertEqual(self.titles(views.visible(self.rows, "  ")), self.titles(views.visible(self.rows, "")))

    def test_a_running_nested_app_is_next_to_its_launcher(self):
        dota = {"id": "steam-570", "app": "steam-570", "name": "Dota 2", "surface": "window", "state": "running"}
        rows, summary = views.apps_page(INSTANCES + [dota], APPS)
        self.assertEqual(self.titles(views.visible(rows, "")), ["Steam", "Dota 2", "Chromium"])
        self.assertEqual(summary, "2 running   2 apps")

    def test_app_row_and_its_sheet(self):
        rows, summary = views.apps_page([], APPS)
        steam = rows[0]
        self.assertEqual((steam.title, steam.value, steam.mark, steam.instance), ("Steam", "", "", ""))
        self.assertEqual(steam.call, views.Call("POST", "/v1/apps/steam/open"))
        self.assertEqual(self.titles(steam.sheet), ["Start", "Big Picture", "Dota 2"])
        self.assertEqual(steam.sheet[1].call, views.Call("POST", "/v1/apps/steam/open", {"action": "bigpicture"}))
        self.assertEqual(summary, "2 apps")

    def test_instances_of_no_app_are_listed(self):
        run = {"id": "run-1", "app": "run-1", "name": "make", "surface": "window", "state": "running"}
        rows, _ = views.apps_page(INSTANCES + [run], APPS)
        self.assertEqual((rows[0].title, rows[0].instance, rows[0].mark), ("make", "run-1", "positive"))

    def test_key_keeps_identity(self):
        again, _ = views.apps_page(INSTANCES, APPS)
        self.assertEqual([r.key() for r in again], [r.key() for r in self.rows])


FOLDER_APPS = APPS + [
    {"id": "dolphin", "name": "Dolphin", "surface": "window", "folder": "Games/Dolphin"},
    {"id": "mkw", "name": "Mario Kart Wii", "surface": "window", "folder": "Games/Dolphin/Wii"},
    {"id": "smg", "name": "Super Mario Galaxy", "surface": "window", "folder": "Games/Dolphin/Wii"},
    {"id": "steam-folder", "name": "Steam", "surface": "window", "folder": "Games/Steam"},
]


class Folders(unittest.TestCase):
    def titles(self, path=(), apps=FOLDER_APPS, root=(), tabs=()):
        rows, _ = views.apps_page(INSTANCES, apps, path, root, tabs)
        return [(r.kind, r.title) for r in views.visible(rows, "")]

    def test_running_then_folders_then_apps(self):
        self.assertEqual(self.titles(), [("item", "Steam"), ("folder", "Games"), ("item", "Chromium")])

    def test_opening_a_folder(self):
        self.assertEqual(self.titles(("Games",)), [("folder", "Dolphin"), ("folder", "Steam")])
        self.assertEqual(self.titles(("Games", "Dolphin")), [("folder", "Wii"), ("item", "Dolphin")])
        self.assertEqual(self.titles(("Games", "Dolphin", "Wii")),
                         [("item", "Mario Kart Wii"), ("item", "Super Mario Galaxy")])

    def test_folder_row(self):
        rows, _ = views.apps_page(INSTANCES, FOLDER_APPS)
        games = next(r for r in rows if r.kind == views.FOLDER)
        self.assertEqual((games.value, games.icon, games.call), ("4 apps", "folder", views.local("folder", ["Games"])))

    def test_games_under_steam_are_in_its_folder_too(self):
        dota = {"id": "steam-570", "name": "Dota 2", "surface": "window", "under": "steam", "folder": "Games/Steam"}
        apps = FOLDER_APPS + [dota]
        rows, _ = views.apps_page(INSTANCES, apps, ("Games", "Steam"))
        self.assertEqual([r.title for r in views.visible(rows, "")], ["Steam", "Dota 2"])
        steam = next(r for r in rows if r.app == "steam")
        self.assertIn("Dota 2", [x.title for x in steam.sheet])  # and still in Steam's options

    def test_filtering_searches_every_folder(self):
        rows, _ = views.apps_page(INSTANCES, FOLDER_APPS)
        self.assertEqual([r.title for r in views.visible(rows, "mario")], ["Mario Kart Wii", "Super Mario Galaxy"])
        self.assertEqual(next(r for r in rows if r.app == "mkw").value, "Games / Dolphin / Wii")
        self.assertTrue(all(r.kind != views.FOLDER for r in views.visible(rows, "a")))


class Tabs(unittest.TestCase):
    def test_categories(self):
        cats = views.categories([("Games", "gamepad-2"), ("Services", "server")])
        self.assertEqual([c.name for c in cats], ["Apps", "Games", "Services", "Users", "Settings", "Status"])
        self.assertEqual([c.key for c in cats][:3], ["apps", "tab:Games", "tab:Services"])
        self.assertEqual((cats[1].kind, cats[1].root, cats[1].icon), ("apps", ("Games",), "gamepad-2"))
        for c in views.categories():
            self.assertTrue(os.path.exists(os.path.join(HERE, "icons", c.icon + ".svg")), c.icon)

    def test_a_tab_lists_its_folder(self):
        rows, summary = views.apps_page(INSTANCES, FOLDER_APPS, ("Games",), ("Games",), ["Games"])
        self.assertEqual([(r.kind, r.title) for r in views.visible(rows, "")], [("folder", "Dolphin"), ("folder", "Steam")])
        self.assertEqual(summary, "4 apps")
        # A path outside the tab is the tab's own folder.
        again, _ = views.apps_page(INSTANCES, FOLDER_APPS, ("Elsewhere",), ("Games",), ["Games"])
        self.assertEqual([r.key() for r in again], [r.key() for r in rows])

    def test_apps_leaves_out_the_tabs(self):
        rows, summary = views.apps_page(INSTANCES, FOLDER_APPS, (), (), ["Games"])
        self.assertEqual([r.title for r in views.visible(rows, "")], ["Steam", "Chromium"])
        self.assertEqual(summary, "1 running   2 apps")
        self.assertEqual([r.title for r in views.visible(rows, "mario")], ["Mario Kart Wii", "Super Mario Galaxy"])

    def test_every_tab_searches_every_app(self):
        rows, _ = views.apps_page(INSTANCES, FOLDER_APPS, ("Games",), ("Games",), ["Games"])
        self.assertEqual([r.title for r in views.visible(rows, "chrom")], ["Chromium"])


SERVICE_APPS = APPS + [
    {"id": "jelly", "name": "Jellyfin", "surface": "background"},
    {"id": "ha", "name": "Home Assistant", "surface": "background"},
    {"id": "px", "name": "Proxy", "surface": "background"},
    {"id": "sync", "name": "Syncthing", "surface": "background", "folder": "Tools"},
]
SERVICE_INSTANCES = INSTANCES + [
    {"id": "jelly", "app": "jelly", "surface": "background", "state": "running"},
    {"id": "px", "app": "px", "surface": "background", "state": "failed"},
    {"id": "ha", "app": "ha", "surface": "background", "state": "exited"},
]


class Services(unittest.TestCase):
    def rows(self, path=()):
        rows, _ = views.apps_page(SERVICE_INSTANCES, SERVICE_APPS, path)
        return views.visible(rows, "")

    def test_background_apps_are_in_services(self):
        self.assertEqual([(r.kind, r.title) for r in self.rows()],
                         [("item", "Steam"), ("folder", "Services"), ("folder", "Tools"), ("item", "Chromium")])
        self.assertEqual([r.title for r in self.rows(("Services",))], ["Jellyfin", "Home Assistant", "Proxy"])
        self.assertEqual([r.title for r in self.rows(("Tools",))], ["Syncthing"])

    def test_the_summary_counts_services_apart(self):
        self.assertEqual(views.apps_page(SERVICE_INSTANCES, SERVICE_APPS)[1], "1 running   2 apps   4 services")
        self.assertEqual(views.apps_page(SERVICE_INSTANCES, SERVICE_APPS, ("Services",), ("Services",), ["Services"])[1],
                         "3 services")

    def test_a_service_shows_its_state_and_acts_through_its_options(self):
        jelly, ha, px = self.rows(("Services",))
        self.assertEqual([(r.badge, r.tone) for r in (jelly, ha, px)],
                         [("running", "positive"), ("stopped", ""), ("failed", "danger")])
        for r in (jelly, ha, px):  # nothing starts or stops on one press
            self.assertEqual((r.call, r.instance, r.icon), (None, "", "server"))
        self.assertEqual([(o.title, o.call) for o in jelly.sheet],
                         [("Stop", views.Call("POST", "/v1/instances/jelly/close")),
                          ("Restart", views.Call("POST", "/v1/instances/jelly/restart"))])
        self.assertEqual([(o.title, o.call) for o in px.sheet], [("Start", views.Call("POST", "/v1/apps/px/open"))])
        self.assertEqual([o.title for o in ha.sheet], ["Start"])


class UsersPage(unittest.TestCase):
    def test_holder_and_others(self):
        rows = views.users_page([{"user": "serve", "vt": 7, "holds": True}, {"user": "guest", "vt": 8, "holds": False}])
        serve, guest = rows[1], rows[2]
        self.assertEqual((serve.badge, serve.call, serve.confirm), ("on screen", None, ""))
        self.assertEqual(guest.call, views.action("seat.switch", user="guest"))
        self.assertIn("guest", guest.confirm)
        self.assertEqual(guest.subtitle, "screen 8")

    def test_no_seats(self):
        rows = views.users_page([])
        self.assertIn("one user", rows[1].title)


AUDIO = {"available": True, "percent": 70, "muted": False, "outputs": [
    {"name": "hdmi", "description": "HDMI", "default": True},
    {"name": "usb", "description": "USB speakers", "default": False}]}
DISPLAY = {"outputs": [{"name": "HDMI-A-1", "make": "Epson", "model": "PJ", "active": True,
                        "width": 1280, "height": 800, "refresh": 59.81,
                        "modes": [{"width": 1280, "height": 800, "refresh": 59.81},
                                  {"width": 1920, "height": 1080, "refresh": 60}]},
                       {"name": "off", "active": False}],
           "gpu_memory": {"total": 4 << 30, "used": 1 << 30}}


class SettingsPage(unittest.TestCase):
    def setUp(self):
        self.rows = {r.title: r for r in views.settings_page(AUDIO, DISPLAY)}

    def test_volume_slider(self):
        v = self.rows["Volume"]
        self.assertEqual((v.kind, v.level, v.value), (views.SLIDER, 70, "70%"))
        self.assertEqual(v.step, views.action("audio.volume.set", percent="+5"))
        self.assertEqual(v.step_back, views.action("audio.volume.set", percent="-5"))

    def test_mute_and_output(self):
        self.assertEqual(self.rows["Mute"].call, views.action("audio.mute.set", muted="toggle"))
        out = self.rows["Output"]
        self.assertEqual(out.value, "HDMI")
        self.assertEqual(out.sheet[1].call, views.action("audio.output.set", output="usb"))
        self.assertEqual(out.sheet[0].badge, "in use")

    def test_resolution(self):
        r = self.rows["Resolution"]
        self.assertEqual(r.value, "1280×800 @ 59.81 Hz")
        self.assertEqual(r.sheet[0].call, views.action("display.mode", output="HDMI-A-1", mode="1920x1080@60"))
        self.assertEqual(r.sheet[1].badge, "now")

    def test_power_asks(self):
        self.assertTrue(self.rows["Turn the screen off"].confirm)

    def test_icons(self):
        self.assertEqual(self.rows["Volume"].icon, "volume-2")
        self.assertEqual(self.rows["Resolution"].icon, "monitor")
        for r in self.rows.values():
            if r.kind != views.HEADER:
                self.assertTrue(os.path.exists(os.path.join(HERE, "icons", r.icon + ".svg")), r.icon)

    def test_background_row(self):
        look = {"mode": "month", "color": "", "image": "/pics/sea.jpg"}
        rows = {r.title: r for r in views.settings_page(AUDIO, DISPLAY, look, tokens())}
        bg = rows["Background"]
        self.assertEqual(bg.value, "Month and time of day")
        self.assertEqual(bg.sheet[0].badge, "now")
        self.assertEqual(bg.sheet[0].call, views.local("background", {"mode": "month", "color": "", "image": "/pics/sea.jpg"}))
        self.assertEqual(bg.sheet[-1].title, "Image: sea.jpg")
        gold = [r for r in bg.sheet if r.title == "Gold"][0]
        self.assertEqual(gold.call.body["mode"], "color")
        # A chosen color is named, and marked.
        look = dict(gold.call.body)
        rows = {r.title: r for r in views.settings_page(AUDIO, DISPLAY, look, tokens())}
        self.assertEqual(rows["Background"].value, "Gold")

    def test_no_sound_no_screen(self):
        titles = [r.title for r in views.settings_page(None, {})]
        self.assertIn("No sound server", titles)
        self.assertIn("No screen attached", titles)
        self.assertNotIn("Turn the screen off", titles)

    def test_one_output_no_choice(self):
        rows = views.settings_page({"percent": 10, "outputs": [{"name": "hdmi"}]}, {})
        self.assertNotIn("Output", [r.title for r in rows])


class StatusPage(unittest.TestCase):
    def test_rows(self):
        log = [{"type": "seat.switch", "summary": "serve", "status": "applied", "time": "2026-11-12T13:25:28-05:00",
                "source": {"name": "laptop"}},
               {"type": "app.open", "status": "failed", "time": "2026-11-12T13:20:00Z", "source": {"kind": "rule"}}]
        rows = views.status_page({"version": "v0.2.0", "previous": "edge-1"}, DISPLAY, log)
        by = {r.title: r for r in rows}
        self.assertEqual(by["Version"].value, "v0.2.0")
        self.assertEqual(by["Video memory"].value, "1.0 GB of 4.0 GB")
        self.assertEqual(by["seat.switch serve"].subtitle, "13:25:28  laptop")
        self.assertEqual((by["app.open"].badge, by["app.open"].tone), ("failed", "danger"))

    def test_empty(self):
        self.assertIn("Nothing done yet", [r.title for r in views.status_page({}, {}, [])])


class MenuFile(unittest.TestCase):
    def load(self, text, has_icon=None):
        d = tempfile.mkdtemp()
        f = os.path.join(d, "menu.toml")
        with open(f, "w") as out:
            out.write(text)
        return menu.load(f, has_icon)

    def test_no_file_no_tabs(self):
        self.assertEqual(menu.load("/nonexistent/menu.toml"), ([], ""))
        self.assertEqual(menu.mtime("/nonexistent/menu.toml"), 0)

    def test_tabs_in_order(self):
        tabs, problem = self.load('[[tab]]\nfolder = "Games"\nicon = "gamepad-2"\n[[tab]]\nfolder = "/Services/"\n',
                                  has_icon=lambda n: n == "gamepad-2")
        self.assertEqual((tabs, problem), ([("Games", "gamepad-2"), ("Services", "folder")], ""))

    def test_an_unknown_icon_is_the_folder(self):
        self.assertEqual(self.load('[[tab]]\nfolder = "Games"\nicon = "nope"\n', lambda n: False)[0], [("Games", "folder")])

    def test_problems_give_no_tabs(self):
        for text, said in (('[[tab]\n', "menu.toml"),
                           ('tab = "Games"\n', "list of tables"),
                           ('[[tab]]\nfolder = "Games/Steam"\n', "one folder"),
                           ('[[tab]]\nicon = "x"\n', "one folder"),
                           ('[[tab]]\nfolder = "Games"\n[[tab]]\nfolder = "Games"\n', "a tab already")):
            tabs, problem = self.load(text)
            self.assertEqual(tabs, [], text)
            self.assertIn(said, problem, text)


class Categories(unittest.TestCase):
    def test_every_page_has_icons(self):
        rows, _ = views.apps_page(INSTANCES, APPS)
        rows += views.users_page([{"user": "serve", "vt": 7}]) + views.status_page({}, DISPLAY, [])
        for r in rows:
            if r.kind != views.HEADER:
                self.assertTrue(r.icon, r.title)


class Background(unittest.TestCase):
    def setUp(self):
        self.tokens = tokens()
        self.dir = tempfile.mkdtemp()
        self.file = os.path.join(self.dir, "hostd", "ui.toml")

    def light(self, hex_color):
        import colorsys
        return colorsys.rgb_to_hls(*background.rgb(hex_color))[1]

    def test_default_and_bad_files(self):
        self.assertEqual(background.load(self.file)["mode"], "month")
        os.makedirs(os.path.dirname(self.file))
        with open(self.file, "w") as f:
            f.write("[background]\nmode = \"color\"\ncolor = \"red\"\n")
        self.assertEqual(background.load(self.file)["mode"], "month")  # no usable color
        with open(self.file, "w") as f:
            f.write("not toml [")
        self.assertEqual(background.load(self.file)["mode"], "month")

    def test_save_and_load(self):
        setting = {"mode": "image", "color": "#203040", "image": '/pics/a "b".jpg'}
        background.save(setting, self.file)
        self.assertEqual(background.load(self.file), setting)

    def test_month_gives_the_hue(self):
        noon = datetime.datetime(2026, 12, 1, 13, 0)
        top, bottom = background.colors({"mode": "month"}, noon, self.tokens)
        r, g, b = background.rgb(top)
        self.assertGreater(r, g)  # December: red
        self.assertLess(self.light(bottom), self.light(top))
        aug = background.colors({"mode": "month"}, noon.replace(month=8), self.tokens)[0]
        r, g, b = background.rgb(aug)
        self.assertGreater(b, r)  # August: blue

    def test_night_is_darker_and_light_is_capped(self):
        day = datetime.datetime(2026, 3, 1, 14, 0)
        night = day.replace(hour=2)
        dusk = day.replace(hour=19)
        light = [self.light(background.colors({"mode": "month"}, t, self.tokens)[0]) for t in (night, dusk, day)]
        self.assertLess(light[0], light[1])
        self.assertLess(light[1], light[2])
        for m in range(1, 13):
            self.assertLessEqual(self.light(background.colors({"mode": "month"}, day.replace(month=m), self.tokens)[0]),
                                 background.MAX_LIGHT + 0.01)
        white = background.colors({"mode": "color", "color": "#ffffff"}, day, self.tokens)[0]
        self.assertLessEqual(self.light(white), background.MAX_LIGHT + 0.01)

    def test_daylight(self):
        self.assertEqual([background.daylight(h) for h in (0, 3, 13, 23)], [0, 0, 1, 0])
        self.assertTrue(0 < background.daylight(8) < 1 and 0 < background.daylight(19) < 1)

    def test_css_and_sway(self):
        now = datetime.datetime(2026, 5, 1, 12, 0)
        self.assertIn("linear-gradient(to bottom", background.css({"mode": "month"}, now, self.tokens))
        args = background.sway_bg({"mode": "month"}, now, self.tokens)
        self.assertEqual((args[:3], args[4]), (["output", "*", "bg"], "solid_color"))
        self.assertTrue(background.is_color(args[3]))
        image = {"mode": "image", "color": "", "image": "/pics/sea.jpg"}
        self.assertIn('url("file:///pics/sea.jpg")', background.css(image, now, self.tokens))
        self.assertEqual(background.sway_bg(image, now, self.tokens), ["output", "*", "bg", "/pics/sea.jpg", "fill"])


class Results(unittest.TestCase):
    def test_text(self):
        self.assertEqual(views.result_text({"status": "applied"}), "")
        self.assertEqual(views.result_text({"status": "skipped", "reason": "already"}), "Skipped: already")
        self.assertEqual(views.result_text(None), "")


class Input(unittest.TestCase):
    def test_keys(self):
        self.assertEqual(inp.verb_for_key("Key_Return"), inp.ACTIVATE)
        self.assertEqual(inp.verb_for_key("Key_F10", shift=True), inp.ACTIONS)
        self.assertEqual(inp.verb_for_key("Key_Tab", shift=True), inp.PREV_TAB)
        self.assertIsNone(inp.verb_for_key("Key_A"))
        self.assertEqual(inp.verb_for_key("Key_Backspace"), inp.BACK)
        self.assertIsNone(inp.verb_for_key("Key_Backspace", typing=True))
        self.assertIsNone(inp.verb_for_key("Key_Space", typing=True))

    def test_nav_is_the_same_verbs(self):
        for what, verb in (("choose", inp.ACTIVATE), ("back", inp.BACK), ("actions", inp.ACTIONS), ("left", inp.LEFT)):
            self.assertEqual(inp.verb_for_nav(what), verb)
        self.assertIsNone(inp.verb_for_nav("dance"))
        for v in inp._NAV.values():
            self.assertIn(v, inp.VERBS)

    def test_every_hostd_verb_is_known(self):
        """The verbs internal/evdev/nav.go can send, kept in step with the menu."""
        for what, verb in (("close", inp.CLOSE), ("prev_tab", inp.PREV_TAB), ("next_tab", inp.NEXT_TAB),
                           ("page_up", inp.PAGE_UP), ("page_down", inp.PAGE_DOWN), ("home", inp.HOME)):
            self.assertEqual(inp.verb_for_nav(what), verb)

    def test_the_go_side_and_the_shell_agree_on_the_verbs(self):
        with open(os.path.join(HERE, "..", "..", "..", "internal", "evdev", "nav.go")) as f:
            src = f.read()
        sent = set(re.findall(r'Verb\w+\s*(?:,\s*Verb\w+)*\s*=\s*((?:"[a-z_]+"(?:,\s*)?)+)', src))
        verbs = set(re.findall(r'"([a-z_]+)"', " ".join(sent)))
        self.assertTrue({"up", "choose", "home", "page_down"} <= verbs, verbs)
        for verb in verbs:
            self.assertIsNotNone(inp.verb_for_nav(verb), f"hostd sends {verb!r}; the shell does not know it")

    def test_hints_per_family(self):
        pairs = [(inp.ACTIVATE, "open"), (inp.CLOSE, "close"), (inp.BACK, "back")]
        self.assertEqual(inp.hint_text(pairs, "controller", "playstation"), "✕ open   △ close   ○ back")
        self.assertEqual(inp.hint_text(pairs, "controller", "nintendo"), "A open   X close   B back")
        self.assertEqual(inp.hint_text(pairs, "controller", "wii"), "A open   2 close   B back")
        self.assertEqual(inp.hint_text(pairs, "controller", "???"), "A open   Y close   B back")

    def test_hints(self):
        pairs = [(inp.ACTIVATE, "open"), (inp.CLOSE, "close"), (inp.BACK, "back")]
        self.assertEqual(inp.hint_text(pairs), "Enter open   Del close   Esc back")
        self.assertEqual(inp.hint_text(pairs, "controller"), "A open   Y close   B back")  # a pad has a close button now


class Clock:
    def __init__(self):
        self.t = 0.0

    def __call__(self):
        return self.t


class NoticeQueue(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.n = notices.Notices(self.clock)

    def test_at_most_three_then_the_rest(self):
        ids = [self.n.notify("a", f"n{i}") for i in range(5)]
        self.assertEqual([x.id for x in self.n.shown()], ids[:3])
        self.n.close(ids[0])
        self.assertEqual([x.id for x in self.n.shown()], ids[1:4])

    def test_critical_goes_first(self):
        for i in range(3):
            self.n.notify("a", f"n{i}")
        crit = self.n.notify("a", "fire", urgency=notices.CRITICAL)
        self.assertNotIn(crit, [x.id for x in self.n.shown()])  # no room yet
        self.n.close(1)
        self.assertEqual(self.n.shown()[0].id, crit)

    def test_replaces(self):
        a = self.n.notify("a", "one")
        self.clock.t = 5
        b = self.n.notify("a", "two", replaces_id=a)
        self.assertEqual(a, b)
        self.assertEqual((len(self.n.all), self.n.find(a).summary), (1, "two"))
        self.clock.t = 12  # 7 s after the replacement: still there
        self.assertEqual(self.n.expire(), [])
        self.clock.t = 13.5
        self.assertEqual(self.n.expire(), [a])
        # Replacing one that is gone makes a new one.
        self.assertNotEqual(self.n.notify("a", "three", replaces_id=a), a)

    def test_timeouts(self):
        quick = self.n.notify("a", "quick", expire_ms=1000)
        crit = self.n.notify("a", "crit", urgency=notices.CRITICAL)
        normal = self.n.notify("a", "normal")
        self.assertEqual(self.n.next_expiry(), 1.0)
        self.clock.t = notices.DEFAULT_TIMEOUT
        self.assertEqual(sorted(self.n.expire()), sorted([quick, normal]))
        self.assertEqual([x.id for x in self.n.shown()], [crit])
        self.clock.t = notices.CRITICAL_TIMEOUT
        self.assertEqual(self.n.expire(), [crit])
        self.assertIsNone(self.n.next_expiry())

    def test_close_unknown(self):
        self.assertFalse(self.n.close(42))

    def test_urgency_hint(self):
        self.assertEqual(notices.urgency_of({"urgency": 2}), 2)
        self.assertEqual(notices.urgency_of({"urgency": "x"}), notices.NORMAL)
        self.assertEqual(notices.urgency_of(None), notices.NORMAL)

    def test_osd(self):
        self.assertEqual(notices.osd_for({"type": "audio.volume.changed", "data": {"percent": 40}}), ("Volume", "40%", 40))
        self.assertEqual(notices.osd_for({"type": "audio.volume.changed", "data": {"muted": True}}), ("Volume", "Muted", 0))
        self.assertEqual(notices.osd_for({"type": "seat.switched", "data": {"user": "guest"}}), ("Screen", "guest", None))
        self.assertIsNone(notices.osd_for({"type": "instance.started"}))


class Hostile(unittest.TestCase):
    """Notifications come from any program; whatever one sends is bounded
    and shown as plain text."""

    def setUp(self):
        self.clock = Clock()
        self.n = notices.Notices(self.clock)

    def test_text_is_bounded_and_cleaned(self):
        nid = self.n.notify("a" * 500, "s" * 5000, "b" * 50000)
        n = self.n.find(nid)
        self.assertEqual((len(n.app), len(n.summary), len(n.body)), (notices.MAX_APP, notices.MAX_SUMMARY, notices.MAX_BODY))
        self.assertTrue(n.summary.endswith("…"))
        n = self.n.find(self.n.notify("x", "a\x00b\x1b[31mc\u202ed\nline", "l1\nl2\x07"))
        self.assertEqual(n.summary, "a b [31mc d line".replace("\u202e", " "))  # controls and bidi marks become spaces
        self.assertEqual(n.body, "l1\nl2 ")  # a body keeps its newlines

    def test_wrong_types_are_survived(self):
        odd = [(None, None, None, "x", "y"), (1, 2, 3, 4.5, None), ([], {}, b"", -1, 10 ** 30)]
        for app, summary, body, replaces, expire in odd:
            nid = self.n.notify(app, summary, body, replaces, NORMAL_URGENCY, expire)
            self.assertGreater(nid, 0)
            self.assertIsInstance(self.n.find(nid).summary, str)

    def test_the_queue_is_capped(self):
        for i in range(notices.MAX_QUEUE * 3):
            self.clock.t += 1  # slowly enough that no program is rate limited
            self.n.notify(f"app{i}", f"n{i}")
        self.assertLessEqual(len(self.n.all), notices.MAX_QUEUE)
        self.assertEqual(len(self.n.shown()), notices.MAX_SHOWN)

    def test_a_critical_notice_outlasts_a_flood(self):
        crit = self.n.notify("a", "fire", urgency=notices.CRITICAL)
        for i in range(notices.MAX_QUEUE * 2):
            self.clock.t += 1
            self.n.notify(f"app{i}", f"n{i}")
        self.assertIsNotNone(self.n.find(crit))

    def test_one_program_cannot_flood(self):
        ids = {self.n.notify("spam", f"msg {i}") for i in range(500)}  # no time passes
        mine = [x for x in self.n.all if x.app == "spam"]
        self.assertLessEqual(len(mine), notices.RATE_COUNT)
        self.assertLessEqual(len(ids), notices.RATE_COUNT)
        self.clock.t += notices.RATE_SECONDS + 1  # a quiet moment: it may speak again
        before = len(self.n.all)
        self.n.notify("spam", "later")
        self.assertEqual(len(self.n.all), before + 1)
        other = self.n.notify("other", "hello")
        self.assertIsNotNone(self.n.find(other))


NORMAL_URGENCY = 1


class Garbage(unittest.TestCase):
    """What hostd's socket, the settings file or the program's own
    environment send must not take the shell down."""

    def test_websocket_frames_survive_random_bytes(self):
        import random
        rng = random.Random(7)
        for _ in range(300):
            data = bytes(rng.randrange(256) for _ in range(rng.randrange(1, 200)))
            a, b = socket.socketpair()
            a.sendall(data)
            a.close()
            f = api.Frames(b)
            try:
                for _i in range(50):
                    f.next()
            except (ValueError, OSError, EOFError):
                pass  # ended: the stream reconnects
            b.close()

    def test_an_oversized_frame_is_refused_not_allocated(self):
        a, b = socket.socketpair()
        a.sendall(bytes([0x81, 127]) + (1 << 40).to_bytes(8, "big"))
        a.close()
        with self.assertRaises((ValueError, OSError, EOFError)):
            api.Frames(b).next()
        b.close()

    def test_the_settings_file_may_be_anything(self):
        d = tempfile.mkdtemp()
        file = os.path.join(d, "ui.toml")
        for body in (b"", b"\xff\xfe\x00garbage", b"[background]\nmode = 5\n", b"[background]\nmode = \"image\"\nimage = 7\n",
                     b"x" * (1 << 20), b"[background\n", b"[background]\ncolor = \"#12\"\nmode = \"color\"\n"):
            with open(file, "wb") as f:
                f.write(body)
            look = background.load(file)
            self.assertIn(look["mode"], background.MODES, body[:20])
            if look["mode"] == "color":
                self.assertTrue(background.is_color(look["color"]))
        os.mkdir(os.path.join(d, "dir.toml"))  # a directory where a file should be
        self.assertEqual(background.load(os.path.join(d, "dir.toml"))["mode"], "month")

    def test_saving_the_setting_into_a_read_only_place_fails_cleanly(self):
        with self.assertRaises(OSError):
            background.save({"mode": "month", "color": "", "image": ""}, "/proc/hostd-nothing/ui.toml")


class Entry(unittest.TestCase):
    def test_a_missing_package_ends_with_78_and_a_clear_line(self):
        """No PySide6/gi: the unit must not restart it every two seconds."""
        import subprocess
        code = ("import runpy, sys\n"
                "sys.modules['PySide6'] = None; sys.modules['gi'] = None\n"
                "sys.argv = ['hostd-shell']\n"
                f"runpy.run_path({os.path.join(HERE, 'hostd-shell')!r}, run_name='__main__')\n")
        out = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True)
        self.assertEqual(out.returncode, 78, out.stderr)
        self.assertIn("a package is missing", out.stderr)
        self.assertIn("hostd-admin", out.stderr)

    def test_the_unit_does_not_retry_a_missing_package(self):
        unit = os.path.join(os.path.dirname(os.path.dirname(HERE)), "files", "systemd", "hostd-shell.service")
        with open(unit) as f:
            self.assertIn("RestartPreventExitStatus=78", f.read())


class Theme(unittest.TestCase):
    def test_tokens_json(self):
        t = tokens()
        self.assertTrue(t["color"]["bg"].startswith("#"))
        self.assertEqual(t["units"]["type"], "px")

    def test_qml_has_no_colors_of_its_own(self):
        """Every color comes from Theme (tokens.toml)."""
        qml = os.path.join(HERE, "qml", "Hostd")
        for name in sorted(os.listdir(qml)):
            if name.endswith(".qml") and name != "Theme.qml":
                with open(os.path.join(qml, name)) as f:
                    src = f.read()
                self.assertEqual(re.findall(r'"#[0-9a-fA-F]{3,8}"|Qt\.rgba?\(', src), [], name)


class FakeHostd(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def address_string(self):
        return "local"

    def reply(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer tok":
            return self.reply(401, {"error": {"code": "unauthorized", "message": "no token"}})
        if self.path == "/v1/seat":
            return self.reply(200, [{"user": "serve", "vt": 7, "holds": True}])
        self.reply(404, {"error": {"code": "not_found", "message": "no such read"}})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.reply(200, {"status": "applied", "got": body})


class API(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.sock = os.path.join(self.dir, "hostd.sock")
        self.srv = FakeHostd(self.sock, Handler)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()

    def test_get_and_do(self):
        a = api.API(self.sock, "tok")
        self.assertEqual(a.get("/v1/seat")[0]["user"], "serve")
        res = a.do("seat.switch", {"user": "guest"})
        self.assertEqual(res["got"], {"type": "seat.switch", "args": {"user": "guest"}})

    def test_errors(self):
        a = api.API(self.sock, "tok")
        with self.assertRaises(api.APIError) as e:
            a.get("/v1/nothing")
        self.assertEqual((e.exception.code, e.exception.status), ("not_found", 404))
        with self.assertRaises(api.APIError) as e:
            api.API(self.sock, "wrong").get("/v1/seat")
        self.assertEqual(e.exception.code, "unauthorized")

    def test_token_file(self):
        cfg = os.path.join(self.dir, "cfg")
        os.makedirs(os.path.join(cfg, "hostd"))
        with open(os.path.join(cfg, "hostd", "local.token"), "w") as f:
            f.write("tok\n")
        old = dict(os.environ)
        try:
            os.environ.pop("HOSTD_TOKEN", None)
            os.environ["XDG_CONFIG_HOME"] = cfg
            self.assertEqual(api.API(self.sock).token, "tok")
        finally:
            os.environ.clear()
            os.environ.update(old)


class Frames(unittest.TestCase):
    def frames(self, data):
        a, b = socket.socketpair()
        a.sendall(data)
        a.close()
        return api.Frames(b)

    def test_text_fragmented_and_control(self):
        f = self.frames(bytes([0x01, 3]) + b"abc" + bytes([0x89, 1]) + b"p" + bytes([0x80, 2]) + b"de")
        self.assertEqual(f.next(), (0x9, b"p"))  # a ping inside a message comes whole
        self.assertEqual(f.next(), (0x1, b"abcde"))

    def test_lengths_and_mask(self):
        long = b"x" * 300
        f = self.frames(bytes([0x81, 126]) + (300).to_bytes(2, "big") + long
                        + bytes([0x81, 0x80 | 2]) + b"\x01\x02\x03\x04" + bytes([ord("h") ^ 1, ord("i") ^ 2]))
        self.assertEqual(f.next(), (0x1, long))
        self.assertEqual(f.next(), (0x1, b"hi"))
        with self.assertRaises(ValueError):
            f.next()

    def test_send_frame_is_masked(self):
        a, b = socket.socketpair()
        api.send_frame(a, 0xA, b"pong")
        data = b.recv(64)
        self.assertEqual(data[0], 0x8A)
        self.assertEqual(data[1], 0x80 | 4)
        mask, body = data[2:6], data[6:]
        self.assertEqual(bytes(c ^ mask[i % 4] for i, c in enumerate(body)), b"pong")


if __name__ == "__main__":
    unittest.main()
