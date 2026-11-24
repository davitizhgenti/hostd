"""The pointers: the mouse's and each Wii Remote's, where they are, which
row each is over, and the strip of devices that tells them apart. No Qt:
every change returns what changed, so the screen is told only that.
Time comes in as an argument."""

from dataclasses import dataclass

MOUSE = "mouse"  # the mouse's pointer; each Wii Remote's is its device's ID
FRESH = {"x": 0.5, "y": 0.5, "visible": False, "down": False, "source": "", "name": "", "colour": "", "hover": -1}


@dataclass
class Changed:
    """What of the pointers' state changed, to tell the screen."""
    pointer: bool = False  # any pointer moved, showed, went, or changed its look
    ids: bool = False  # a device's pointer came to exist
    hovers: bool = False  # the rows with a pointer over them, or their colours
    strip: bool = False  # the devices strip
    devices: bool = False  # the list of input devices
    pressed: str = ""  # a Wii Remote's button went down: click where it points

    def __bool__(self):
        return any((self.pointer, self.ids, self.hovers, self.strip, self.devices, self.pressed))

    def merge(self, other):
        for f in ("pointer", "ids", "hovers", "strip", "devices"):
            setattr(self, f, getattr(self, f) or getattr(other, f))
        self.pressed = self.pressed or other.pressed
        return self


