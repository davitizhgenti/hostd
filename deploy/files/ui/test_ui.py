"""Tests of hostd_ui's parts that need no screen: run with
python3 -m unittest discover -s deploy/files/ui -p "test_*.py"."""

import datetime
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

from hostd_ui import api, background, cross, input as inp, notices, theme, views  # noqa: E402

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

    def test_running_then_apps(self):
        self.assertEqual(self.titles(views.visible(self.rows, "")), ["Running", "Steam", "Apps", "Steam", "Chromium"])
        self.assertEqual(self.summary, "1 running   2 apps")

    def test_running_row(self):
        r = self.rows[1]
        self.assertEqual((r.instance, r.badge, r.tone), ("steam", "running", "positive"))
        self.assertEqual(r.call, views.Call("POST", "/v1/instances/steam/switch"))
        self.assertEqual(self.titles(r.sheet), ["Bring to front", "Close", "Big Picture", "Dota 2"])
        self.assertEqual(r.sheet[1].tone, "danger")

    def test_nested_apps_only_when_filtering(self):
        dota = [r for r in self.rows if r.app == "steam-570"]
        self.assertTrue(dota and dota[0].under and dota[0].value == "Steam")
        self.assertEqual(self.titles(views.visible(self.rows, "dota")), ["Dota 2"])
        self.assertEqual(self.titles(views.visible(self.rows, "  ")), self.titles(views.visible(self.rows, "")))

    def test_app_row_and_its_sheet(self):
        steam = [r for r in self.rows if r.app == "steam" and not r.instance][0]
        self.assertEqual(steam.value, "1 action  1 app")
        self.assertEqual(steam.call, views.Call("POST", "/v1/apps/steam/open"))
        self.assertEqual(steam.sheet[1].call, views.Call("POST", "/v1/apps/steam/open", {"action": "bigpicture"}))

    def test_nothing_running(self):
        rows, summary = views.apps_page([], APPS)
        self.assertEqual(rows[0].title, "Apps")
        self.assertEqual(summary, "0 running   2 apps")

    def test_key_keeps_identity(self):
        again, _ = views.apps_page(INSTANCES, APPS)
        self.assertEqual([r.key() for r in again], [r.key() for r in self.rows])


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
                self.assertTrue(os.path.exists(os.path.join(theme.HERE, "icons", r.icon + ".svg")), r.icon)

    def test_background_row(self):
        look = {"mode": "month", "color": "", "image": "/pics/sea.jpg"}
        rows = {r.title: r for r in views.settings_page(AUDIO, DISPLAY, look, theme.tokens())}
        bg = rows["Background"]
        self.assertEqual(bg.value, "Month and time of day")
        self.assertEqual(bg.sheet[0].badge, "now")
        self.assertEqual(bg.sheet[0].call, views.local("background", {"mode": "month", "color": "", "image": "/pics/sea.jpg"}))
        self.assertEqual(bg.sheet[-1].title, "Image: sea.jpg")
        gold = [r for r in bg.sheet if r.title == "Gold"][0]
        self.assertEqual(gold.call.body["mode"], "color")
        # A chosen color is named, and marked.
        look = dict(gold.call.body)
        rows = {r.title: r for r in views.settings_page(AUDIO, DISPLAY, look, theme.tokens())}
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


class Categories(unittest.TestCase):
    def test_names_and_icons(self):
        self.assertEqual([n for n, _ in views.CATEGORIES], ["Apps", "Users", "Settings", "Status"])
        for _, icon in views.CATEGORIES:
            self.assertTrue(os.path.exists(os.path.join(theme.HERE, "icons", icon + ".svg")))

    def test_every_page_has_icons(self):
        rows, _ = views.apps_page(INSTANCES, APPS)
        rows += views.users_page([{"user": "serve", "vt": 7}]) + views.status_page({}, DISPLAY, [])
        for r in rows:
            if r.kind != views.HEADER:
                self.assertTrue(r.icon, r.title)


M = cross.Metrics(x=360, y=300, category_step=200, icon_category=64, item_height=64, item_current=112,
                  header_height=48, gap=16, label_height=50)


