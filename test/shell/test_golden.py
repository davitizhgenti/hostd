"""Golden images: each state of each surface, rendered offscreen with the
fixture data, compared with test/shell/golden/<size>/<state>.png.

Every run writes what it rendered to test/shell/out/<size>/ with an
index.html contact sheet (and a .diff.png beside any image that differs),
for review on this machine: open test/shell/out/index.html. To accept the
new look: HOSTD_GOLDEN=update (make shell-golden), then review the change
to the golden images in git."""

import copy
import html
import os
import unittest

import numpy as np
from PySide6.QtGui import QImage

import fixtures
from harness import HERE, Screen

GOLDEN = os.path.join(HERE, "golden")
OUT = os.path.join(HERE, "out")
UPDATE = os.environ.get("HOSTD_GOLDEN") == "update"
SIZES = {"1920x1080": (1920, 1080), "1280x800": (1280, 800)}
CHANNEL, SHARE = 8, 0.001  # a channel may differ by 8; at most 0.1 % of pixels by more

KEYS_DOL = [("Key_D", "d"), ("Key_O", "o"), ("Key_L", "l")]
# (state, surface, what to do first, what it shows)
STATES = [
    ("01-apps", "Menu", [], "Apps: running apps (marked), folders, then the catalog; the current item at the list's top"),
    ("02-apps-down", "Menu", ["down"] * 4, "Four down: passed items scrolled away under the row"),
    ("02b-folders", "Menu", ["down", "down"], "A folder in the list, among the apps"),
    ("02c-folder-open", "Menu", ["down", "down", "activate", "activate"], "Inside Games / Dolphin: a subfolder and an app"),
    ("03-options", "Menu", ["actions"], "An item's options, at the right edge"),
    ("04-filter", "Menu", KEYS_DOL, "Typing filters Apps"),
    ("05-users", "Menu", ["right"], "Users"),
    ("06-question", "Menu", ["right", "down", "activate"], "Switching asks first; No is chosen"),
    ("07-settings", "Menu", ["right", "right"], "Settings"),
    ("08-volume", "Menu", ["right", "right", "activate"], "The volume's level"),
    ("09-background", "Menu", ["right", "right"] + ["down"] * 5 + ["activate"], "Background choices"),
    ("10-status", "Menu", ["right"] * 3, "Status"),
    ("11-notices", "NoticeLayer", "notices", "Notices, critical first"),
    ("12-osd", "OsdLayer", "osd", "The OSD"),
    ("13-gallery", "Gallery", [], "The component sheet"),
    ("03b-pointer", "Menu", [("pointer", 0.42, 0.575, 1)], "The pointer over a row (underlined); the selection stays"),
    ("03c-devices", "Menu+devices", [("wii", "057e:0306-hidraw3", 0.35, 0.656), ("wii", "057e:0306-hidraw4", 0.6, 0.55),
                                      ("pointer", 0.42, 0.575, 1)],
     "Three devices: the strip at the top left, each pointer and underline in its device's color"),
    ("10b-status-devices", "Menu+devices", ["right"] * 3 + ["down", "down"], "Status lists the devices, each with its color"),
    ("10c-device-colours", "Menu+devices", ["right"] * 3 + ["down", "down", "actions"], "A device's options: its colour"),
    ("03d-calibrate", "Menu+devices", [("wii", "057e:0306-hidraw4", 0.3, 0.4),
                                       ("calibrate", "057e:0306-hidraw4", "Mia", "violet", 1, "top right")],
     "Calibrating a Wii Remote: the corner to point at, in the remote's colour"),
    ("17-service-page", "Menu+page", ["right", "right", "activate"],
     "A service that says what it is doing: its page, in the menu's own list"),
    ("14-tabs", "Menu+tabs", ["right"], "The Games tab (tiles): a launcher heads its group"),
    ("14b-tiles-game", "Menu+tabs", ["right", "down"], "A game: its tile, and on the right its banner and details"),
    ("14c-tiles-other", "Menu+tabs", ["right"] + ["down"] * 4, "A game with no banner, in the Steam group"),
    ("15-services", "Menu+tabs", ["right", "right"], "The Services tab: each service's state"),
    ("16-service-options", "Menu+tabs", ["right", "right", "activate"], "A service's options: Stop, Restart"),
]


