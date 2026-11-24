"""The async client (hostd_shell.daemon) against a fake hostd: answers and
errors, a hostd that never answers while the screen keeps running, and an
event stream that drops and comes back."""

import itertools
import time
import unittest

from fakehostd import FakeHostd
from PySide6.QtCore import QCoreApplication, QTimer

from hostd_shell.daemon import Daemon, Events


def wait_until(cond, seconds=5.0):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        QCoreApplication.processEvents()
        if cond():
            return True
        time.sleep(0.002)
    return cond()


class Base(unittest.TestCase):
    def setUp(self):
        self.fake = FakeHostd()
        self.daemon = Daemon(self.fake.path, "tok")
        self.addCleanup(self.fake.close)

    def ask(self, method, path, body=None, timeout_ms=5000):
        got = []
        self.daemon.request(method, path, body, got.append, timeout_ms)
        self.assertTrue(wait_until(lambda: got), f"{method} {path} was never answered")
        return got[0]


class Requests(Base):
    def test_a_read(self):
        self.fake.route("GET", "/v1/version", body={"version": "edge-1", "dev": False})
        r = self.ask("GET", "/v1/version")
        self.assertEqual((r.ok, r.status, r.payload), (True, 200, {"version": "edge-1", "dev": False}))
        self.assertEqual(self.fake.requests[-1][2], "Bearer tok")

    def test_a_post_carries_its_body(self):
        self.fake.route("POST", "/v1/actions", body={"status": "applied"})
        r = self.ask("POST", "/v1/actions", {"type": "audio.volume.set", "args": {"percent": 40}})
        self.assertTrue(r.ok)
        self.assertIn(b'"audio.volume.set"', self.fake.requests[-1][3])

    def test_an_error_answer_says_why(self):
        self.fake.route("POST", "/v1/actions", 403, {"error": {"code": "forbidden", "message": "needs scope apps"}})
        r = self.ask("POST", "/v1/actions", {"type": "x"})
        self.assertEqual((r.ok, r.status, r.code, r.message, r.reached), (False, 403, "forbidden", "needs scope apps", True))

    def test_an_unknown_path_is_hostds_not_found(self):
        r = self.ask("GET", "/v1/nothing")
        self.assertEqual((r.ok, r.status, r.code), (False, 404, "not_found"))

    def test_a_chunked_answer(self):
        self.fake.route("GET", "/v1/big", body={"items": list(range(500))})
        self.fake.chunked.add("/v1/big")
        r = self.ask("GET", "/v1/big")
        self.assertEqual(r.payload["items"][-1], 499)

    def test_many_at_once(self):
        self.fake.route("GET", "/v1/audio", body={"percent": 40})
        got = []
        for _ in range(40):
            self.daemon.get("/v1/audio", got.append)
        self.assertTrue(wait_until(lambda: len(got) == 40))
        self.assertTrue(all(r.ok for r in got))
        self.assertEqual(self.daemon.pending(), 0)

    def test_a_cancelled_request_never_calls_back(self):
        self.fake.route("GET", "/v1/audio", body={})
        got = []
        h = self.daemon.get("/v1/audio", got.append)
        self.daemon.cancel(h)
        wait_until(lambda: False, 0.2)
        self.assertEqual((got, self.daemon.pending()), ([], 0))

    def test_no_hostd_is_an_error_at_once(self):
        d = Daemon("/nonexistent/hostd.sock", "tok")
        got = []
        start = time.monotonic()
        d.get("/v1/version", got.append)
        self.assertTrue(wait_until(lambda: got))
        self.assertFalse(got[0].ok)
        self.assertFalse(got[0].reached)
        self.assertIn("cannot reach hostd", got[0].message)
        self.assertLess(time.monotonic() - start, 1.0)

    def test_something_that_is_not_hostd_is_refused(self):
        self.fake.route("GET", "/v1/junk", body=None)
        # an HTTP answer that is not JSON
        self.fake.routes[("GET", "/v1/junk")] = (200, None)
        r = self.ask("GET", "/v1/junk")
        self.assertTrue(r.ok)  # null is JSON
        self.fake.server.RequestHandlerClass.handle = lambda self: self.request.sendall(b"SSH-2.0-nope\r\n\r\n")
        r = self.ask("GET", "/v1/version")
        self.assertFalse(r.ok)
        self.assertTrue(r.reached)


