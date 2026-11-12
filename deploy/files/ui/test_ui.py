"""Tests of hostd_ui's parts that need no screen: run with
python3 -m unittest discover -s deploy/files/ui -p "test_*.py"."""

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

from hostd_ui import api, input as inp, notices, theme, views  # noqa: E402

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
