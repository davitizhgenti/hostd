"""A hostd for tests: HTTP over a unix socket and the /v1/events WebSocket,
in a thread, with what the tests need to go wrong: an endpoint that never
answers, a chunked answer, a stream that is dropped, a refused upgrade."""

import contextlib
import json
import os
import socket
import socketserver
import tempfile
import threading
import time

from hostd_shell.core import wire


class FakeHostd:
    def __init__(self):
        self.path = os.path.join(tempfile.mkdtemp(), "hostd.sock")
        self.routes = {}  # (method, path) -> (status, json) | callable(body) -> (status, json)
        self.requests = []  # (method, path, authorization, body)
        self.hang = set()  # paths that never answer
        self.chunked = set()  # paths answered in chunks
        self.refuse_events = 0  # that many upgrades are answered 401
        self.streams = []  # the open event connections
        self.lock = threading.Lock()
        self.pongs = []
        fake = self

        class Handler(socketserver.BaseRequestHandler):
            def handle(self):
                fake._serve(self.request)

        class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
            daemon_threads = True
            allow_reuse_address = True

        self.server = Server(self.path, Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.drop_events()
        self.server.shutdown()
        self.server.server_close()

    # --- the tests' hands ---

    def route(self, method, path, status=200, body=None):
        self.routes[(method, path)] = (status, body)

    def push(self, event):
        with self.lock:
            streams = list(self.streams)
        for s in streams:
            with contextlib.suppress(OSError):
                s.sendall(wire.frame(wire.TEXT, json.dumps(event).encode(), mask=False))

    def ping(self, payload=b"hi"):
        with self.lock:
            streams = list(self.streams)
        for s in streams:
            s.sendall(wire.frame(wire.PING, payload, mask=False))

    def drop_events(self):
        with self.lock:
            streams, self.streams = list(self.streams), []
        for s in streams:
            with contextlib.suppress(OSError):
                s.shutdown(socket.SHUT_RDWR)
            s.close()

    def streams_open(self):
        with self.lock:
            return len(self.streams)

    # --- serving ---

    def _read_request(self, conn):
        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = conn.recv(4096)
            if not chunk:
                return None
            buf += chunk
        head, rest = buf.split(b"\r\n\r\n", 1)
        lines = head.decode("latin-1").split("\r\n")
        method, path, _ = lines[0].split(" ", 2)
        headers = {k.strip().lower(): v.strip() for k, _, v in (ln.partition(":") for ln in lines[1:])}
        n = int(headers.get("content-length", "0"))
        while len(rest) < n:
            rest += conn.recv(4096)
        return method, path, headers, rest[:n]

    def _serve(self, conn):
        req = self._read_request(conn)
        if req is None:
            return
        method, path, headers, body = req
        self.requests.append((method, path, headers.get("authorization", ""), body))
        if path.startswith("/v1/events"):
            return self._events(conn, headers)
        if path in self.hang:
            time.sleep(30)
            return
        route = self.routes.get((method, path)) or self.routes.get((method, path.split("?")[0]))
        if route is None:
            status, payload = 404, {"error": {"code": "not_found", "message": f"no {path}"}}
        else:
            status, payload = route
            if callable(status):
                status, payload = status(json.loads(body) if body else None)
        data = json.dumps(payload).encode()
        if path in self.chunked:
            head = f"HTTP/1.1 {status} X\r\nTransfer-Encoding: chunked\r\n\r\n".encode()
            mid = len(data) // 2
            conn.sendall(head + b"%x\r\n%s\r\n" % (mid, data[:mid]))
            time.sleep(0.02)
            conn.sendall(b"%x\r\n%s\r\n0\r\n\r\n" % (len(data) - mid, data[mid:]))
        else:
            head = f"HTTP/1.1 {status} X\r\nContent-Type: application/json\r\nContent-Length: {len(data)}\r\n\r\n"
            conn.sendall(head.encode() + data)

    def _events(self, conn, headers):
        if self.refuse_events > 0:
            self.refuse_events -= 1
            conn.sendall(b"HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n")
            return
        key = headers["sec-websocket-key"]
        conn.sendall(("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                      f"Sec-WebSocket-Accept: {wire.accept_for(key)}\r\n\r\n").encode())
        with self.lock:
            self.streams.append(conn)
        frames = wire.Frames()
        try:
            while True:
                data = conn.recv(4096)
                if not data:
                    return
                for op, payload in frames.feed(data):
                    if op == wire.PONG:
                        self.pongs.append(payload)
        except OSError:
            return
        finally:
            with self.lock:
                if conn in self.streams:
                    self.streams.remove(conn)