class HungHostd(Base):
    def test_the_screen_keeps_running_while_hostd_never_answers(self):
        self.fake.hang.add("/v1/instances")
        ticks = []
        timer = QTimer(singleShot=False, interval=10)
        timer.timeout.connect(lambda: ticks.append(time.monotonic()))
        timer.start()
        got, start = [], time.monotonic()
        self.daemon.get("/v1/instances", got.append, timeout_ms=400)
        self.assertTrue(wait_until(lambda: got, 3))
        timer.stop()
        r = got[0]
        self.assertEqual((r.ok, r.message), (False, "hostd did not answer"))
        self.assertGreater(time.monotonic() - start, 0.35)
        self.assertLess(time.monotonic() - start, 1.5)
        gaps = [b - a for a, b in itertools.pairwise(ticks)]
        self.assertGreater(len(ticks), 15, "the loop barely ran")
        self.assertLess(max(gaps), 0.15, "the loop stopped while waiting for hostd")

    def test_other_requests_are_answered_while_one_hangs(self):
        self.fake.hang.add("/v1/instances")
        self.fake.route("GET", "/v1/audio", body={"percent": 40})
        slow, fast = [], []
        self.daemon.get("/v1/instances", slow.append, timeout_ms=600)
        self.daemon.get("/v1/audio", fast.append)
        self.assertTrue(wait_until(lambda: fast, 1.0))
        self.assertTrue(fast[0].ok)
        self.assertEqual(slow, [])
        self.assertTrue(wait_until(lambda: slow, 2.0))


class EventStream(Base):
    def stream(self, **kw):
        events = Events(self.daemon, first_ms=20, max_ms=80, **kw)
        got = []
        events.event.connect(got.append)
        self.addCleanup(events.stop)
        events.start()
        return events, got

    def test_events_arrive_in_order_after_hostd_connected(self):
        events, got = self.stream()
        self.assertTrue(wait_until(lambda: events.connected))
        for i in range(30):
            self.fake.push({"type": "audio.volume.changed", "n": i})
        self.assertTrue(wait_until(lambda: len(got) == 31))
        self.assertEqual(got[0], {"type": "hostd.connected"})
        self.assertEqual([e["n"] for e in got[1:]], list(range(30)))
        self.assertEqual(self.fake.requests[-1][2], "Bearer tok")

    def test_a_ping_is_answered_with_a_pong(self):
        events, _ = self.stream()
        self.assertTrue(wait_until(lambda: events.connected))
        self.fake.ping(b"are you there")
        self.assertTrue(wait_until(lambda: self.fake.pongs))
        self.assertEqual(self.fake.pongs[0], b"are you there")

    def test_a_dropped_stream_comes_back(self):
        events, got = self.stream()
        states = []
        events.opened.connect(lambda: states.append("opened"))
        events.closed.connect(lambda: states.append("closed"))
        self.assertTrue(wait_until(lambda: events.connected))
        self.fake.drop_events()
        self.assertTrue(wait_until(lambda: "closed" in states))
        self.assertTrue(wait_until(lambda: events.connected and self.fake.streams_open() == 1))
        self.fake.push({"type": "seat.switched"})
        self.assertTrue(wait_until(lambda: got and got[-1]["type"] == "seat.switched"))
        self.assertEqual([e["type"] for e in got if e["type"] == "hostd.connected"], ["hostd.connected"] * 2)

    def test_a_refused_upgrade_is_tried_again_later(self):
        self.fake.refuse_events = 2
        events, got = self.stream()
        self.assertTrue(wait_until(lambda: events.connected, 3))
        self.assertEqual(self.fake.refuse_events, 0)
        self.assertEqual([e["type"] for e in got], ["hostd.connected"])

    def test_no_hostd_means_waiting_and_trying_again_without_a_crash(self):
        d = Daemon("/nonexistent/hostd.sock", "tok")
        events = Events(d, first_ms=10, max_ms=20)
        events.start()
        self.addCleanup(events.stop)
        wait_until(lambda: False, 0.2)
        self.assertFalse(events.connected)

    def test_stopping_ends_it_and_does_not_reconnect(self):
        events, _ = self.stream()
        self.assertTrue(wait_until(lambda: events.connected))
        events.stop()
        self.assertTrue(wait_until(lambda: self.fake.streams_open() == 0))
        wait_until(lambda: False, 0.2)
        self.assertEqual((events.connected, self.fake.streams_open()), (False, 0))


if __name__ == "__main__":
    unittest.main()
