"""hostd's API without ever waiting for it: every request and the event
stream are Qt sockets in the UI's own event loop, so a slow or hung hostd
can make an answer late or fail, and never makes the screen stop.

Qt's HTTP client cannot talk to a unix socket, so this is QLocalSocket and
the wire codec (core/wire.py), which has no Qt in it and is tested alone.
Callbacks and signals run in the UI thread.
"""

import json
import os
from dataclasses import dataclass

from PySide6.QtCore import QObject, QTimer, Signal
from PySide6.QtNetwork import QLocalSocket

from hostd_shell.core import sources, wire

REQUEST_MS = 10000  # how long an answer may take


def default_socket():
    runtime = os.environ.get("XDG_RUNTIME_DIR", f"/run/user/{os.getuid()}")
    return os.environ.get("HOSTD_SOCKET", os.path.join(runtime, "hostd.sock"))


def local_token():
    """The token of what is chosen on the screen: it counts as someone at it."""
    if os.environ.get("HOSTD_TOKEN"):
        return os.environ["HOSTD_TOKEN"]
    config = os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config"))
    try:
        with open(os.path.join(config, "hostd", "local.token")) as f:
            return f.read().strip()
    except OSError:
        return ""


@dataclass
class Result:
    """What came of a request. ok: hostd answered with success."""
    ok: bool
    status: int = 0
    payload: object = None
    message: str = ""  # why not, for the screen
    code: str = ""  # hostd's error code (forbidden, ...); "" when hostd was not reached
    reached: bool = True  # hostd answered at all


class _Flight(QObject):
    """A request's socket and clock, used for one request after another: a
    finished one waits in the Daemon's pool, so nothing is ever deleted while
    Qt may still be using it (PySide crashes on that). A QObject with slots
    of its own, not closures."""

    def __init__(self, daemon):
        super().__init__(daemon)
        self.daemon = daemon
        self.gen = 0
        self.finished = True
        self.done = None
        self.sock = QLocalSocket(self)
        self.timer = QTimer(self, singleShot=True)
        self.sock.connected.connect(self._connected)
        self.sock.readyRead.connect(self._ready)
        self.sock.disconnected.connect(self._closed)
        self.sock.errorOccurred.connect(self._error)
        self.timer.timeout.connect(self._timeout)

    def begin(self, method, path, body, done, timeout_ms):
        """Starts a request; returns its handle."""
        self.gen += 1
        self.finished, self.sent, self.done = False, False, done
        self.data = wire.request(method, path, self.daemon.token, body)
        self.response = wire.Response()
        self.timer.start(timeout_ms)
        self.sock.connectToServer(self.daemon.socket_path)
        return Handle(self, self.gen)

    def _connected(self):
        if not self.finished:
            self.sock.write(self.data)
            self.sent = True

    def _ready(self):
        if self.finished:
            return
        try:
            self.response.feed(bytes(self.sock.readAll().data()))
        except wire.ProtocolError as e:
            return self._fail(f"hostd said something unexpected: {e}", reached=True)
        if self.response.done:
            self._answer()

    def _closed(self):
        if self.finished:
            return
        try:
            self.response.eof()
        except wire.ProtocolError as e:
            return self._fail(str(e), reached=self.sent)
        self._answer()

    def _error(self, _error):
        if self.finished:
            return
        if not self.sent:
            self._fail(f"cannot reach hostd: {self.sock.errorString()}")
        elif not self.response.done:
            self._fail(self.sock.errorString(), reached=True)

    def _timeout(self):
        self._fail("hostd did not answer", reached=self.sent)

    def _answer(self):
        r = self.response
        try:
            payload = r.json() if r.status < 400 else None
        except ValueError:
            return self._fail(f"hostd's answer is not JSON (HTTP {r.status})", reached=True)
        if r.status >= 400:
            message, code = r.error()
            return self._finish(Result(False, r.status, None, message, code))
        self._finish(Result(True, r.status, payload))

    def _fail(self, message, reached=False):
        self._finish(Result(False, message=message, reached=reached))

    def _finish(self, result):
        done = self.done
        self.release()
        if done:
            done(result)

    def release(self):
        """The request is over (answered, failed or cancelled): the socket is
        closed and this waits in the pool for the next one."""
        if self.finished:
            return
        self.finished, self.done = True, None
        self.timer.stop()
        self.sock.abort()
        self.daemon._pending.discard(self)
        self.daemon._idle.append(self)


class Handle:
    """A request in flight, for Daemon.cancel."""

    def __init__(self, flight, gen):
        self.flight, self.gen = flight, gen


