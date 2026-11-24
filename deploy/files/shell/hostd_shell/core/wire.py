"""hostd's wire protocol, as bytes in and bytes out: HTTP/1.1 requests and
responses over a socket, and the WebSocket of GET /v1/events. No sockets and
no Qt here: the caller feeds what it received and sends what comes back.
"""

import base64
import hashlib
import json
import os
import struct

WS_GUID = b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
MAX_BODY = 8 << 20  # a response bigger than this is not hostd's


class ProtocolError(ValueError):
    """What arrived is not what hostd sends."""


def request(method, path, token="", body=None, extra=(), close=True):
    """An HTTP/1.1 request, closing after its answer. body: JSON-able or None."""
    head = [f"{method} {path} HTTP/1.1", "Host: hostd"]
    if close:
        head.append("Connection: close")
    if token:
        head.append(f"Authorization: Bearer {token}")
    data = b""
    if body is not None:
        data = json.dumps(body).encode()
        head += ["Content-Type: application/json"]
    if data or method in ("POST", "PUT", "PATCH"):
        head.append(f"Content-Length: {len(data)}")
    head += list(extra)
    return ("\r\n".join(head) + "\r\n\r\n").encode() + data


class Response:
    """An HTTP response, fed as it arrives. Content-Length, chunked and
    read-to-close bodies. done is True once the answer is whole."""

    def __init__(self):
        self.buf = b""
        self.status, self.reason, self.headers = 0, "", {}
        self.body = b""
        self.done = False
        self._head = False
        self._chunked = False
        self._length = None
        self.rest = b""  # bytes after the head, for an upgrade (101)

    def feed(self, data):
        if self.done:
            return
        self.buf += data
        if not self._head:
            if b"\r\n\r\n" not in self.buf:
                if len(self.buf) > 64 << 10:
                    raise ProtocolError("the response's head is too long")
                return
            head, self.buf = self.buf.split(b"\r\n\r\n", 1)
            self._parse_head(head)
            self._head = True
            if self.status == 101 or self.status in (204, 304) or 100 <= self.status < 200:
                self.rest, self.buf, self.done = self.buf, b"", True
                return
        self._body()

    def _parse_head(self, head):
        lines = head.decode("latin-1").split("\r\n")
        parts = lines[0].split(" ", 2)
        if len(parts) < 2 or not parts[0].startswith("HTTP/1.") or not parts[1].isdigit():
            raise ProtocolError(f"not an HTTP response: {lines[0][:60]!r}")
        self.status, self.reason = int(parts[1]), parts[2] if len(parts) > 2 else ""
        for line in lines[1:]:
            k, _, v = line.partition(":")
            self.headers[k.strip().lower()] = v.strip()
        self._chunked = "chunked" in self.headers.get("transfer-encoding", "").lower()
        if "content-length" in self.headers:
            if not self.headers["content-length"].isdigit():
                raise ProtocolError("bad Content-Length")
            self._length = int(self.headers["content-length"])
            if self._length > MAX_BODY:
                raise ProtocolError("the response is too big")

    def _body(self):
        if self._chunked:
            while True:
                if b"\r\n" not in self.buf:
                    return
                size_line, after = self.buf.split(b"\r\n", 1)
                try:
                    size = int(size_line.split(b";")[0], 16)
                except ValueError:
                    raise ProtocolError("bad chunk size") from None
                if size == 0:  # the trailers end with an empty line
                    if b"\r\n" in after:
                        self.done, self.buf = True, b""
                    return
                if len(after) < size + 2:
                    return
                self.body += after[:size]
                if len(self.body) > MAX_BODY:
                    raise ProtocolError("the response is too big")
                self.buf = after[size + 2:]
        elif self._length is not None:
            if len(self.buf) >= self._length:
                self.body, self.buf, self.done = self.buf[:self._length], b"", True
        elif len(self.buf) > MAX_BODY:
            raise ProtocolError("the response is too big")

    def eof(self):
        """The connection closed. A body that runs to the close ends here;
        any other answer cut short is an error."""
        if self.done:
            return
        if self._head and self._length is None and not self._chunked:
            self.body, self.buf, self.done = self.buf, b"", True
            return
        raise ProtocolError("the connection closed before the answer was whole")

    def json(self):
        return json.loads(self.body or b"null")

    def error(self):
        """(message, code) of an error answer (hostd's {"error": {...}})."""
        try:
            err = (self.json() or {}).get("error", {})
        except (ValueError, AttributeError):
            err = {}
        return err.get("message") or f"HTTP {self.status}", err.get("code", "")


