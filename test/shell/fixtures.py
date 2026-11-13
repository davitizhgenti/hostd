"""Fixed data for the shell's layout and golden tests: what hostd would
report, through a fake API (no socket, no hostd)."""

import copy
import datetime
import os

HERE = os.path.dirname(os.path.abspath(__file__))
APP_ICON = os.path.join(HERE, "fixtures", "app-icon.svg")
NOW = datetime.datetime(2026, 2, 14, 13, 30)  # February (gold), early afternoon
LONG = "A game with a very long title that cannot fit in the column however wide the screen is"

INSTANCES = [
    {"id": "steam", "app": "steam", "name": "Steam", "surface": "window", "state": "running"},
    {"id": "chromium", "app": "chromium", "name": "Chromium", "surface": "window", "state": "starting"},
]
APPS = [
    {"id": "steam", "name": "Steam", "surface": "window", "icon": APP_ICON,
     "actions": [{"id": "bigpicture", "name": "Big Picture"}]},
    {"id": "steam-570", "name": "Dota 2", "surface": "window", "under": "steam"},
    {"id": "chromium", "name": "Chromium", "surface": "window"},
    {"id": "dolphin", "name": "Dolphin", "surface": "window", "folder": "Games/Dolphin"},
    {"id": "kodi", "name": "Kodi", "surface": "window", "actions": [{"id": "music", "name": "Music"}]},
    {"id": "jellyfin", "name": "Jellyfin", "surface": "window"},
    {"id": "retroarch", "name": "RetroArch", "surface": "window"},
    {"id": "long", "name": LONG, "surface": "window"},
    {"id": "firefox", "name": "Firefox", "surface": "window"},
    {"id": "vlc", "name": "VLC", "surface": "window"},
    {"id": "mkw", "name": "Mario Kart Wii", "surface": "window", "folder": "Games/Dolphin/Wii"},
    {"id": "smg", "name": "Super Mario Galaxy", "surface": "window", "folder": "Games/Dolphin/Wii"},
    {"id": "steam-launcher", "name": "Steam", "surface": "window", "folder": "Games/Steam"},
]
SEATS = [{"user": "serve", "vt": 7, "holds": True}, {"user": "guest", "vt": 8, "holds": False}]
AUDIO = {"available": True, "percent": 70, "muted": False, "outputs": [
    {"name": "hdmi", "description": "GP107GL HDMI", "default": True},
    {"name": "usb", "description": "USB speakers", "default": False}]}
DISPLAY = {"outputs": [{"name": "HDMI-A-1", "make": "Seiko Epson", "model": "EPSON PJ", "active": True,
                        "width": 1920, "height": 1080, "refresh": 60,
                        "modes": [{"width": 1920, "height": 1080, "refresh": 60},
                                  {"width": 1280, "height": 800, "refresh": 59.81}]}],
           "gpu_memory": {"total": 4 << 30, "used": 1 << 30}}
VERSION = {"version": "v0.2.0", "previous": "edge-7b37121"}
LOG = [
    {"type": "seat.switch", "summary": "serve", "status": "applied", "time": "2026-02-14T13:25:28Z",
     "source": {"name": "laptop"}},
    {"type": "app.open", "summary": "steam", "status": "applied", "time": "2026-02-14T13:20:01Z",
     "source": {"name": "menu"}},
    {"type": "display.mode", "summary": "HDMI-A-1", "status": "skipped", "time": "2026-02-14T13:10:44Z",
     "source": {"name": "phone"}},
]
READS = {
    "/v1/instances": INSTANCES,
    "/v1/apps": [{"spec": a} for a in APPS],
    "/v1/seat": SEATS,
    "/v1/audio": AUDIO,
    "/v1/display": DISPLAY,
    "/v1/version": VERSION,
    "/v1/actions?limit=12": LOG,
}


class FakeAPI:
    """hostd's API, answered from READS; what was sent is in .sent."""

    token = "tok"

    def __init__(self, reads=None):
        self.reads = copy.deepcopy(READS if reads is None else reads)
        self.sent = []

    def get(self, path):
        if path not in self.reads:
            from hostd_shell.api import APIError
            raise APIError("no such read", "not_found", 404)
        return copy.deepcopy(self.reads[path])

    def call(self, method, path, body=None):
        if method == "GET":
            return self.get(path)
        self.sent.append((method, path, body))
        if body and body.get("type") == "audio.volume.set":
            step = int(body["args"]["percent"])
            self.reads["/v1/audio"]["percent"] = max(0, min(150, self.reads["/v1/audio"]["percent"] + step))
        return {"status": "applied"}

    def do(self, action, args=None):
        return self.call("POST", "/v1/actions", {"type": action, "args": args or {}})
