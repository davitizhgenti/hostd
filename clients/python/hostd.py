"""A client for hostd's API, in one file with only Python's standard library.

    from hostd import Hostd

    core = Hostd("192.168.18.33", token="hostd_...", ca_fingerprint="1f2e...")
    # or HOSTD_URL, HOSTD_TOKEN, HOSTD_CA_FINGERPRINT (hostctl tls on the machine shows it)
    core.notice("Tests are running")
    result = core.run(["pytest", "-q", "/home/screen/proj"], background=True, wait=True)
    print(result.exit_code, "\\n".join(result.lines))
    core.start("show", file="/home/screen/plot.png")    # an app that takes arguments
    core.run(["imv", "-f", "/home/screen/plot.png"])     # any command, full screen

The machine serves its API over HTTPS with a certificate authority of its
own: ca_fingerprint pins it (the client trusts that CA and nothing else,
whatever name or address it uses), or ca_file names its ca.pem.

A token needs the scopes of what it does: apps to start and stop apps,
run to run commands and define apps, read to read (see docs/api.md).
Every call raises HostdError when hostd refuses it, with its code
("forbidden", "not_found", "invalid_args"...).
"""

import hashlib
import json
import os
import ssl
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
    def __init__(self, url=None, token=None, ca_fingerprint=None, ca_file=None, timeout=60):
        url = url or os.environ.get("HOSTD_URL", "")
        if not url:
            raise ValueError("no hostd address: Hostd(url=...) or HOSTD_URL")
        if "://" not in url:
            url = "https://" + url
        scheme, rest = url.split("://", 1)
        host = rest.split("/", 1)[0]
        if ":" not in host.rsplit("]", 1)[-1]:
            rest = host + ":7300" + rest[len(host):]
        self.url = (scheme + "://" + rest).rstrip("/")
        self.token = token or os.environ.get("HOSTD_TOKEN", "")
        self.timeout = timeout
        fp = ca_fingerprint or os.environ.get("HOSTD_CA_FINGERPRINT", "")
        self.ca_fingerprint = fp.replace(":", "").replace(" ", "").lower()
        self.ca_file = ca_file
        self._context = None

    def _ssl(self):
        """The SSL context: the pinned CA only, or the system's CAs."""
        if self._context is not None or not self.url.startswith("https://"):
            return self._context
        if not self.ca_fingerprint and not self.ca_file:
            self._context = ssl.create_default_context()
            return self._context
        if self.ca_file:
            with open(self.ca_file) as f:
                ca_pem = f.read()
        else:
            # The CA is public: fetch it over an unchecked connection, then
            # accept it only if it is the one pinned.
            loose = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
            loose.check_hostname, loose.verify_mode = False, ssl.CERT_NONE
            with urllib.request.urlopen(self.url + "/v1/tls/ca.pem", timeout=self.timeout, context=loose) as res:
                ca_pem = res.read().decode()
            got = hashlib.sha256(ssl.PEM_cert_to_DER_cert(ca_pem)).hexdigest()
            if got != self.ca_fingerprint:
                raise HostdError("ca_mismatch", f"the machine's CA is {got}, not the one pinned: another machine, "
                                 "or someone in between")
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        ctx.minimum_version = ssl.TLSVersion.TLSv1_2
        ctx.load_verify_locations(cadata=ca_pem)
        ctx.check_hostname = False  # the CA vouches for this machine only, by any name or address
        ctx.verify_mode = ssl.CERT_REQUIRED
        self._context = ctx
        return ctx

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
            with urllib.request.urlopen(req, timeout=self.timeout, context=self._ssl()) as res:
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