class Pointers:
    def __init__(self):
        self.devices = []  # the input devices (hostd's GET /v1/devices)
        self.pointers = {}  # by device: id, x, y (shares of the screen), visible, down, source, name, colour, hover
        self.last = MOUSE  # the pointer that moved last
        self._told_hovers, self._told_strip = [], []  # what the screen was last told
        self._mouse_last = None  # where the mouse was last seen
        self._mouse_next = None  # the latest move waiting for its turn
        self._mouse_at = 0.0  # when a move was last applied

    # --- what the screen draws ---

    def shown(self, pointer):
        """A pointer as the screen draws it: in its device's colour only when
        more than one device points (alone, it is plain)."""
        pointing = sum(1 for d in self.devices if d.get("pointer"))
        return dict(pointer) if pointing > 1 else {**pointer, "colour": ""}

    def last_shown(self):
        p = self.pointers.get(self.last)
        return self.shown(p) if p else {"id": MOUSE, **FRESH}

    def all_shown(self):
        return [self.shown(p) for p in self.pointers.values()]

    def hovers(self):
        """The rows with a pointer over them: {row, colour} for each."""
        return [{"row": p["hover"], "colour": self.shown(p)["colour"]} for p in self.pointers.values()
                if p["visible"] and p["hover"] >= 0]

    def strip(self):
        """The devices the strip at the top shows: those that point or play,
        with their colour and whether in use now. Only with two or more."""
        devs = [d for d in self.devices if d.get("kind") in ("pointer", "gamepad")]
        if len(devs) < 2:
            return []
        return [{"id": d.get("id", ""), "label": d.get("label") or d.get("name", ""), "colour": d.get("colour", ""),
                 "active": bool(self.pointers.get(MOUSE if d.get("kind") == "pointer" else d.get("id"), {}).get("visible"))}
                for d in devs]

    def mouse_hover(self):
        return self.pointers.get(MOUSE, {}).get("hover", -1)

    def is_visible(self, device):
        return bool(self.pointers.get(device, {}).get("visible"))

    # --- changes ---

    def identity(self, device, name, colour):
        """A pointer's name and colour: the device's, when hostd listed it."""
        for d in self.devices:
            if d.get("id") == device or (device == MOUSE and d.get("kind") == "pointer"):
                return name or d.get("label", ""), colour or d.get("colour", "")
        return name or ("Mouse" if device == MOUSE else "Wii Remote"), colour or "blue"

    def set(self, device, **kv):
        old = self.pointers.get(device)
        new = dict(old) if old else {"id": device, **FRESH}
        for k, v in kv.items():
            if v is not None:
                new[k] = v
        if not new["name"] or not new["colour"]:
            new["name"], new["colour"] = self.identity(device, new["name"], new["colour"])
        if new == old:
            return Changed()
        self.pointers[device] = new
        if new["visible"] and (old is None or not old["visible"] or kv.get("x") is not None):
            self.last = device
        return Changed(pointer=True, ids=old is None).merge(self._tell())

    def _tell(self):
        """The hover underlines and the strip change rarely, though pointers
        move all the time: they say so only when they did."""
        out = Changed()
        hovers = self.hovers()
        if hovers != self._told_hovers:
            self._told_hovers, out.hovers = hovers, True
        strip = self.strip()
        if strip != self._told_strip:
            self._told_strip, out.strip = strip, True
        return out

    def hover(self, device, row):
        return self.set(device, hover=row)

    def mouse_down(self, down):
        return self.set(MOUSE, down=down)

    def hide_mouse(self):
        """The mouse's pointer goes (a key or a pad is used, or it was still)."""
        if MOUSE in self.pointers:
            return self.set(MOUSE, visible=False, down=False)
        return Changed()

    def reset_mouse(self):
        """The menu opens: the first sample is where the cursor already was."""
        self._mouse_last = None

    def mouse_moved(self, x, y, width, height, now, every):
        """The mouse moved to x, y in a window of width x height, at time now
        (seconds); moves are applied at most every `every` seconds. Returns
        (Changed, wait): what changed now, and when (seconds) to call
        apply_mouse for a move that must wait, else None."""
        if width <= 0 or height <= 0:
            return Changed(), None
        last, self._mouse_last = self._mouse_last, (x, y)
        # The first sample after the menu opens is where the cursor already
        # was, and a hover with no movement is not the mouse moving.
        if last is None or abs(x - last[0]) + abs(y - last[1]) < 2:
            return Changed(), None
        self._mouse_next = (x / width, y / height)
        wait = every - (now - self._mouse_at)
        if wait <= 0:
            return self.apply_mouse(now), None
        return Changed(), max(0.001, wait)

    def apply_mouse(self, now):
        """The latest mouse position, once for all the moves since the last."""
        if self._mouse_next is None:
            return Changed()
        (x, y), self._mouse_next = self._mouse_next, None
        self._mouse_at = now
        return self.set(MOUSE, x=x, y=y, visible=True, source="mouse")

    def remote_at(self, x, y, visible, down, source="wii", device="wii", name="", colour=""):
        """A Wii Remote's pointer: x, y as shares of the screen; not visible
        when it points away. Each remote has its own. Changed.pressed is the
        device whose button just went down while it points."""
        if not isinstance(x, (int, float)) or not isinstance(y, (int, float)):
            return Changed()
        was = self.pointers.get(device, {}).get("down", False)
        out = self.set(device, x=min(1.0, max(0.0, float(x))), y=min(1.0, max(0.0, float(y))), visible=visible,
                       down=down, source=source or "wii", name=name or None, colour=colour or None)
        if visible and down and not was:
            out.pressed = device
        return out

    def set_devices(self, devices):
        """The input devices hostd listed. A renamed or recoloured device's
        pointer follows."""
        devices = [d for d in devices or [] if isinstance(d, dict)]
        if devices == self.devices:
            return Changed()
        self.devices = devices
        for k, p in list(self.pointers.items()):
            name, colour = self.identity(k, "", "")
            self.pointers[k] = {**p, "name": name, "colour": colour}
        return Changed(devices=True, pointer=True).merge(self._tell())


def calibration(data):
    """A remote's calibration event: (what is going on, text to say or "",
    whether the text is an error). What is going on is {} when it ended."""
    state = data.get("state")
    name = data.get("name") if isinstance(data.get("name"), str) else "The remote"
    if state == "aim":
        step = data.get("step") if isinstance(data.get("step"), int) else 0
        return ({"device": str(data.get("device", "")), "name": name, "colour": str(data.get("colour", "")),
                 "step": step, "corner": str(data.get("corner", "")), "reason": str(data.get("reason", ""))}, "", False)
    text = {"done": f"{name} is calibrated", "reset": f"{name}'s calibration is cleared",
            "failed": str(data.get("reason") or "The calibration failed")}.get(state, "")
    return {}, text, state == "failed"
