"""The new navigator (hostd_shell.core.nav) does what the Shell does.

Random sequences of intents drive both on the same fake hostd, and after
every step everything an observer could see is compared: the category, each
category's cursor and rows, the overlay, the filter, the folders and service
pages, what was sent to hostd, and the message. While the Shell is being
replaced this is the proof it is a replacement.
"""

import os
import random
import tempfile
import unittest

from PySide6.QtCore import QTimer

import fixtures
import harness  # the Qt application the Shell's timers need
from hostd_shell import background, menu, views
from hostd_shell import input as inp
from hostd_shell.core import hints, nav, pointers
from hostd_shell.core.driver import Driver
from hostd_shell.legacy import Shell


def old_shell(menu_text):
    menu_path = "/nonexistent/menu.toml"
    if menu_text is not None:
        menu_path = os.path.join(tempfile.mkdtemp(), "menu.toml")
        with open(menu_path, "w") as f:
            f.write(menu_text)
    s = Shell(fixtures.FakeAPI(), harness.TOKENS, now=lambda: fixtures.NOW,
              look_path=os.path.join(tempfile.mkdtemp(), "ui.toml"), menu_path=menu_path)
    for c in s.cats:
        s.refresh(c.key)
    return s, menu_path


def new_nav(menu_path):
    tabs, _ = menu.load(menu_path, lambda name: True)
    n = nav.Navigator(views.categories(tabs), tabs)
    look = background.load(os.path.join(tempfile.mkdtemp(), "ui.toml"))
    d = Driver(n, fixtures.FakeAPI(), look=look, tokens=harness.TOKENS)
    d.run([nav.Refresh(c.key) for c in n.cats])
    return n, d


NAMES = ("category", "cursors", "overlay", "filter", "paths", "pages", "rows", "sent", "message",
         "hints", "focus", "section", "offset")


def same(test, old, new, why):
    for name, a, b in zip(NAMES, old, new, strict=True):
        test.assertEqual(a, b, f"{name}: {why}")


def seen_old(s):
    ov = None
    if s._question:
        ov = ("question", s._question, s._yes)
    elif s._panel == "level":
        ov = ("level", s._panel_row.key())
    elif s._panel == "options":
        ov = ("options", s._panel_row.key(), s._option)
    return (s._category, dict(s.currents), ov, s._filter, {k: tuple(v) for k, v in s.paths.items()},
            {k: v["app"] for k, v in s.svc_page.items()}, {k: [r.key() for r in m.rows] for k, m in s.pages.items()},
            list(s.api.sent), s._message, s._hints(), s.focus_info(), s.section, s.offset())


def seen_new(n, d, mode):
    ov = n.overlay
    if isinstance(ov, nav.Question):
        ov = ("question", ov.text, ov.yes)
    elif isinstance(ov, nav.Level):
        ov = ("level", ov.row.key())
    elif isinstance(ov, nav.Options):
        ov = ("options", ov.row.key(), ov.option)
    return (n.category, dict(n.currents), ov, n.filter, {k: tuple(v) for k, v in n.paths.items()},
            {k: v["app"] for k, v in n.svc_page.items()}, {k: [r.key() for r in v] for k, v in n.rows.items()},
            list(d.api.sent), d.messages[-1][0] if d.messages else "", hints.hints(n, mode), hints.focus_info(n.current_row()),
            n.section(), n.offset())


class Parity(unittest.TestCase):
    def setUp(self):
        # The Shell refreshes a moment after a send; here, at once.
        self._single = QTimer.singleShot
        QTimer.singleShot = staticmethod(lambda ms, fn: fn())

    def tearDown(self):
        QTimer.singleShot = self._single

    def ops(self, rnd, steps):
        words = ["", "ste", "doo", "z"]
        for _ in range(steps):
            r = rnd.random()
            if r < 0.70:
                yield ("verb", rnd.choice(inp.VERBS))
            elif r < 0.78:
                yield ("filter", rnd.choice(words))
            elif r < 0.88:
                yield ("click", rnd.randrange(-1, 14), rnd.random() < 0.3)
            elif r < 0.91:
                yield ("category", rnd.randrange(0, 6))
            elif r < 0.94:
                yield ("option", rnd.randrange(0, 4))
            elif r < 0.97:
                yield ("mode", rnd.choice(["keys", "controller"]), rnd.choice(["", "xbox", "playstation", "nintendo", "wii"]))
            else:
                yield ("dismiss",)

    def apply(self, s, n, d, mode, op):
        kind = op[0]
        if kind == "verb":
            s.handle(op[1])
            d.run(n.handle(op[1]))
        elif kind == "filter":
            if s.apps_tab() and not s._panel and not s._question:
                s.set_filter(op[1])
            if n.apps_tab() and n.overlay is None:
                n.set_filter(op[1])
        elif kind == "click":
            s.clickItem(op[1], "right" if op[2] else "left")
            d.run(n.click_item(op[1], op[2]))
        elif kind == "category":
            s.clickCategory(op[1])
            d.run(n.click_category(op[1]))
        elif kind == "mode":
            s._set_device(op[1])
            s._family = op[2]
            mode.use(op[1])
            mode.use_family(op[2])
        elif kind == "option":
            s.clickOption(op[1])
            d.run(n.click_option(op[1]))
        else:
            s.dismiss()
            n.dismiss()

    def run_sequences(self, menu_text, count, steps):
        for seed in range(count):
            s, path = old_shell(menu_text)
            n, d = new_nav(path)
            mode = hints.Mode()
            same(self, seen_old(s), seen_new(n, d, mode), "at the start")
            history = []
            for op in self.ops(random.Random(seed), steps):
                history.append(op)
                self.apply(s, n, d, mode, op)
                same(self, seen_old(s), seen_new(n, d, mode), f"seed {seed} after {history[-6:]}")

    def test_without_tabs(self):
        self.run_sequences(None, 120, 40)

    def test_with_tabs_and_tiles(self):
        self.run_sequences(fixtures.MENU, 120, 40)


