"""Tests for hostd.py against a fake API: python3 -m unittest (make lint-scripts)."""

import hashlib
import json
import os
import shutil
import ssl
import subprocess
import tempfile
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
        cls.core = Hostd("http://127.0.0.1:%d" % cls.srv.server_address[1], token="hostd_test")

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


@unittest.skipUnless(shutil.which("openssl"), "needs openssl to make a CA")
class PinnedTest(unittest.TestCase):
    """Over HTTPS, the client trusts the pinned CA and nothing else."""

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        d = cls.dir

        def run(*args):
            subprocess.run(args, check=True, capture_output=True, cwd=d)

        for name in ("ca", "other"):
            run("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
                "-keyout", name + "-key.pem", "-out", name + ".pem", "-days", "1", "-subj", "/CN=hostd " + name,
                "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign")
        run("openssl", "req", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
            "-keyout", "key.pem", "-out", "leaf.csr", "-subj", "/CN=core")
        with open(os.path.join(d, "ext"), "w") as f:
            f.write("subjectAltName=DNS:core.local\nextendedKeyUsage=serverAuth\n")
        run("openssl", "x509", "-req", "-in", "leaf.csr", "-CA", "ca.pem", "-CAkey", "ca-key.pem", "-CAcreateserial",
            "-out", "leaf.pem", "-days", "1", "-extfile", "ext")
        with open(os.path.join(d, "chain.pem"), "w") as f:
            f.write(open(os.path.join(d, "leaf.pem")).read() + open(os.path.join(d, "ca.pem")).read())

        ca_pem = open(os.path.join(d, "ca.pem")).read()

        class TLSAPI(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                body = ca_pem.encode() if self.path == "/v1/tls/ca.pem" else b'{"version": "edge-test"}'
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        cls.srv = ThreadingHTTPServer(("127.0.0.1", 0), TLSAPI)
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(os.path.join(d, "chain.pem"), os.path.join(d, "key.pem"))
        cls.srv.socket = ctx.wrap_socket(cls.srv.socket, server_side=True)
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
        cls.url = "https://127.0.0.1:%d" % cls.srv.server_address[1]

        def fingerprint(name):
            return hashlib.sha256(ssl.PEM_cert_to_DER_cert(open(os.path.join(d, name)).read())).hexdigest()

        cls.pin, cls.other_pin = fingerprint("ca.pem"), fingerprint("other.pem")

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        shutil.rmtree(cls.dir)

    def test_pinned(self):
        # By IP, though the certificate names core.local only.
        self.assertEqual(Hostd(self.url, ca_fingerprint=self.pin).get("/v1/version"), {"version": "edge-test"})
        self.assertEqual(Hostd(self.url, ca_file=os.path.join(self.dir, "ca.pem")).get("/v1/version")["version"], "edge-test")

    def test_another_ca_is_refused(self):
        with self.assertRaises(HostdError) as e:
            Hostd(self.url, ca_fingerprint=self.other_pin).get("/v1/version")
        self.assertEqual(e.exception.code, "ca_mismatch")
        with self.assertRaises(OSError):  # urllib wraps the SSL error
            Hostd(self.url, ca_file=os.path.join(self.dir, "other.pem")).get("/v1/version")

    def test_unpinned_uses_the_system_cas(self):
        with self.assertRaises(OSError):  # the CA is not one the system trusts
            Hostd(self.url).get("/v1/version")

    def test_address_defaults(self):
        self.assertEqual(Hostd("core").url, "https://core:7300")
        self.assertEqual(Hostd("192.168.18.33:8000").url, "https://192.168.18.33:8000")
        self.assertEqual(Hostd("http://[fd00::1]").url, "http://[fd00::1]:7300")


if __name__ == "__main__":
    unittest.main()