# --- WebSocket ---------------------------------------------------------------


def new_key():
    return base64.b64encode(os.urandom(16)).decode()


def accept_for(key):
    return base64.b64encode(hashlib.sha1(key.encode() + WS_GUID).digest()).decode()


def upgrade(path, token, key):
    """The request that turns GET path into a WebSocket."""
    return request("GET", path, token, extra=(
        "Upgrade: websocket", "Connection: Upgrade", "Sec-WebSocket-Version: 13",
        f"Sec-WebSocket-Key: {key}", "Sec-WebSocket-Protocol: hostd.v1"), close=False)


def upgraded(response, key):
    """Checks the answer to upgrade(): raises ProtocolError if it is not one."""
    if response.status != 101:
        raise ProtocolError(f"HTTP {response.status} {response.reason}".strip())
    if response.headers.get("sec-websocket-accept") != accept_for(key):
        raise ProtocolError("the server's WebSocket accept key is wrong")


TEXT, BINARY, CLOSE, PING, PONG = 0x1, 0x2, 0x8, 0x9, 0xA


def frame(opcode, payload=b"", mask=True):
    """One WebSocket frame, final. A client masks (RFC 6455); a server does not."""
    n = len(payload)
    head = bytearray([0x80 | opcode])
    flag = 0x80 if mask else 0
    if n < 126:
        head.append(flag | n)
    elif n < 1 << 16:
        head += bytes([flag | 126]) + struct.pack(">H", n)
    else:
        head += bytes([flag | 127]) + struct.pack(">Q", n)
    if not mask:
        return bytes(head) + payload
    key = os.urandom(4)
    return bytes(head) + key + bytes(c ^ key[i % 4] for i, c in enumerate(payload))


class Frames:
    """Decodes WebSocket frames as they arrive: feed() returns the messages
    and control frames that became whole, as (opcode, payload). A message
    in fragments comes out joined; a control frame between fragments comes
    out at once."""

    def __init__(self):
        self.buf = b""
        self._message, self._opcode = b"", None

    def feed(self, data):
        self.buf += data
        out = []
        while True:
            item = self._one()
            if item is None:
                return out
            if item is not False:
                out.append(item)

    def _one(self):
        b = self.buf
        if len(b) < 2:
            return None
        n, masked, pos = b[1] & 0x7F, bool(b[1] & 0x80), 2
        if n == 126:
            if len(b) < 4:
                return None
            n, pos = struct.unpack(">H", b[2:4])[0], 4
        elif n == 127:
            if len(b) < 10:
                return None
            n, pos = struct.unpack(">Q", b[2:10])[0], 10
        if n > MAX_BODY:
            raise ProtocolError("a WebSocket frame is too big")
        key = b[pos:pos + 4] if masked else None
        pos += 4 if masked else 0
        if len(b) < pos + n or (masked and len(key) < 4):
            return None
        data = b[pos:pos + n]
        self.buf = b[pos + n:]
        if key:
            data = bytes(c ^ key[i % 4] for i, c in enumerate(data))
        op = b[0] & 0x0F
        if op >= 0x8:  # control frames come whole, even inside a message
            return (op, data)
        if op != 0:
            self._opcode = op
        elif self._opcode is None:
            raise ProtocolError("a continuation with nothing to continue")
        self._message += data
        if len(self._message) > MAX_BODY:
            raise ProtocolError("a WebSocket message is too big")
        if b[0] & 0x80:  # FIN
            out = (self._opcode, self._message)
            self._message, self._opcode = b"", None
            return out
        return False