class Cross(unittest.TestCase):
    def test_categories_slide(self):
        self.assertEqual(cross.category_x(M, 2, 2), 360)
        self.assertEqual(cross.category_x(M, 3, 2), 560)
        self.assertEqual(cross.category_x(M, 0, 2), -40)

    def test_current_under_the_row_others_around(self):
        kinds = ["header", "item", "item", "header", "item", "item"]
        ys = cross.item_ys(M, kinds, 2)
        below = M.row_bottom
        self.assertEqual(below, 300 + 64 + 50 + 16)
        self.assertEqual(ys[2], below)  # the current one
        self.assertEqual(ys[3], below + 112)  # then a header, under the current (taller) one
        self.assertEqual(ys[4], below + 112 + 48)
        self.assertEqual(ys[1], 300 - 16 - 64)  # before it: above the row
        self.assertEqual(ys[0], 300 - 16 - 64 - 48)

    def test_first_and_empty(self):
        self.assertEqual(cross.item_ys(M, ["item", "item"], 0), [M.row_bottom, M.row_bottom + 112])
        self.assertEqual(cross.item_ys(M, ["item"], 5), [M.row_bottom])  # clamped
        self.assertEqual(cross.item_ys(M, [], 0), [])

    def test_metrics_scale(self):
        m = cross.Metrics.of(theme.tokens()["layout"], 2.0, 16, 50)
        self.assertEqual((m.x, m.gap, m.label_height), (theme.tokens()["layout"]["cross-x"] * 2, 32, 100))

    def test_ease(self):
        self.assertEqual((cross.ease(0), cross.ease(1), cross.ease(2)), (0, 1, 1))
        self.assertGreater(cross.ease(0.5), 0.5)  # quick, then settling


class Background(unittest.TestCase):
    def setUp(self):
        self.tokens = theme.tokens()
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
        self.assertEqual(inp.verb_for_key("Return"), inp.ACTIVATE)
        self.assertEqual(inp.verb_for_key("F10", shift=True), inp.ACTIONS)
        self.assertEqual(inp.verb_for_key("Tab", shift=True), inp.PREV_TAB)
        self.assertIsNone(inp.verb_for_key("a"))
        self.assertEqual(inp.verb_for_key("BackSpace"), inp.BACK)
        self.assertIsNone(inp.verb_for_key("BackSpace", typing=True))
        self.assertIsNone(inp.verb_for_key("space", typing=True))

    def test_nav_is_the_same_verbs(self):
        for what, verb in (("choose", inp.ACTIVATE), ("back", inp.BACK), ("actions", inp.ACTIONS), ("left", inp.LEFT)):
            self.assertEqual(inp.verb_for_nav(what), verb)
        self.assertIsNone(inp.verb_for_nav("dance"))
        for v in inp._NAV.values():
            self.assertIn(v, inp.VERBS)

    def test_hints(self):
        pairs = [(inp.ACTIVATE, "open"), (inp.CLOSE, "close"), (inp.BACK, "back")]
        self.assertEqual(inp.hint_text(pairs), "Enter open   Del close   Esc back")
        self.assertEqual(inp.hint_text(pairs, "controller"), "A open   B back")


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


class Theme(unittest.TestCase):
    def test_scaling(self):
        self.assertEqual(theme.scaled("a: 27px; b: 4px; c: 150ms", 2160), "a: 54px; b: 8px; c: 150ms")
        self.assertEqual(theme.scaled("a: 100px", 800), "a: 85px")  # never below MIN_SCALE
        self.assertEqual(theme.scaled("a: 1px", 400), "a: 1px")

    def test_stylesheet_has_tokens_then_theme(self):
        css = theme.stylesheet(1080)
        self.assertLess(css.index("--color-bg"), css.index("window.hostd"))
        # The theme uses only tokens for its colors.
        import re
        with open(os.path.join(theme.HERE, "theme.css")) as f:
            own = f.read()
        self.assertEqual(re.findall(r"#[0-9a-fA-F]{3,8}\b", own), [])

    def test_tokens_json(self):
        t = theme.tokens()
        self.assertTrue(t["color"]["bg"].startswith("#"))
        self.assertEqual(t["units"]["type"], "px")


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
