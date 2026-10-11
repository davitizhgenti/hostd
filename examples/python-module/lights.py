#!/usr/bin/env python3
"""A hostd module in Python, standard library only: pretend lights.

hostd starts it with HOSTD_MODULE_SOCKET set and talks JSON-RPC 2.0 over
that socket, one message per line (sdk/PROTOCOL.md). To hostd it is a
module like the built-in ones: its actions go through the same pipeline
(locks, priority holds, permissions, audit), so

    hostctl lights set porch --brightness 40
    curl --unix-socket $XDG_RUNTIME_DIR/hostd.sock -H "Authorization: Bearer $T" \\
         -d '{"brightness": 40}' http://x/v1/lights/porch

both work, and a rule can apply them.

Configure it in ~/.config/hostd/hostd.toml:

    [external.lights]
    command = ["python3", "/path/to/lights.py"]
"""

import json
import os
import socket
import sys
import threading

NAME = os.environ.get("HOSTD_MODULE_NAME", "lights")

MANIFEST = {
    "name": NAME,
    "version": "0.1.0",
    "owns": ["lights.*"],
    "scopes": [{"name": "lights", "description": "Turn the lights up and down"}],
    "actions": [
        {
            "type": "lights.set",
            "description": "Set a light's brightness (0-100)",
            "schema": {
                "type": "object",
                "properties": {
                    "id": {"type": "string", "description": "the light"},
                    "brightness": {"type": "integer", "minimum": 0, "maximum": 100},
                },
                "required": ["id", "brightness"],
                "additionalProperties": False,
            },
            "keys": ["light:{id}"],
            "scope": "lights",
            "timeout": "5s",
            "route": {"method": "POST", "path": "/v1/lights/{id}"},
        },
        {
            "type": "lights.dim_all",
            "description": "Dim every light to 10 (one lights.set per light, as child actions)",
            "scope": "lights",
            "timeout": "10s",
            "route": {"method": "POST", "path": "/v1/lights/dim"},
        },
    ],
    "events": [{"type": "lights.changed", "description": "A light's brightness changed"}],
    "reads": [{"name": "lights", "description": "Every light and its brightness", "path": "/v1/lights"}],
}


class HostdError(Exception):
    """An error with one of hostd's codes (not_found, invalid_args, ...)."""

    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


class Connection:
    """JSON-RPC 2.0 both ways over the module socket, one message per line."""

    def __init__(self, path, handler):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.connect(path)
        self.reader = self.sock.makefile("r", encoding="utf-8")
        self.wlock = threading.Lock()
        self.lock = threading.Lock()
        self.pending = {}
        self.next_id = 0
        self.handler = handler

    def send(self, msg):
        msg["jsonrpc"] = "2.0"
        data = (json.dumps(msg) + "\n").encode()
        with self.wlock:
            self.sock.sendall(data)

    def call(self, method, params, timeout=30):
        """Calls hostd (emit, do, subscribe, read) and returns the result."""
        with self.lock:
            self.next_id += 1
            mid = "m%d" % self.next_id
            box = {"event": threading.Event()}
            self.pending[mid] = box
        self.send({"id": mid, "method": method, "params": params})
        if not box["event"].wait(timeout):
            raise HostdError("timeout", "hostd did not answer %s" % method)
        with self.lock:
            del self.pending[mid]
        msg = box["msg"]
        if "error" in msg:
            err = msg["error"]
            raise HostdError((err.get("data") or {}).get("code", "internal"), err.get("message", ""))
        return msg.get("result")

    def serve(self):
        for line in self.reader:
            msg = json.loads(line)
            if "method" not in msg:  # an answer to one of our calls
                with self.lock:
                    box = self.pending.get(msg.get("id"))
                if box:
                    box["msg"] = msg
                    box["event"].set()
                continue
            # hostd's requests may overlap: answer each in its own thread.
            threading.Thread(target=self.answer, args=(msg,), daemon=True).start()

    def answer(self, msg):
        try:
            result = self.handler(msg["method"], msg.get("params") or {})
            reply = {"result": result if result is not None else {}}
        except HostdError as e:
            reply = {"error": {"code": -32000, "message": str(e), "data": {"code": e.code}}}
        except Exception as e:  # noqa: BLE001 - every failure becomes an answer
            reply = {"error": {"code": -32000, "message": "%s: %s" % (type(e).__name__, e), "data": {"code": "internal"}}}
        if "id" in msg:
            reply["id"] = msg["id"]
            self.send(reply)


class Lights:
    def __init__(self):
        self.lights = {"porch": 0, "living-room": 0}
        self.started_apps = 0
        self.lock = threading.Lock()
        self.conn = None

    def handle_request(self, method, params):
        if method == "hostd.handshake":
            if params.get("protocol") != 1:
                raise HostdError("invalid_args", "this module speaks protocol 1")
            return {"protocol": 1}
        if method == "manifest":
            return MANIFEST
        if method == "start":
            # Count the apps hostd starts: events arrive as notifications.
            self.conn.call("subscribe", {"filter": "instance.started"})
            return {}
        if method == "event":
            with self.lock:
                self.started_apps += 1
            return None
        if method == "validate":
            action = params["action"]
            if action["type"] == "lights.set" and action["args"]["id"] not in self.lights:
                raise HostdError("not_found", "no light %r" % action["args"]["id"])
            return {}
        if method == "handle":
            return self.handle(params["action"])
        if method == "read":
            return self.read(params["name"])
        if method == "state":
            with self.lock:
                return {"lights": dict(self.lights), "apps_started": self.started_apps}
        if method == "stop":
            return {}
        raise HostdError("not_found", "no method %r" % method)

    def handle(self, action):
        if action["type"] == "lights.set":
            light, level = action["args"]["id"], action["args"]["brightness"]
            with self.lock:
                self.lights[light] = level
            # Tied to the action: part of its chain, in the audit trail.
            self.conn.call("emit", {"event": {"type": "lights.changed", "action": action["id"],
                                              "data": {"id": light, "brightness": level}}})
            return {"status": "applied", "data": {"id": light, "brightness": level}}
        if action["type"] == "lights.dim_all":
            # Child actions: "within" makes each one part of this action.
            for light in sorted(self.lights):
                self.conn.call("do", {"action": {"type": "lights.set", "args": {"id": light, "brightness": 10}},
                                      "within": action["id"]})
            return {"status": "applied", "data": {"dimmed": sorted(self.lights)}}
        raise HostdError("not_found", "no action %r" % action["type"])

    def read(self, name):
        if name != "lights":
            raise HostdError("not_found", "no read %r" % name)
        with self.lock:
            return [{"id": k, "brightness": v} for k, v in sorted(self.lights.items())]


def main():
    path = os.environ.get("HOSTD_MODULE_SOCKET")
    if not path:
        sys.exit("run by hostd: HOSTD_MODULE_SOCKET is not set")
    lights = Lights()
    lights.conn = Connection(path, lights.handle_request)
    print("lights module connected", flush=True)
    lights.conn.serve()  # until hostd closes the socket


if __name__ == "__main__":
    main()
