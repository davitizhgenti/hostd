"""Pointers, hints and the focus panel with no Qt (make lint-scripts)."""

import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "test", "shell"))

from test_nav import go, make, to_row  # noqa: E402

import fixtures  # noqa: E402
from hostd_shell import input as inp  # noqa: E402
from hostd_shell.core import hints, nav, pointers  # noqa: E402

D = fixtures.DEVICES


class Mouse(unittest.TestCase):
    def test_the_first_sample_after_opening_and_tiny_moves_are_not_the_mouse_moving(self):
        p = pointers.Pointers()
        self.assertFalse(p.mouse_moved(100, 100, 1920, 1080, 0.0, 0.0)[0])  # where it already was
        self.assertFalse(p.mouse_moved(101, 100, 1920, 1080, 0.1, 0.0)[0])  # a hover, no movement
        ch, wait = p.mouse_moved(400, 300, 1920, 1080, 0.2, 0.0)
        self.assertTrue(ch.pointer and ch.ids and wait is None)
        self.assertAlmostEqual(p.pointers["mouse"]["x"], 400 / 1920)
        self.assertAlmostEqual(p.pointers["mouse"]["y"], 300 / 1080)
        p.reset_mouse()
        self.assertFalse(p.mouse_moved(900, 900, 1920, 1080, 0.3, 0.0)[0])  # the menu opened again

    def test_a_gaming_mouse_is_applied_at_most_every_few_milliseconds(self):
        p, t = pointers.Pointers(), 100.0  # the clock is long past the first move
        p.mouse_moved(0, 0, 1920, 1080, t, 0.008)
        ch, wait = p.mouse_moved(50, 50, 1920, 1080, t + 0.001, 0.008)
        self.assertTrue(ch.pointer)
        self.assertIsNone(wait)  # applied at once: nothing was applied before it
        ch, wait = p.mouse_moved(60, 60, 1920, 1080, t + 0.002, 0.008)
        self.assertFalse(ch)
        self.assertAlmostEqual(wait, 0.007, places=3)  # waits for its turn
        p.mouse_moved(70, 70, 1920, 1080, t + 0.003, 0.008)  # a newer move replaces the waiting one
        ch = p.apply_mouse(t + 0.009)
        self.assertTrue(ch.pointer)
        self.assertAlmostEqual(p.pointers["mouse"]["x"], 70 / 1920)
        self.assertFalse(p.apply_mouse(t + 0.010))  # nothing waits

    def test_a_window_with_no_size_is_ignored(self):
        self.assertFalse(pointers.Pointers().mouse_moved(1, 1, 0, 1080, 0.0, 0.0)[0])


class Remotes(unittest.TestCase):
    def test_a_press_while_pointing_is_reported_once(self):
        p = pointers.Pointers()
        self.assertEqual(p.remote_at(0.5, 0.5, True, False, device="wii").pressed, "")
        self.assertEqual(p.remote_at(0.5, 0.5, True, True, device="wii").pressed, "wii")
        self.assertEqual(p.remote_at(0.5, 0.5, True, True, device="wii").pressed, "")  # held
        self.assertEqual(p.remote_at(0.5, 0.5, False, True, device="other").pressed, "")  # pointing away

    def test_numbers_are_kept_on_the_screen_and_junk_is_ignored(self):
        p = pointers.Pointers()
        p.remote_at(1.7, -3, True, False, device="wii")
        self.assertEqual((p.pointers["wii"]["x"], p.pointers["wii"]["y"]), (1.0, 0.0))
        self.assertFalse(p.remote_at("x", None, True, False, device="wii"))

    def test_colour_shows_only_when_more_than_one_device_points(self):
        p = pointers.Pointers()
        p.set_devices(D[:2])  # a mouse and one remote
        p.remote_at(0.5, 0.5, True, False, device=D[1]["id"])
        self.assertEqual(p.all_shown()[0]["colour"], D[1]["colour"])
        p.set_devices(D[1:2])  # only the remote points
        self.assertEqual(p.all_shown()[0]["colour"], "")

    def test_a_renamed_device_renames_its_pointer(self):
        p = pointers.Pointers()
        p.set_devices(D)
        p.remote_at(0.5, 0.5, True, False, device=D[2]["id"])
        self.assertEqual((p.pointers[D[2]["id"]]["name"], p.pointers[D[2]["id"]]["colour"]), ("Mia", "violet"))
        ch = p.set_devices([{**D[2], "label": "Zed", "colour": "teal"}, *D[:2]])
        self.assertTrue(ch.devices and ch.pointer)
        self.assertEqual(p.pointers[D[2]["id"]]["name"], "Zed")
        self.assertFalse(p.set_devices(p.devices))  # nothing changed

    def test_the_strip_needs_two_devices_and_marks_the_one_in_use(self):
        p = pointers.Pointers()
        p.set_devices(D[:1])
        self.assertEqual(p.strip(), [])
        p.set_devices(D[:3])
        p.remote_at(0.5, 0.5, True, False, device=D[1]["id"])
        active = {s["id"]: s["active"] for s in p.strip()}
        self.assertTrue(active[D[1]["id"]])
        self.assertFalse(active[D[0]["id"]])

    def test_the_screen_is_told_hovers_only_when_they_change(self):
        p = pointers.Pointers()
        p.set_devices(D[:3])
        p.remote_at(0.5, 0.5, True, False, device=D[1]["id"])
        self.assertTrue(p.hover(D[1]["id"], 3).hovers)
        self.assertFalse(p.hover(D[1]["id"], 3))
        self.assertTrue(p.set(D[1]["id"], x=0.6).pointer)  # it moved: the pointer changed, not the hovers
        self.assertFalse(p.set(D[1]["id"], x=0.6))


