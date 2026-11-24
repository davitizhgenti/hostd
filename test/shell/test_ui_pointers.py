"""ui.Pointers (the typed QObject QML will bind to) does what the Shell's
pointer code does: the same values for QML, and the same signals."""

import random
import unittest

from test_nav_parity import DEVICE_SETS, old_shell

import fixtures
from hostd_shell.ui.pointers import Pointers

D = fixtures.DEVICES
PROPS = ("pointer", "pointers", "pointerIds", "hovers", "strip", "deviceList", "calibrating")
SIGNALS = {"pointerChanged": "pointerChanged", "hoversChanged": "hoversChanged", "stripChanged": "stripChanged",
           "pointerIdsChanged": "idsChanged", "devicesChanged": "devicesChanged", "calibrationChanged": "calibrationChanged"}
CALIBRATIONS = [{"state": "aim", "device": "w1", "name": "Mia", "colour": "pink", "step": 1, "corner": "top-left"},
                {"state": "aim", "step": "x"}, {"state": "done", "name": "Mia"}, {"state": "reset"},
                {"state": "failed", "reason": "no IR"}, {"state": "cancelled"}, {"state": "failed", "name": 3}]


class Parity(unittest.TestCase):
    def one(self, seed):
        s, _ = old_shell(None)
        s.mouse_ms = 0
        p = Pointers(mouse_ms=0, clock=lambda: 0.0)
        old_fired, new_fired, old_pressed, new_pressed, new_said = [], [], [], [], []
        for old_name, new_name in SIGNALS.items():
            getattr(s, old_name).connect(lambda n=old_name: old_fired.append(n))
            getattr(p, new_name).connect(lambda n=old_name: new_fired.append(n))
        s.pointerPressed.connect(old_pressed.append)
        p.pressed.connect(new_pressed.append)
        p.said.connect(lambda t, e: new_said.append((t, e)))
        rnd, history = random.Random(seed), []
        for _ in range(60):
            for lst in (old_fired, new_fired, old_pressed, new_pressed, new_said):
                lst.clear()
            s._message, s._error = "", False
            r = rnd.random()
            if r < 0.28:
                op = ("move", rnd.random() * 1920, rnd.random() * 1080, rnd.choice([1920, 1920, 1920, 0]))
                s.mouseAt(op[1], op[2], op[3], 1080 if op[3] else 0)
                p.mouseAt(op[1], op[2], op[3], 1080 if op[3] else 0)
            elif r < 0.42:
                op = ("hover", rnd.randrange(-1, 6))
                s.hover(op[1])
                p.hover(op[1])
            elif r < 0.52:
                dev = rnd.choice(["wii", D[1]["id"], D[2]["id"]])
                op = ("rhover", dev, rnd.randrange(-1, 6))
                s.pointerHover(dev, op[2])
                p.pointerHover(dev, op[2])
            elif r < 0.70:
                dev = rnd.choice(["wii", D[1]["id"], D[2]["id"]])
                op = ("remote", rnd.choice([0.2, 0.9, 1.7, -0.5, "x", None]), 0.4, rnd.random() < 0.7, rnd.random() < 0.5, dev,
                      rnd.choice(["", "Mia"]), rnd.choice(["", "pink"]))
                s.pointer_at(op[1], op[2], op[3], op[4], "wii", op[5], op[6], op[7])
                p.remote_at(op[1], op[2], op[3], op[4], "wii", op[5], op[6], op[7])
            elif r < 0.77:
                op = ("down", rnd.random() < 0.5)
                s.mouseDown(op[1])
                p.mouseDown(op[1])
            elif r < 0.83:
                op = ("hide",)
                s.hide_pointer()
                p.hide_mouse()
            elif r < 0.92:
                op = ("devices", rnd.randrange(len(DEVICE_SETS)))
                s.api.reads["/v1/devices"] = DEVICE_SETS[op[1]]
                s._load_devices()
                p.set_devices(DEVICE_SETS[op[1]])
            elif r < 0.97:
                op = ("calibrate", rnd.randrange(len(CALIBRATIONS)))
                s._calibration_event(CALIBRATIONS[op[1]])
                p.calibration_event(CALIBRATIONS[op[1]])
            else:
                op = ("reset",)
                s._mouse_last = None
                p.reset_mouse()
            history.append(op)
            why = f"seed {seed} after {history[-5:]}"
            for name in PROPS:
                self.assertEqual(getattr(s, name), getattr(p, name), f"{name}: {why}")
            self.assertEqual(sorted(set(old_fired)), sorted(set(new_fired)), f"signals: {why}")
            self.assertEqual(old_pressed, new_pressed, f"pressed: {why}")
            self.assertEqual((s._message, s._error) if s._message else None, new_said[-1] if new_said else None, f"said: {why}")

    def test_random_pointers(self):
        for seed in range(120):
            self.one(seed)


if __name__ == "__main__":
    unittest.main()
