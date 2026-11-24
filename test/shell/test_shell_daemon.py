"""The whole Shell on the async client, against a fake hostd over a socket:
it loads, follows hostd's events, and keeps working while hostd hangs."""

import itertools
import os
import tempfile
import time
import unittest

from fakehostd import FakeHostd
from PySide6.QtCore import QTimer
from test_daemon import wait_until

import fixtures
import harness  # the Qt application
from hostd_shell import input as inp
from hostd_shell.daemon import Daemon
from hostd_shell.shell import Shell


class ShellOnDaemon(unittest.TestCase):
    def setUp(self):
        self.fake = FakeHostd()
        self.addCleanup(self.fake.close)
        for path, body in fixtures.READS.items():
            self.fake.route("GET", path, body=body)
        self.fake.route("POST", "/v1/actions", body={"status": "applied"})
        self.shell = Shell(None, harness.TOKENS, now=lambda: fixtures.NOW, daemon=Daemon(self.fake.path, "tok"),
                           look_path=os.path.join(tempfile.mkdtemp(), "ui.toml"), menu_path="/nonexistent/menu.toml")
        self.shell.start()
        self.addCleanup(self.stop)

    def stop(self):
        self.shell.events.stop()

    def asked(self, path):
        return sum(1 for m, p, _, _ in self.fake.requests if m == "GET" and p == path)

    def open(self):
        self.assertTrue(wait_until(lambda: self.shell.events.connected and self.shell.menu.page().rows))
        self.shell.showMenu()
        self.shell.menu.setMenuActive(True)
        wait_until(lambda: False, 0.4)  # hostd.connected has everything listed again; let that finish

    def test_it_loads_and_a_controller_drives_it_through_the_event_stream(self):
        self.open()
        first = self.shell.menu.current_row().title
        self.fake.push({"type": "display.nav", "data": {"what": "down", "family": "xbox"}})
        self.assertTrue(wait_until(lambda: self.shell.menu.current_row().title != first))
        self.assertEqual(self.shell.menu.hintList[0]["button"], "A")  # the buttons of the pad that was used

    def test_a_controller_is_ignored_while_the_menu_is_not_in_front(self):
        self.assertTrue(wait_until(lambda: self.shell.events.connected and self.shell.menu.page().rows))
        first = self.shell.menu.current_row().title
        self.fake.push({"type": "display.nav", "data": {"what": "down"}})
        wait_until(lambda: False, 0.2)
        self.assertEqual(self.shell.menu.current_row().title, first)

    def test_an_event_lists_again_only_what_it_changed_and_shows_the_osd(self):
        self.open()
        before = (self.asked("/v1/audio"), self.asked("/v1/seat"))
        self.fake.push({"type": "audio.volume.changed", "data": {"percent": 40}})
        self.assertTrue(wait_until(lambda: self.asked("/v1/audio") > before[0]))
        wait_until(lambda: False, 0.1)
        self.assertEqual(self.asked("/v1/seat"), before[1])
        self.assertEqual((self.shell.osd.visible, self.shell.osd.value), (True, "40%"))

    def test_choosing_sends_the_call_and_lists_again_behind_it(self):
        self.open()
        self.shell.menu.handle(inp.RIGHT)  # Users
        self.shell.menu.handle(inp.RIGHT)  # Settings
        for _ in range(12):
            row = self.shell.menu.current_row()
            if row is not None and row.call is not None and row.kind != "slider":
                break
            self.shell.menu.handle(inp.DOWN)
        n = len(self.fake.requests)
        self.shell.menu.handle(inp.ACTIVATE)
        self.assertTrue(wait_until(lambda: any(m == "POST" for m, *_ in self.fake.requests[n:])))

    def test_the_screen_keeps_moving_while_hostd_hangs(self):
        self.open()
        self.fake.hang.add("/v1/seat")
        ticks = []
        timer = QTimer(singleShot=False, interval=10)
        timer.timeout.connect(lambda: ticks.append(time.monotonic()))
        timer.start()
        self.fake.push({"type": "seat.switched", "data": {"user": "guest"}})
        for verb in (inp.RIGHT, inp.RIGHT, inp.DOWN, inp.LEFT, inp.ACTIONS, inp.BACK, inp.HOME) * 3:
            self.shell.menu.handle(verb)
            wait_until(lambda: False, 0.02)
        timer.stop()
        gaps = [b - a for a, b in itertools.pairwise(ticks)]
        self.assertLess(max(gaps), 0.15, "the screen stopped while hostd was hanging")
        self.assertEqual(self.shell.menu.category, 0)


if __name__ == "__main__":
    unittest.main()
