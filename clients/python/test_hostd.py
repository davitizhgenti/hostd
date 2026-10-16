"""Tests for hostd.py against a fake API: python3 -m unittest (make lint-scripts)."""

import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from hostd import Hostd, HostdError


class FakeAPI(BaseHTTPRequestHandler):
    sent = []  # (method, path, body, token)
    polls = 0

    def log_message(self, *args):
        pass

    def answer(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        FakeAPI.sent.append(("POST", self.path, body, self.headers.get("Authorization")))
        if body["type"] == "app.run" and body["args"]["command"][0] == "forbidden":
            return self.answer(403, {"error": {"code": "forbidden", "message": "the token lacks scope run"}})
        if body["type"] in ("app.run", "app.start"):
            data = {"instance": "run-tests", "state": "running"}
            return self.answer(200, {"action": "act_1", "status": "applied", "data": data})
        self.answer(200, {"action": "act_2", "status": "applied", "data": {}})

    def do_GET(self):
        FakeAPI.sent.append(("GET", self.path, None, self.headers.get("Authorization")))
        if self.path == "/v1/instances/run-tests":
            FakeAPI.polls += 1
            if FakeAPI.polls < 3:
                return self.answer(200, {"id": "run-tests", "state": "running"})
            return self.answer(200, {"id": "run-tests", "state": "failed", "ended": "2026-10-16T10:00:00Z", "exit_code": 1})
        if self.path.startswith("/v1/instances/run-tests/logs?"):
            return self.answer(200, {"lines": ["collected 2 items", "1 failed, 1 passed"]})
        self.answer(404, {"error": {"code": "not_found", "message": "no read " + self.path}})


class ClientTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = ThreadingHTTPServer(("127.0.0.1", 0), FakeAPI)
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
        cls.core = Hostd("127.0.0.1:%d" % cls.srv.server_address[1], token="hostd_test")

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()

    def setUp(self):
        FakeAPI.sent, FakeAPI.polls = [], 0

    def test_run_and_wait(self):
        done = self.core.run(["pytest", "-q"], name="tests", background=True, env={"CI": "1"}, wait=True, poll=0.01)
        self.assertEqual((done.instance, done.exit_code, done.ok), ("run-tests", 1, False))
        self.assertEqual(done.lines, ["collected 2 items", "1 failed, 1 passed"])
        method, path, body, token = FakeAPI.sent[0]
        self.assertEqual((method, path, token), ("POST", "/v1/actions", "Bearer hostd_test"))
        self.assertEqual(body, {"type": "app.run", "args": {"command": ["pytest", "-q"], "name": "tests",
                                                            "background": True, "env": {"CI": "1"}}})
        self.assertIn("plain=true", FakeAPI.sent[-1][1])

    def test_start_with_arguments(self):
        self.assertEqual(self.core.start("show", file="/home/screen/plot.png"), "run-tests")
        want = {"type": "app.start", "args": {"id": "show", "args": {"file": "/home/screen/plot.png"}}}
        self.assertEqual(FakeAPI.sent[0][2], want)

    def test_refusals_raise(self):
        with self.assertRaises(HostdError) as e:
            self.core.run(["forbidden"])
        self.assertEqual((e.exception.code, e.exception.status), ("forbidden", 403))
        with self.assertRaises(HostdError) as e:
            self.core.get("/v1/nothing")
        self.assertEqual(e.exception.code, "not_found")

    def test_define(self):
        self.core.define("show", 'runner = { type = "exec", command = ["imv", "{file}"] }')
        self.assertEqual(FakeAPI.sent[0][2]["type"], "app.define")
        self.assertEqual(FakeAPI.sent[0][2]["args"]["id"], "show")


if __name__ == "__main__":
    unittest.main()
