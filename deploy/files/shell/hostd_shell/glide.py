"""The mouse in the menu, without a pointer: movement glides the selection.

There is nothing to point at, so the mouse is felt rather than seen, like a
trackpad on a TV: a short push up or down moves one item, a firm push
sideways changes category. Small or slow movement (a hand resting on the
mouse, a bumped table) does nothing, a diagonal push moves along one axis
only, and a half-made step is forgotten after a pause, so the selection
never creeps. Plain logic with no Qt: Shell feeds it positions and acts on
the verbs it returns.

A hidden pointer still stops at the screen's edges, where moving further
gives no more positions: recentre() says when to put it back in the middle
(the shell asks the compositor), and the jump that causes is not a move.
"""

import math

UP, DOWN, LEFT, RIGHT = "up", "down", "left", "right"


class Glide:
    def __init__(self, step=40, cross=100, rest_ms=250, floor=60, burst=3):
        self.step, self.cross = step, cross
        self.rest_ms, self.floor, self.burst = rest_ms, floor, burst
        self.last = None  # (x, y, t)
        self.ax = self.ay = 0.0  # travel not yet made into steps
        self.axis = None  # "x" or "y": the axis of the movement under way
        self.expect = None  # where a recentre puts the pointer

    @classmethod
    def from_tokens(cls, tokens, scale=1.0):
        i = tokens.get("input", {})
        return cls(step=i.get("glide-step", 40) * scale, cross=i.get("glide-cross", 100) * scale,
                   rest_ms=i.get("glide-rest", 250), floor=i.get("glide-floor", 60) * scale,
                   burst=i.get("glide-burst", 3))

    def reset(self):
        self.ax = self.ay = 0.0
        self.axis = None

    def motion(self, x, y, t_ms):
        """The pointer is at (x, y) at t_ms; returns the verbs that makes."""
        if self.expect is not None and math.hypot(x - self.expect[0], y - self.expect[1]) <= 2:
            self.expect = None
            self.last = (x, y, t_ms)  # the recentre's jump: no movement
            return []
        if self.last is None:
            self.last = (x, y, t_ms)
            return []
        lx, ly, lt = self.last
        self.last = (x, y, t_ms)
        dt = t_ms - lt
        if dt > self.rest_ms:
            self.reset()  # a pause: start afresh
        dx, dy = x - lx, y - ly
        dist = math.hypot(dx, dy)
        if dist == 0:
            return []
        if dt > 0 and dist / dt * 1000 < self.floor:
            return []  # too slow to mean anything
        self.ax += dx
        self.ay += dy
        if self.axis is None:
            # The axis is the one the movement mostly follows, decided once
            # it has gone far enough to tell.
            if max(abs(self.ax) / self.cross, abs(self.ay) / self.step) < 0.25:
                return []
            self.axis = "y" if abs(self.ay) * self.cross >= abs(self.ax) * self.step else "x"
        verbs = []
        if self.axis == "y":
            self.ax = 0.0
            while abs(self.ay) >= self.step and len(verbs) < self.burst:
                verbs.append(DOWN if self.ay > 0 else UP)
                self.ay -= math.copysign(self.step, self.ay)
        else:
            self.ay = 0.0
            while abs(self.ax) >= self.cross and len(verbs) < self.burst:
                verbs.append(RIGHT if self.ax > 0 else LEFT)
                self.ax -= math.copysign(self.cross, self.ax)
        if len(verbs) == self.burst:  # a fling: what is left over is dropped
            self.ax = self.ay = 0.0
        return verbs

    def recentre(self, x, y, width, height, margin):
        """Where to put the pointer back, or None: near an edge it would
        soon stop giving movement. The next position there is not a move."""
        if min(x, y, width - x, height - y) > margin:
            return None
        self.expect = (width / 2, height / 2)
        return self.expect