DEVICES = fixtures.DEVICES
DEVICE_SETS = [[], DEVICES, DEVICES[:2], DEVICES[1:3], [{**DEVICES[1], "label": "Renamed", "colour": "teal"}, *DEVICES[2:]]]
SIGNALS = ("pointerChanged", "hoversChanged", "stripChanged", "pointerIdsChanged", "devicesChanged")
FLAGS = {"pointerChanged": "pointer", "hoversChanged": "hovers", "stripChanged": "strip", "pointerIdsChanged": "ids",
         "devicesChanged": "devices"}


class PointersParity(unittest.TestCase):
    """The Pointers do what the Shell's pointer code does, and say the same
    things changed (the signals the screen would have got)."""

    def one(self, seed):
        s, _ = old_shell(None)
        s.mouse_ms = 0
        p = pointers.Pointers()
        fired = []
        for sig in SIGNALS:
            getattr(s, sig).connect(lambda sig=sig: fired.append(sig))
        pressed = []
        s.pointerPressed.connect(pressed.append)
        rnd, history = random.Random(seed), []
        for _ in range(60):
            fired.clear()
            pressed.clear()
            r = rnd.random()
            if r < 0.30:
                op = ("move", rnd.random() * 1920, rnd.random() * 1080, rnd.choice([1920, 1920, 1920, 0]))
                s.mouseAt(op[1], op[2], op[3], 1080 if op[3] else 0)
                new = p.mouse_moved(op[1], op[2], op[3], 1080 if op[3] else 0, 0.0, 0.0)[0]
            elif r < 0.45:
                op = ("hover", rnd.randrange(-1, 6))
                s.hover(op[1])
                new = p.hover(pointers.MOUSE, op[1])
            elif r < 0.55:
                dev = rnd.choice(["wii", DEVICES[1]["id"], DEVICES[2]["id"]])
                op = ("rhover", dev, rnd.randrange(-1, 6))
                s.pointerHover(dev, op[2])
                new = p.hover(dev, op[2])
            elif r < 0.75:
                dev = rnd.choice(["wii", DEVICES[1]["id"], DEVICES[2]["id"]])
                op = ("remote", rnd.choice([0.2, 0.9, 1.7, -0.5, "x", None]), 0.4, rnd.random() < 0.7, rnd.random() < 0.5, dev,
                      rnd.choice(["", "Mia"]), rnd.choice(["", "pink"]))
                s.pointer_at(op[1], op[2], op[3], op[4], "wii", op[5], op[6], op[7])
                new = p.remote_at(op[1], op[2], op[3], op[4], "wii", op[5], op[6], op[7])
            elif r < 0.82:
                op = ("down", rnd.random() < 0.5)
                s.mouseDown(op[1])
                new = p.mouse_down(op[1])
            elif r < 0.88:
                op = ("hide",)
                s.hide_pointer()
                new = p.hide_mouse()
            elif r < 0.96:
                op = ("devices", rnd.randrange(len(DEVICE_SETS)))
                s.api.reads["/v1/devices"] = DEVICE_SETS[op[1]]
                s._load_devices()
                new = p.set_devices(DEVICE_SETS[op[1]])
            else:
                op = ("reset",)
                s._mouse_last = None
                p.reset_mouse()
                new = pointers.Changed()
            history.append(op)
            why = f"seed {seed} after {history[-5:]}"
            self.assertEqual(s._pointers, p.pointers, why)
            self.assertEqual(s.last_pointer(), p.last_shown(), why)
            self.assertEqual([s._shown(x) for x in s._pointers.values()], p.all_shown(), why)
            self.assertEqual((s.strip_list(), s.hovers_list(), s.devices), (p.strip(), p.hovers(), p.devices), why)
            self.assertEqual({FLAGS[x] for x in fired}, {f for f in FLAGS.values() if getattr(new, f)}, f"signals: {why}")
            self.assertEqual(pressed[0] if pressed else "", new.pressed, why)

    def test_random_pointers(self):
        for seed in range(150):
            self.one(seed)


if __name__ == "__main__":
    unittest.main()
