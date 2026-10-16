"""A client for hostd's API, in one file with only Python's standard library.

    from hostd import Hostd

    core = Hostd("http://192.168.18.33:7300", token="hostd_...")   # or HOSTD_URL, HOSTD_TOKEN
    core.notice("Tests are running")
    result = core.run(["pytest", "-q", "/home/screen/proj"], background=True, wait=True)
    print(result.exit_code, "\\n".join(result.lines))
    core.start("show", file="/home/screen/plot.png")    # an app that takes arguments
    core.run(["imv", "-f", "/home/screen/plot.png"])     # any command, full screen

A token needs the scopes of what it does: apps to start and stop apps,
run to run commands and define apps, read to read (see docs/api.md).
Every call raises HostdError when hostd refuses it, with its code
("forbidden", "not_found", "invalid_args"...).
"""

import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field

__all__ = ["Hostd", "HostdError", "Finished"]


class HostdError(Exception):
    """hostd refused a call: code is its error code, message why."""

    def __init__(self, code, message, status=None):
        super().__init__(f"{code}: {message}")
        self.code, self.message, self.status = code, message, status


@dataclass
class Finished:
    """A command that ended: its instance, exit status and output."""

    instance: str
    exit_code: int
    state: str
    lines: list = field(default_factory=list)
    error: str = ""

    @property
    def ok(self):
        return self.exit_code == 0 and not self.error


class Hostd:
    def __init__(self, url=None, token=None, timeout=60):
        url = url or os.environ.get("HOSTD_URL", "")
        if not url:
            raise ValueError("no hostd address: Hostd(url=...) or HOSTD_URL")
        if "://" not in url:
            url = "http://" + url
        self.url = url.rstrip("/")
        self.token = token or os.environ.get("HOSTD_TOKEN", "")
        self.timeout = timeout

    # --- the API ---------------------------------------------------------------

    def request(self, method, path, body=None):
        """One call to the API; the answer's JSON."""
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.url + path, data=data, method=method)
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as res:
                raw = res.read()
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                err = json.loads(raw)["error"]
                raise HostdError(err.get("code", "error"), err.get("message", ""), e.code) from None
            except (ValueError, KeyError, TypeError):
                raise HostdError("http_%d" % e.code, raw.decode(errors="replace").strip(), e.code) from None
        return json.loads(raw) if raw else None

    def action(self, type, **args):
        """Any action, by type: what hostctl action does. Returns its data."""
        res = self.request("POST", "/v1/actions", {"type": type, "args": args})
        return res.get("data") if isinstance(res, dict) else res

    def get(self, path, **query):
        """Any read, by path (GET /v1/...)."""
        if query:
            path += "?" + urllib.parse.urlencode(query)
        return self.request("GET", path)

    # --- apps and commands -----------------------------------------------------

    def run(self, command, name=None, background=False, fullscreen=True, front=False, dir=None, env=None,
            wait=False, poll=0.5, wait_timeout=None):
        """Run a command on the machine (scope run): a window, full screen by
        default, or in the background. Returns the instance's ID, or with
        wait=True a Finished once it has ended."""
        args = {"command": list(command)}
        if name:
            args["name"] = name
        if background:
            args["background"] = True
        if not fullscreen:
            args["fullscreen"] = False
        if front:
            args["front"] = True
        if dir:
            args["dir"] = dir
        if env:
            args["env"] = dict(env)
        instance = self.action("app.run", **args)["instance"]
        return self.wait(instance, poll, wait_timeout) if wait else instance

    def wait(self, instance, poll=0.5, timeout=None):
        """Wait until an instance ends; its exit status and output."""
        path = "/v1/instances/" + urllib.parse.quote(instance, safe="")
        deadline = None if timeout is None else time.monotonic() + timeout
        while True:
            info = self.get(path)
            if info.get("ended"):
                break
            if deadline is not None and time.monotonic() > deadline:
                raise TimeoutError(f"{instance} still runs after {timeout}s")
            time.sleep(poll)
        logs = self.get(path + "/logs", plain="true", lines=5000)
        return Finished(instance=instance, exit_code=info.get("exit_code") or 0, state=info.get("state", ""),
                        lines=logs.get("lines", []), error=info.get("error", ""))

    def start(self, app, front=False, **args):
        """Start an app (scope apps); keyword arguments fill in its args
        (an app file's args = { file = "path" }). Returns the instance's ID."""
        body = {"id": app}
        if front:
            body["front"] = True
        if args:
            body["args"] = args
        return self.action("app.start", **body)["instance"]

    def stop(self, instance):
        """Stop an instance (scope apps)."""
        self.action("instance.stop", id=instance)

    def define(self, app, toml):
        """Add or replace an app file (scope run): TOML, as written by hand."""
        return self.action("app.define", id=app, file=toml)

    def undefine(self, app):
        """Remove an app file (scope run)."""
        return self.action("app.undefine", id=app)

    def apps(self, all=False):
        """The app catalog."""
        return self.get("/v1/apps", all="true")["apps"] if all else self.get("/v1/apps")["apps"]

    def instances(self, all=False):
        """What runs (all=True: and what ended lately)."""
        return self.get("/v1/instances", all="true") if all else self.get("/v1/instances")

    def logs(self, instance, lines=200, plain=True):
        """An instance's latest log lines."""
        path = "/v1/instances/" + urllib.parse.quote(instance, safe="") + "/logs"
        return self.get(path, lines=lines, plain="true" if plain else "false")["lines"]

    # --- the screen and sound --------------------------------------------------

    def notice(self, text):
        """A short notice on the screen, above any app (scope display)."""
        self.action("display.notice", text=text)

    def volume(self, percent):
        """Set the volume (0-150), or change it: "+5", "-5" (scope audio)."""
        return self.action("audio.volume.set", percent=percent)
