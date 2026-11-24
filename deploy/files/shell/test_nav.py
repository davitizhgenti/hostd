"""The navigator (hostd_shell.core.nav) on its own: no Qt, no screen.
python3 -m unittest discover -s deploy/files/shell -p "test_*.py" (make lint-scripts)."""

import json
import os
import random
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "test", "shell"))

import fixtures  # noqa: E402
from hostd_shell import background, menu, views  # noqa: E402
from hostd_shell import input as inp  # noqa: E402
from hostd_shell.core import nav  # noqa: E402
from hostd_shell.core.driver import Driver  # noqa: E402

with open(os.path.join(HERE, "tokens.json")) as _f:
    TOKENS = json.load(_f)


def make(menu_text=None, api=None):
    """A navigator and its driver on the fixture data, every category listed."""
    tabs = []
    if menu_text is not None:
        import tempfile
        path = os.path.join(tempfile.mkdtemp(), "menu.toml")
        with open(path, "w") as f:
            f.write(menu_text)
        tabs, _ = menu.load(path, lambda name: True)
    n = nav.Navigator(views.categories(tabs), tabs)
    d = Driver(n, api or fixtures.FakeAPI(), look=background.load("/nonexistent/ui.toml"), tokens=TOKENS)
    d.run([nav.Refresh(c.key) for c in n.cats])
    return n, d


def go(n, d, *verbs):
    for v in verbs:
        d.run(n.handle(v))


def to_row(n, d, title):
    for _ in range(60):
        if n.current_row() is not None and n.current_row().title == title:
            return
        go(n, d, inp.DOWN)
    raise AssertionError(f"no row {title!r}")


class Moving(unittest.TestCase):
    def test_the_cursor_starts_on_the_first_item_and_skips_headers(self):
        n, d = make()
        go(n, d, inp.RIGHT)  # Users
        self.assertEqual(n.category, "users")
        for key, rows in n.rows.items():
            i = n.currents[key]
            if rows:
                self.assertNotEqual(rows[i].kind, views.HEADER, key)

    def test_left_and_right_wrap_around_the_categories(self):
        n, d = make()
        go(n, d, inp.LEFT)
        self.assertEqual(n.category, n.cats[-1].key)
        go(n, d, inp.RIGHT)
        self.assertEqual(n.category, "apps")

    def test_home_goes_to_the_first_category_from_a_folder_of_a_tab(self):
        n, d = make(fixtures.MENU)
        go(n, d, inp.RIGHT)  # the Games tab
        self.assertEqual(n.category, "tab:Games")
        sent_before = len(d.api.sent)
        n.paths["tab:Games"] = ("Games", "Steam")  # a folder is open (set as a click on one would)
        effects = n.go_home()
        self.assertIn(nav.Refresh("tab:Games", nav.FIRST), effects)
        d.run(effects)
        self.assertEqual((n.category, n.paths["tab:Games"]), ("apps", ("Games",)))
        self.assertEqual(len(d.api.sent), sent_before)


class Overlays(unittest.TestCase):
    def test_a_question_is_answered_with_the_direction_and_sent_only_on_yes(self):
        n, d = make()
        go(n, d, inp.RIGHT)
        row = next((r for r in n.page_rows() if r.confirm), None)
        self.assertIsNotNone(row, "the Users page has a row that asks")
        to_row(n, d, row.title)
        sent = len(d.api.sent)
        go(n, d, inp.ACTIVATE)
        self.assertIsInstance(n.overlay, nav.Question)
        self.assertFalse(n.overlay.yes)
        go(n, d, inp.ACTIVATE)  # "no" is the default: nothing is sent
        self.assertIsNone(n.overlay)
        self.assertEqual(len(d.api.sent), sent)
        go(n, d, inp.ACTIVATE, inp.RIGHT, inp.ACTIVATE)
        self.assertEqual(len(d.api.sent), sent + 1)

    def test_back_closes_an_overlay_before_anything_else(self):
        n, d = make()
        go(n, d, inp.RIGHT)
        row = next(r for r in n.page_rows() if r.confirm)
        to_row(n, d, row.title)
        go(n, d, inp.ACTIVATE, inp.BACK)
        self.assertIsNone(n.overlay)
        self.assertEqual(n.category, "users")

    def test_a_click_on_a_header_does_nothing(self):
        n, d = make()
        go(n, d, inp.RIGHT)  # Users: its first row is a header
        self.assertEqual(n.rows["users"][0].kind, views.HEADER)
        before = n.currents["users"]
        self.assertEqual(n.click_item(0), [])
        self.assertEqual(n.currents["users"], before)