class Calibration(unittest.TestCase):
    def test_aiming_then_the_ending(self):
        going, text, err = pointers.calibration({"state": "aim", "device": "w1", "name": "Mia", "step": 2, "corner": "top-left"})
        self.assertEqual((going["step"], going["corner"], text, err), (2, "top-left", "", False))
        for state, want, error in (("done", "Mia is calibrated", False), ("reset", "Mia's calibration is cleared", False),
                                   ("failed", "The calibration failed", True), ("cancelled", "", False)):
            going, text, err = pointers.calibration({"state": state, "name": "Mia"})
            self.assertEqual((going, text, err), ({}, want, error), state)
        self.assertEqual(pointers.calibration({"state": "failed", "reason": "no IR"})[1], "no IR")
        self.assertEqual(pointers.calibration({"state": "done", "name": 7})[1], "The remote is calibrated")


class Hints(unittest.TestCase):
    def buttons(self, n, mode=None):
        return [h["what"] for h in hints.hints(n, mode or hints.Mode())]

    def test_hints_follow_the_overlay(self):
        n, d = make()
        go(n, d, inp.RIGHT)
        row = next(r for r in n.page_rows() if r.confirm)
        to_row(n, d, row.title)
        self.assertIn("choose", self.buttons(n))
        go(n, d, inp.ACTIVATE)
        self.assertEqual(self.buttons(n), ["answer", "choose", "cancel"])
        go(n, d, inp.BACK)
        n.overlay = nav.Options(next(r for r in n.all_rows["apps"] if r.sheet))
        self.assertEqual(self.buttons(n), ["move", "choose", "close"])

    def test_back_is_offered_only_where_there_is_a_back(self):
        n, _ = make()
        self.assertNotIn("back", self.buttons(n))
        n.set_filter("jelly")
        self.assertIn("back", self.buttons(n))

    def test_the_buttons_are_those_of_the_input_last_used(self):
        n, _ = make()
        mode = hints.Mode()
        keys = hints.hints(n, mode)
        self.assertTrue(mode.use("controller") and not mode.use("controller"))
        self.assertTrue(mode.use_family("xbox") and not mode.use_family("xbox"))
        self.assertNotEqual(hints.hints(n, mode), keys)


class FocusInfo(unittest.TestCase):
    def test_nothing_current_is_nothing_to_show(self):
        self.assertEqual(hints.focus_info(None), {})

    def test_the_current_row_as_the_tiles_panel_shows_it(self):
        n, _ = make()
        info = hints.focus_info(n.current_row())
        self.assertEqual(info["title"], n.current_row().title)
        self.assertEqual(set(info), {"title", "subtitle", "badge", "tone", "mark", "launcher", "banner", "details"})


if __name__ == "__main__":
    unittest.main()
