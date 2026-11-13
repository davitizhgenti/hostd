"""The notices on screen, as data: no GTK, no D-Bus.

Follows the desktop notification spec's rules that matter on a TV: a notice
can replace an earlier one (replaces_id), closes itself after its timeout
(the app's, or ours), critical ones stay longer, and at most MAX_SHOWN are
on screen at once; the rest wait their turn.
"""

import time

MAX_SHOWN = 3
DEFAULT_TIMEOUT = 8.0  # seconds
CRITICAL_TIMEOUT = 30.0
LOW, NORMAL, CRITICAL = 0, 1, 2

# Why a notice closed (the spec's NotificationClosed reasons).
EXPIRED, DISMISSED, CLOSED_BY_CALL = 1, 2, 3


class Notice:
    def __init__(self, nid, app, summary, body, urgency, timeout):
        self.id, self.app, self.summary, self.body = nid, app, summary, body
        self.urgency, self.timeout = urgency, timeout
        self.shown_at = None  # when it reached the screen

    def __repr__(self):
        return f"Notice({self.id}, {self.summary!r})"


class Notices:
    """The notices waiting and shown. clock: seconds, monotonic."""

    def __init__(self, clock=time.monotonic):
        self.clock = clock
        self.all = []  # oldest first
        self.next_id = 1

    def notify(self, app, summary, body="", replaces_id=0, urgency=NORMAL, expire_ms=-1):
        """Adds a notice (or replaces one); returns its id."""
        if expire_ms and expire_ms > 0:
            timeout = expire_ms / 1000
        elif urgency == CRITICAL:
            timeout = CRITICAL_TIMEOUT
        else:
            timeout = DEFAULT_TIMEOUT
        old = self.find(replaces_id) if replaces_id else None
        if old is not None:
            old.app, old.summary, old.body, old.urgency, old.timeout = app, summary, body, urgency, timeout
            if old.shown_at is not None:
                old.shown_at = self.clock()  # its time starts again
            return old.id
        n = Notice(self.next_id, app, summary, body, urgency, timeout)
        self.next_id += 1
        self.all.append(n)
        self.fill()
        return n.id

    def find(self, nid):
        return next((n for n in self.all if n.id == nid), None)

    def close(self, nid):
        """Takes a notice away; True if there was one."""
        n = self.find(nid)
        if n is None:
            return False
        self.all.remove(n)
        self.fill()
        return True

    def shown(self):
        """The notices on screen: critical first, then oldest first."""
        on = [n for n in self.all if n.shown_at is not None]
        return sorted(on, key=lambda n: (n.urgency != CRITICAL, n.shown_at, n.id))

    def fill(self):
        """Puts waiting notices on screen while there is room; critical ones first."""
        now = self.clock()
        waiting = sorted((n for n in self.all if n.shown_at is None), key=lambda n: (n.urgency != CRITICAL, n.id))
        room = MAX_SHOWN - len(self.shown())
        for n in waiting[:max(0, room)]:
            n.shown_at = now

    def expire(self):
        """Closes notices whose time is up; returns their ids."""
        now = self.clock()
        gone = [n.id for n in self.all if n.shown_at is not None and now - n.shown_at >= n.timeout]
        for nid in gone:
            self.close(nid)
        return gone

    def next_expiry(self):
        """Seconds until the next notice on screen expires, or None."""
        on = self.shown()
        if not on:
            return None
        now = self.clock()
        return max(0.0, min(n.shown_at + n.timeout - now for n in on))


def urgency_of(hints):
    """The urgency hint (a byte in the spec; any int here)."""
    try:
        return int((hints or {}).get("urgency", NORMAL))
    except (TypeError, ValueError):
        return NORMAL


# What the OSD says for hostd's events; None for events it does not show.
def osd_for(event):
    kind, data = event.get("type", ""), event.get("data") or {}
    if kind == "audio.volume.changed":
        if data.get("muted"):
            return ("Volume", "Muted", 0)
        pct = int(data.get("percent") or 0)
        return ("Volume", f"{pct}%", pct)
    if kind == "seat.switched" and data.get("user"):
        return ("Screen", data["user"], None)
    return None