class Leaving(unittest.TestCase):
    def test_back_walks_out_and_ends_by_asking_the_window_manager(self):
        n, d = make(fixtures.MENU)
        go(n, d, inp.RIGHT, inp.RIGHT)  # the Services tab
        go(n, d, inp.BACK)
        self.assertEqual(n.category, "apps")
        go(n, d, inp.BACK)
        self.assertEqual(d.api.sent[-1][:2], ("POST", "/v1/windows/back"))

    def test_back_empties_the_filter_first(self):
        n, d = make()
        n.set_filter("jelly")
        self.assertTrue(all("jelly" in r.title.lower() or r.matches("jelly") for r in n.page_rows()))
        go(n, d, inp.BACK)
        self.assertEqual(n.filter, "")


class Invariants(unittest.TestCase):
    """Whatever is pressed, the navigator stays in a state it can show."""

    def check(self, n, why):
        self.assertIn(n.category, n.rows, why)
        for key, rows in n.rows.items():
            i = n.currents[key]
            if not rows:
                self.assertEqual(i, -1, f"{key}: no rows, cursor {i}; {why}")
                continue
            self.assertTrue(0 <= i < len(rows), f"{key}: cursor {i} of {len(rows)}; {why}")
            self.assertNotEqual(rows[i].kind, views.HEADER, f"{key}: the cursor is on a header; {why}")
        if isinstance(n.overlay, nav.Options):
            self.assertTrue(0 <= n.overlay.option < len(n.overlay.row.sheet), why)
        if n.filter:
            self.assertTrue(n.apps_tab() or n.rows[n.category] is not None, why)

    def test_random_intents_never_strand_the_cursor(self):
        for seed in range(60):
            n, d = make(fixtures.MENU if seed % 2 else None)
            rnd, history = random.Random(seed), []
            for _ in range(80):
                r = rnd.random()
                if r < 0.8:
                    op = rnd.choice(inp.VERBS)
                    d.run(n.handle(op))
                elif r < 0.9:
                    op = ("filter", rnd.choice(["", "s", "ste", "zzzz"]))
                    n.set_filter(op[1])
                else:
                    op = ("click", rnd.randrange(-2, 16))
                    d.run(n.click_item(op[1], rnd.random() < 0.3))
                history.append(op)
                self.check(n, f"seed {seed} after {history[-5:]}")

    def test_it_moves_with_hostd_not_answering(self):
        """Rows are set once; after that nothing needs hostd for the cursor
        to move, change category, filter and open overlays."""
        n, _ = make(fixtures.MENU)
        n.set_rows("apps", n.all_rows["apps"])  # the data the screen has
        for verb in (inp.DOWN, inp.DOWN, inp.RIGHT, inp.LEFT, inp.PAGE_DOWN, inp.ACTIONS, inp.BACK):
            effects = n.handle(verb)  # not run: no driver, no answers
            self.assertTrue(all(isinstance(e, (nav.Refresh, nav.Send, nav.ClearMessage, nav.SaveBackground)) for e in effects))
        self.check(n, "after moving without answers")


class SetRows(unittest.TestCase):
    def test_the_cursor_stays_on_its_row_when_rows_arrive_again(self):
        n, d = make()
        go(n, d, inp.DOWN, inp.DOWN)
        key = n.current_row().key()
        d.run([nav.Refresh("apps")])
        self.assertEqual(n.current_row().key(), key)

    def test_a_row_that_went_puts_the_cursor_on_the_first(self):
        n, d = make()
        go(n, d, inp.DOWN)
        last = [r for r in n.all_rows["apps"] if not r.under][-1:]
        n.set_rows("apps", last)
        self.assertEqual(n.current_row().key(), last[0].key())
        self.assertEqual(n.currents["apps"], 0)

    def test_set_rows_for_a_category_that_is_gone_is_ignored(self):
        n, _ = make(fixtures.MENU)
        n.set_categories(views.categories(()), [])
        n.set_rows("tab:Games", [])  # no error


if __name__ == "__main__":
    unittest.main()
