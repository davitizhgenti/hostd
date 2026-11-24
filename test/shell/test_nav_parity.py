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
from hostd_shell.core import nav
from hostd_shell.core.driver import Driver
from hostd_shell.shell import Shell


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
    d = Driver(n, fixtures.FakeAPI(), look=background.load(os.path.join(tempfile.mkdtemp(), "ui.toml")), tokens=harness.TOKENS)
    d.run([nav.Refresh(c.key) for c in n.cats])
    return n, d


NAMES = ("category", "cursors", "overlay", "filter", "paths", "pages", "rows", "sent", "message")


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
            list(s.api.sent), s._message)


def seen_new(n, d):
    ov = n.overlay
    if isinstance(ov, nav.Question):
        ov = ("question", ov.text, ov.yes)
    elif isinstance(ov, nav.Level):
        ov = ("level", ov.row.key())
    elif isinstance(ov, nav.Options):
        ov = ("options", ov.row.key(), ov.option)
    return (n.category, dict(n.currents), ov, n.filter, {k: tuple(v) for k, v in n.paths.items()},
            {k: v["app"] for k, v in n.svc_page.items()}, {k: [r.key() for r in v] for k, v in n.rows.items()},
            list(d.api.sent), d.messages[-1][0] if d.messages else "")


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
            elif r < 0.94:
                yield ("category", rnd.randrange(0, 6))
            elif r < 0.97:
                yield ("option", rnd.randrange(0, 4))
            else:
                yield ("dismiss",)

    def apply(self, s, n, d, op):
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
            same(self, seen_old(s), seen_new(n, d), "at the start")
            history = []
            for op in self.ops(random.Random(seed), steps):
                history.append(op)
                self.apply(s, n, d, op)
                same(self, seen_old(s), seen_new(n, d), f"seed {seed} after {history[-6:]}")

    def test_without_tabs(self):
        self.run_sequences(None, 120, 40)

    def test_with_tabs_and_tiles(self):
        self.run_sequences(fixtures.MENU, 120, 40)


if __name__ == "__main__":
    unittest.main()