def render(size, surface, steps):
    surface, _, extra = surface.partition("+")
    reads = None
    if extra == "page":
        reads = copy.deepcopy(fixtures.READS)
        for a in reads["/v1/apps"]:
            if a["spec"]["id"] == "homeassistant":
                a["spec"]["status"] = {"url": "http://127.0.0.1:8481/status"}
        reads["/v1/catalog/homeassistant/status"] = fixtures.STATUS
    if extra == "devices":
        reads = copy.deepcopy(fixtures.READS)
        reads["/v1/devices"] = fixtures.DEVICES
    with Screen(size, surface, reads=reads, tabs=extra in ("tabs", "page")) as s:
        if extra == "devices":
            s.shell.menu.loader.refresh_watched({"/v1/devices"})
            s.shell.menu.refresh("status")
        if steps == "notices":
            s.shell.notify("Steam", "Download finished", "Dota 2 is ready to play", 0, 1, -1)
            s.shell.notify("hostd", "Video memory nearly full", "3.9 GB of 4 GB in use", 0, 2, -1)
        elif steps == "osd":
            s.shell.osd.show("Volume", "70%", 70)
        else:
            for step in steps:
                if isinstance(step, tuple) and step[0] == "pointer":
                    _, px, py, row = step  # the pointer at px, py (shares of the screen), over row `row`
                    w, h = s.size
                    s.mute_hover()
                    s.shell.pointers.mouseAt(w * px - 30, h * py, w, h)
                    s.shell.pointers.mouseAt(w * px, h * py, w, h)
                    s.shell.pointers.hover(row)
                elif isinstance(step, tuple) and step[0] == "calibrate":
                    _, device, name, colour, n, corner = step
                    s.shell.on_event({"type": "display.calibrate", "data": {
                        "device": device, "name": name, "colour": colour, "state": "aim", "step": n, "corner": corner}})
                    s.settle()
                elif isinstance(step, tuple) and step[0] == "wii":
                    _, device, px, py = step  # a Wii Remote's pointer, over what is there
                    s.shell.menu.setMenuActive(True)
                    s.shell.on_event({"type": "display.pointer", "data": {
                        "x": px, "y": py, "visible": True, "down": False, "family": "wii", "device": device}})
                    s.settle()
                elif isinstance(step, tuple):
                    s.key(*step)
                else:
                    s.verb(step)
        s.settle()
        warnings = list(s.warnings)
        return s.grab().convertToFormat(QImage.Format_ARGB32), warnings


def pixels(img):
    ptr = img.constBits()
    return np.frombuffer(ptr, np.uint8, img.sizeInBytes()).reshape(img.height(), img.bytesPerLine())[:, :img.width() * 4] \
        .reshape(img.height(), img.width(), 4).copy()


def compare(got, want):
    """(share of pixels off by more than CHANNEL, a diff image) or (1, None)
    when the sizes differ."""
    if got.size() != want.size():
        return 1.0, None
    a, b = pixels(got).astype(int), pixels(want.convertToFormat(QImage.Format_ARGB32)).astype(int)
    bad = (np.abs(a - b) > CHANNEL).any(axis=2)
    diff = np.zeros_like(pixels(got))
    diff[..., 3] = 255
    diff[..., 2] = np.where(bad, 255, (pixels(got)[..., 2] // 4))  # red where it differs, dim elsewhere
    out = QImage(diff.tobytes(), got.width(), got.height(), got.width() * 4, QImage.Format_ARGB32).copy()
    return float(bad.mean()), out


def contact_sheet(rows):
    parts = ['<!doctype html><meta charset="utf-8"><title>hostd shell</title><style>'
             'body{background:#0b0b0c;color:#f2f2f4;font:16px/1.5 Inter,sans-serif;margin:24px}'
             'h2{font-weight:300;margin:32px 0 8px}figure{margin:0 0 24px}img{max-width:100%;border:1px solid #2a2a2e}'
             '.bad{color:#e07e7e}.muted{color:#8b8b93}</style><h1>hostd shell: rendered states</h1>'
             '<p class="muted">Offscreen, software renderer, fixed time and fixture data. '
             'Red notes mark states that differ from the golden images (the .diff.png shows where).</p>']
    for size in SIZES:
        parts.append(f"<h2>{size}</h2>")
        for state, note, status in [r for r in rows if r[0] == size for r in [r[1:]]]:
            cls = "bad" if status else "muted"
            diff = f' <a href="{size}/{state}.diff.png">diff</a>' if status else ""
            parts.append(f'<figure><p><b>{html.escape(state)}</b> <span class="muted">{html.escape(note)}</span>'
                         f' <span class="{cls}">{html.escape(status)}</span>{diff}</p>'
                         f'<img loading="lazy" src="{size}/{state}.png"></figure>')
    with open(os.path.join(OUT, "index.html"), "w") as f:
        f.write("\n".join(parts))


class Golden(unittest.TestCase):
    def test_states(self):
        rows, failures = [], []
        for size_name, size in SIZES.items():
            os.makedirs(os.path.join(OUT, size_name), exist_ok=True)
            os.makedirs(os.path.join(GOLDEN, size_name), exist_ok=True)
            for state, surface, steps, note in STATES:
                img, warnings = render(size, surface, steps)
                img.save(os.path.join(OUT, size_name, state + ".png"))
                golden = os.path.join(GOLDEN, size_name, state + ".png")
                status = ""
                if warnings:
                    status = f"QML warnings: {warnings}"
                elif UPDATE:
                    img.save(golden)
                elif not os.path.exists(golden):
                    status = "no golden image (make shell-golden)"
                else:
                    share, diff = compare(img, QImage(golden))
                    if share > SHARE:
                        status = f"differs: {share:.2%} of pixels"
                        if diff is not None:
                            diff.save(os.path.join(OUT, size_name, state + ".diff.png"))
                rows.append((size_name, state, note, status))
                if status:
                    failures.append(f"{size_name}/{state}: {status}")
        contact_sheet(rows)
        self.assertEqual(failures, [], "see test/shell/out/index.html")


if __name__ == "__main__":
    unittest.main()
