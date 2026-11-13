"""hostd's API over its unix socket, and its event stream.

Only the standard library: the UI's programs and their tests use it without
GTK. Requests carry the local token (~/.config/hostd/local.token, or
HOSTD_TOKEN), so what is chosen on the screen counts as someone at it.
"""

import base64
import http.client
import json
import os
import socket
import struct
import threading
import time


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path, timeout=10):
        super().__init__("hostd", timeout=timeout)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


class APIError(Exception):
    """hostd answered with an error; code is its error code (forbidden, ...)."""

    def __init__(self, message, code="", status=0):
        super().__init__(message)
        self.code, self.status = code, status


class API:
    def __init__(self, socket_path=None, token=None):
        runtime = os.environ.get("XDG_RUNTIME_DIR", f"/run/user/{os.getuid()}")
        self.socket = socket_path or os.environ.get("HOSTD_SOCKET", os.path.join(runtime, "hostd.sock"))
        self.token = token if token is not None else (os.environ.get("HOSTD_TOKEN") or self._token_file())

    @staticmethod
    def _token_file():
        config = os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config"))
        try:
            with open(os.path.join(config, "hostd", "local.token")) as f:
                return f.read().strip()
        except OSError:
            return ""

    def call(self, method, path, body=None):
        conn = UnixHTTPConnection(self.socket)
        try:
            headers = {"Authorization": "Bearer " + self.token}
            data = None
            if body is not None:
                data = json.dumps(body).encode()
                headers["Content-Type"] = "application/json"
            conn.request(method, path, body=data, headers=headers)
            resp = conn.getresponse()
            payload = json.loads(resp.read() or b"null")
            if resp.status >= 400:
                err = (payload or {}).get("error", {})
                raise APIError(err.get("message") or f"HTTP {resp.status}", err.get("code", ""), resp.status)
            return payload
        finally:
            conn.close()

    def get(self, path):
        """A read, at its own path: /v1/audio, /v1/display, /v1/seat..."""
        return self.call("GET", path)

    def do(self, action, args=None):
        """Sends an action by type (POST /v1/actions): {"status": "applied" | "skipped", ...}."""
        return self.call("POST", "/v1/actions", {"type": action, "args": args or {}})


class EventStream(threading.Thread):
    """Follows hostd's events (GET /v1/events, a WebSocket) and hands each to
    post(on_event, event), which runs it on the UI's main loop.
    Reconnects with a growing pause when the connection drops. A minimal
    client: hostd sends unmasked text frames; the client answers pings and
    masks what it sends, as RFC 6455 asks."""

    def __init__(self, api, on_event, post, types="*"):
        super().__init__(daemon=True)
        self.api, self.on_event, self.post, self.types = api, on_event, post, types
        self.connected = False

    def run(self):
        pause = 1
        while True:
            try:
                self.follow()
                pause = 1
            except (OSError, ValueError):  # hostd away or restarting; try again
                pass
            self.connected = False
            time.sleep(pause)
            pause = min(pause * 2, 30)

    def follow(self):
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(10)  # for connecting and the handshake
        sock.connect(self.api.socket)
        try:
            key = base64.b64encode(os.urandom(16)).decode()
            sock.sendall((
                f"GET /v1/events?type={self.types} HTTP/1.1\r\nHost: hostd\r\nUpgrade: websocket\r\n"
                "Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"
                f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Protocol: hostd.v1\r\n"
                f"Authorization: Bearer {self.api.token}\r\n\r\n").encode())
            head = b""
            while b"\r\n\r\n" not in head:
                chunk = sock.recv(1024)
                if not chunk:
                    raise ValueError("connection closed during the handshake")
                head += chunk
            head, rest = head.split(b"\r\n\r\n", 1)
            if not head.startswith(b"HTTP/1.1 101"):
                raise ValueError(head.split(b"\r\n", 1)[0].decode(errors="replace"))
            # A local socket: when hostd stops, reads end at once, so a quiet
            # stream can wait for as long as it stays quiet.
            sock.settimeout(None)
            self.connected = True
            self.post(self.on_event, {"type": "hostd.connected"})
            reader = Frames(sock, rest)
            while True:
                opcode, payload = reader.next()
                if opcode == 0x1:  # text: one event
                    self.post(self.on_event, json.loads(payload))
                elif opcode == 0x9:  # ping
                    send_frame(sock, 0xA, payload)
                elif opcode == 0x8:  # close
                    return
        finally:
            sock.close()


class Frames:
    """Reads WebSocket frames (unmasked, from the server)."""

    def __init__(self, sock, buffered=b""):
        self.sock, self.buf = sock, buffered
        # A message in fragments, across a control frame between them.
        self.message, self.opcode = b"", None

    def _read(self, n):
        while len(self.buf) < n:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise ValueError("connection closed")
            self.buf += chunk
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def next(self):
        while True:
            b0, b1 = self._read(2)
            n = b1 & 0x7F
            if n == 126:
                n = struct.unpack(">H", self._read(2))[0]
            elif n == 127:
                n = struct.unpack(">Q", self._read(8))[0]
            mask = self._read(4) if b1 & 0x80 else None
            data = self._read(n)
            if mask:
                data = bytes(c ^ mask[i % 4] for i, c in enumerate(data))
            op = b0 & 0x0F
            if op >= 0x8:  # control frames come whole, even inside a message
                return op, data
            if op != 0:
                self.opcode = op
            self.message += data
            if b0 & 0x80:  # FIN
                out = (self.opcode, self.message)
                self.message, self.opcode = b"", None
                return out


def send_frame(sock, opcode, payload):
    mask = os.urandom(4)
    head = bytes([0x80 | opcode, 0x80 | len(payload)])  # control frames: < 126 bytes
    sock.sendall(head + mask + bytes(c ^ mask[i % 4] for i, c in enumerate(payload)))
