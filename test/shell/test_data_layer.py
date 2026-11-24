"""The loader on the real async client, against a fake hostd over a socket:
what the screen does when hostd is slow, hung, or talking."""

import itertools
import os
import tempfile
import time
import unittest

from fakehostd import FakeHostd
from PySide6.QtCore import QCoreApplication, QTimer
from test_daemon import wait_until

import fixtures
import harness  # the Qt application
from hostd_shell import background, menu, views
from hostd_shell import input as inp
from hostd_shell.core import loader, nav
from hostd_shell.core.driver import Driver
from hostd_shell.daemon import Daemon, Events

LOOK = background.load(os.path.join(tempfile.mkdtemp(), "ui.toml"))


class DataLayer(unittest.TestCase):
    def setUp(self):
        self.fake = FakeHostd()
        self.addCleanup(self.fake.close)
        for path, body in fixtures.READS.items():
            self.fake.route("GET", path, body=body)
        self.daemon = Daemon(self.fake.path, "tok")
        path = os.path.join(tempfile.mkdtemp(), "menu.toml")
        with open(path, "w") as f:
            f.write(fixtures.MENU)
        tabs, _ = menu.load(path, lambda name: True)
        self.nav = nav.Navigator(views.categories(tabs), tabs)
        self.said = []
        self.timeout = 5000
        self.loader = loader.Loader(self.nav, lambda p, cb: self.daemon.ask(p, cb, self.timeout), look=lambda: LOOK,
                                    tokens=harness.TOKENS, say=lambda t, e=False: self.said.append((t, e)))

    def load_all(self):
        self.loader.run([nav.Refresh(c.key) for c in self.nav.cats])

    def loaded(self, *keys):
        return lambda: all(self.nav.rows[k] for k in keys)

    def asked(self, path):
        return sum(1 for m, p, _, _ in self.fake.requests if m == "GET" and p == path)

    def test_every_category_loads_and_matches_the_blocking_result(self):
        self.load_all()
        self.assertTrue(wait_until(self.loaded("apps", "users", "settings", "status")))
        ref = nav.Navigator(self.nav.cats, self.nav.tabs)
        Driver(ref, fixtures.FakeAPI(), look=LOOK, tokens=harness.TOKENS).run([nav.Refresh(c.key) for c in ref.cats])
        self.assertEqual({k: [r.key() for r in v] for k, v in self.nav.rows.items()},
                         {k: [r.key() for r in v] for k, v in ref.rows.items()})
        self.assertEqual(self.said, [])

    def test_a_hung_endpoint_holds_back_its_pages_and_nothing_else(self):
        self.fake.hang.add("/v1/instances?all=true")
        self.timeout = 700
        ticks = []
        timer = QTimer(singleShot=False, interval=10)
        timer.timeout.connect(lambda: ticks.append(time.monotonic()))
        timer.start()
        self.load_all()
        self.assertTrue(wait_until(self.loaded("users", "settings", "status")), "the pages that do not need it load")
        self.assertEqual(self.nav.rows["apps"], [], "the page that does is not there yet")
        # and the screen still works: move, change category, open and close an overlay
        for verb in (inp.RIGHT, inp.RIGHT, inp.RIGHT, inp.DOWN, inp.LEFT, inp.HOME, inp.ACTIONS, inp.BACK):
            self.loader.run(self.nav.handle(verb))
            QCoreApplication.processEvents()
        self.assertTrue(wait_until(lambda: self.said, 3), "it says why in the end")
        timer.stop()
        self.assertTrue(self.said[0][0].startswith("Cannot reach hostd:") and "did not answer" in self.said[0][0], self.said)
        gaps = [b - a for a, b in itertools.pairwise(ticks)]
        self.assertLess(max(gaps), 0.15, "the loop stopped while hostd was hanging")

    def test_when_it_comes_back_the_page_fills(self):
        self.fake.hang.add("/v1/instances?all=true")
        self.timeout = 300
        self.load_all()
        self.assertTrue(wait_until(lambda: self.said, 3))
        self.fake.hang.discard("/v1/instances?all=true")
        self.loader.run([nav.Refresh("apps")])
        self.assertTrue(wait_until(self.loaded("apps")))
        self.assertEqual(self.said[-1], ("", False))

    def test_an_event_asks_again_only_what_it_made_stale(self):
        self.load_all()
        self.assertTrue(wait_until(self.loaded("apps", "users", "settings", "status")))
        before = {p: self.asked(p) for p in ("/v1/seat", "/v1/audio", "/v1/display", "/v1/instances", "/v1/version")}
        self.loader.refresh_categories(self.loader.on_event("seat.switched"))
        wait_until(lambda: self.asked("/v1/seat") == before["/v1/seat"] + 1)
        wait_until(lambda: False, 0.1)
        self.assertEqual(self.asked("/v1/seat"), before["/v1/seat"] + 1)
        for path in ("/v1/audio", "/v1/display", "/v1/instances", "/v1/version"):
            self.assertEqual(self.asked(path), before[path], path)

    def test_the_event_stream_drives_it(self):
        events = Events(self.daemon, first_ms=20, max_ms=80)
        self.addCleanup(events.stop)
        events.event.connect(lambda ev: self.loader.refresh_categories(self.loader.on_event(ev.get("type", ""))))
        self.load_all()
        events.start()
        self.assertTrue(wait_until(lambda: self.loaded("apps", "users", "settings", "status")() and events.connected))
        n = self.asked("/v1/seat")
        self.fake.push({"type": "seat.switched", "data": {"user": "guest"}})
        self.assertTrue(wait_until(lambda: self.asked("/v1/seat") == n + 1))
        # hostd comes back after being away: everything is asked again
        total = len(self.fake.requests)
        self.fake.drop_events()
        self.assertTrue(wait_until(lambda: len(self.fake.requests) > total + 6, 5))
        self.assertEqual(self.said, [])


if __name__ == "__main__":
    unittest.main()
