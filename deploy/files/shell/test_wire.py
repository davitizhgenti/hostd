"""The wire codec (hostd_shell.core.wire) with no sockets and no Qt (make lint-scripts)."""

import contextlib
import json
import os
import random
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

from hostd_shell.core import wire  # noqa: E402


def feed_in_pieces(obj, data, rnd, sizes=(1, 2, 3, 7, 50)):
    i = 0
    while i < len(data):
        n = rnd.choice(sizes)
        obj.feed(data[i:i + n])
        i += n


class Requests(unittest.TestCase):
    def test_a_get(self):
        self.assertEqual(wire.request("GET", "/v1/audio", "tok"),
                         b"GET /v1/audio HTTP/1.1\r\nHost: hostd\r\nConnection: close\r\nAuthorization: Bearer tok\r\n\r\n")

    def test_a_post_has_its_body_and_length(self):
        raw = wire.request("POST", "/v1/actions", "t", {"type": "x"})
        head, body = raw.split(b"\r\n\r\n")
        self.assertEqual(json.loads(body), {"type": "x"})
        self.assertIn(b"Content-Length: %d" % len(body), head)
        self.assertIn(b"Content-Type: application/json", head)

    def test_a_post_without_a_body_still_says_so(self):
        self.assertIn(b"Content-Length: 0", wire.request("POST", "/v1/windows/back", "t"))


class Responses(unittest.TestCase):
    def parse(self, raw, rnd=None):
        r = wire.Response()
        if rnd:
            feed_in_pieces(r, raw, rnd)
        else:
            r.feed(raw)
        return r

    def test_content_length_in_any_pieces(self):
        raw = b'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 13\r\n\r\n{"a": [1, 2]}'
        for seed in range(30):
            r = self.parse(raw, random.Random(seed))
            self.assertTrue(r.done, seed)
            self.assertEqual((r.status, r.json()), (200, {"a": [1, 2]}))

    def test_chunked(self):
        raw = (b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\n[1, 2\r\n"
               b"4;ext=1\r\n, 3]\r\r\n0\r\nX-Trailer: y\r\n\r\n")
        raw = raw.replace(b"4;ext=1\r\n, 3]\r\r\n", b"3\r\n, 3\r\n1\r\n]\r\n")
        for seed in range(30):
            r = self.parse(raw, random.Random(seed))
            self.assertTrue(r.done, seed)
            self.assertEqual(r.json(), [1, 2, 3], seed)

    def test_a_body_that_runs_to_the_close(self):
        r = self.parse(b"HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n[1]")
        self.assertFalse(r.done)
        r.eof()
        self.assertEqual(r.json(), [1])

    def test_an_answer_cut_short_is_an_error(self):
        r = self.parse(b"HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n[1")
        with self.assertRaises(wire.ProtocolError):
            r.eof()
        with self.assertRaises(wire.ProtocolError):
            wire.Response().eof()

    def test_no_content_and_upgrade_have_no_body(self):
        self.assertTrue(self.parse(b"HTTP/1.1 204 No Content\r\n\r\n").done)
        r = self.parse(b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n\x81\x02hi")
        self.assertTrue(r.done)
        self.assertEqual(r.rest, b"\x81\x02hi")

    def test_an_error_answer_says_why(self):
        body = json.dumps({"error": {"code": "forbidden", "message": "needs scope apps"}}).encode()
        r = self.parse(b"HTTP/1.1 403 Forbidden\r\nContent-Length: %d\r\n\r\n" % len(body) + body)
        self.assertEqual(r.error(), ("needs scope apps", "forbidden"))
        r = self.parse(b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 4\r\n\r\nnope")
        self.assertEqual(r.error(), ("HTTP 502", ""))

    def test_what_is_not_hostd_is_refused(self):
        for raw in (b"SSH-2.0-OpenSSH\r\n\r\n", b"HTTP/1.1 abc\r\n\r\n", b"HTTP/1.1 200 OK\r\nContent-Length: x\r\n\r\n",
                    b"HTTP/1.1 200 OK\r\nContent-Length: 99999999999\r\n\r\n",
                    b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"):
            with self.assertRaises(wire.ProtocolError, msg=raw):
                self.parse(raw)
        with self.assertRaises(wire.ProtocolError):
            self.parse(b"x" * (70 << 10))


class WebSocket(unittest.TestCase):
    def test_the_accept_key_is_the_rfcs(self):
        self.assertEqual(wire.accept_for("dGhlIHNhbXBsZSBub25jZQ=="), "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=")

    def test_the_upgrade_is_checked(self):
        key = wire.new_key()
        good = wire.Response()
        good.feed(f"HTTP/1.1 101 Switching Protocols\r\nSec-WebSocket-Accept: {wire.accept_for(key)}\r\n\r\n".encode())
        wire.upgraded(good, key)
        for raw in (b"HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n",
                    b"HTTP/1.1 101 Switching Protocols\r\nSec-WebSocket-Accept: nope\r\n\r\n"):
            r = wire.Response()
            r.feed(raw)
            with self.assertRaises(wire.ProtocolError):
                wire.upgraded(r, key)
        up = wire.upgrade("/v1/events?type=*", "tok", key)
        self.assertIn(b"Upgrade: websocket", up)
        self.assertNotIn(b"Connection: close", up)

    def test_frames_of_every_length_round_trip_masked_or_not(self):
        for n in (0, 1, 125, 126, 127, 65535, 65536, 70000):
            for mask in (True, False):
                payload = os.urandom(n)
                got = wire.Frames().feed(wire.frame(wire.BINARY, payload, mask))
                self.assertEqual(got, [(wire.BINARY, payload)], (n, mask))

    def test_fragments_join_and_a_ping_between_comes_out_at_once(self):
        wire_bytes = (b"\x01\x03abc" + b"\x89\x01!" + b"\x00\x02de" + b"\x80\x01f")  # text "abc", ping, "de", "f"
        got = wire.Frames().feed(wire_bytes)
        self.assertEqual(got, [(wire.PING, b"!"), (wire.TEXT, b"abcdef")])

    def test_a_continuation_with_nothing_to_continue_is_refused(self):
        with self.assertRaises(wire.ProtocolError):
            wire.Frames().feed(b"\x80\x01x")

    def test_a_frame_that_is_too_big_is_refused(self):
        with self.assertRaises(wire.ProtocolError):
            wire.Frames().feed(b"\x82\x7f" + (1 << 40).to_bytes(8, "big"))

    def test_arriving_in_any_pieces_changes_nothing(self):
        rnd = random.Random(5)
        msgs = [(wire.TEXT, json.dumps({"n": i, "pad": "x" * rnd.randrange(0, 300)}).encode()) for i in range(40)]
        data = b"".join(wire.frame(op, p, mask=bool(i % 2)) for i, (op, p) in enumerate(msgs))
        for seed in range(20):
            f, got = wire.Frames(), []
            i, r = 0, random.Random(seed)
            while i < len(data):
                n = r.choice((1, 2, 5, 97, 1000))
                got += f.feed(data[i:i + n])
                i += n
            self.assertEqual(got, msgs, seed)

    def test_random_bytes_are_decoded_or_refused_never_worse(self):
        rnd = random.Random(7)
        for _ in range(500):
            data = bytes(rnd.randrange(256) for _ in range(rnd.randrange(1, 200)))
            with contextlib.suppress(wire.ProtocolError):
                wire.Frames().feed(data)
            r = wire.Response()
            try:
                r.feed(data)
                r.eof()
            except wire.ProtocolError:
                pass


if __name__ == "__main__":
    unittest.main()