class Daemon(QObject):
    def __init__(self, socket_path=None, token=None, parent=None):
        super().__init__(parent)
        self.socket_path = socket_path or default_socket()
        self.token = local_token() if token is None else token
        self._pending, self._idle = set(), []

    def request(self, method, path, body=None, done=None, timeout_ms=REQUEST_MS):
        """Sends a request; done(Result) is called when it is answered or has
        failed. Returns a handle for cancel()."""
        flight = self._idle.pop() if self._idle else _Flight(self)
        self._pending.add(flight)
        return flight.begin(method, path, body, done, timeout_ms)

    def cancel(self, handle):
        """Drops a request: its callback is not called. Does nothing if it
        has finished already."""
        if handle.flight.gen == handle.gen:
            handle.flight.release()

    def get(self, path, done=None, timeout_ms=REQUEST_MS):
        return self.request("GET", path, None, done, timeout_ms)

    def ask(self, path, callback, timeout_ms=REQUEST_MS):
        """For the loader: callback(sources.Answer) when path is answered. An
        error answer of hostd's has its status; hostd not reached or not
        answering has none (status 0), and fails the page that needs it."""
        def done(r):
            if r.ok:
                callback(sources.Answer(True, r.payload))
            else:
                callback(sources.Answer(False, status=r.status if r.reached else 0, message=r.message))
        return self.get(path, done, timeout_ms)

    def pending(self):
        return len(self._pending)

    def close(self):
        """Drops every request on its way (no callbacks)."""
        for flight in list(self._pending):
            flight.release()


class Events(QObject):
    """Follows hostd's events (GET /v1/events, a WebSocket). event(dict) for
    each; opened/closed when the stream comes and goes. A stream that drops
    is opened again after a growing pause (first_ms, doubling to max_ms).
    One socket, used again for each connection."""

    event = Signal(dict)
    opened = Signal()
    closed = Signal()

    def __init__(self, daemon, types="*", first_ms=1000, max_ms=30000, parent=None):
        super().__init__(parent or daemon)
        self.daemon, self.types = daemon, types
        self.first_ms, self.max_ms = first_ms, max_ms
        self.connected = False
        self._pause = first_ms
        self._active = False  # a connection is being made or is open
        self._stopped = True
        self._key = ""
        self._response = self._frames = None
        self._sock = QLocalSocket(self)
        self._sock.connected.connect(self._on_connected)
        self._sock.readyRead.connect(self._on_ready)
        self._sock.disconnected.connect(self._lost)
        self._sock.errorOccurred.connect(self._on_error)
        self._retry = QTimer(self, singleShot=True, timeout=self._open)

    def start(self):
        self._stopped = False
        self._open()

    def stop(self):
        self._stopped = True
        self._retry.stop()
        self._drop()

    def _open(self):
        if self._stopped or self._active:
            return
        self._active = True
        self._key = wire.new_key()
        self._response, self._frames = wire.Response(), None
        self._sock.connectToServer(self.daemon.socket_path)

    def _on_connected(self):
        if self._active:
            self._sock.write(wire.upgrade(f"/v1/events?type={self.types}", self.daemon.token, self._key))

    def _on_error(self, _error):
        self._lost()

    def _on_ready(self):
        if not self._active:
            return
        data = bytes(self._sock.readAll().data())
        try:
            if self._frames is None:
                self._response.feed(data)
                if not self._response.done:
                    return
                wire.upgraded(self._response, self._key)
                self._frames = wire.Frames()
                self._pause = self.first_ms
                self.connected = True
                self.opened.emit()
                self.event.emit({"type": "hostd.connected"})
                data = self._response.rest
                if not data:
                    return
            for op, payload in self._frames.feed(data):
                if op == wire.TEXT:
                    try:
                        ev = json.loads(payload)
                    except ValueError:
                        continue
                    if isinstance(ev, dict):
                        self.event.emit(ev)
                elif op == wire.PING:
                    self._sock.write(wire.frame(wire.PONG, payload[:125]))
                elif op == wire.CLOSE:
                    return self._lost()
        except wire.ProtocolError:
            self._lost()

    def _lost(self):
        """The connection ended or never was: try again, later."""
        if not self._active:
            return
        self._drop()
        if not self._stopped:
            self._retry.start(self._pause)
            self._pause = min(self._pause * 2, self.max_ms)

    def _drop(self):
        was_active, self._active = self._active, False
        if self.connected:
            self.connected = False
            self.closed.emit()
        if was_active:
            self._sock.abort()
