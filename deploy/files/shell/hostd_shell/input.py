"""One set of verbs for every input: keys, the mouse, and controllers.

The shell acts on verbs only, so a keyboard, a mouse and a controller do
the same thing everywhere. Controllers reach it as hostd's display.nav
events (hostd holds them while the menu is in front, so apps do not act on
them too); their "what" is already a verb.
"""

UP, DOWN, LEFT, RIGHT = "up", "down", "left", "right"
PAGE_UP, PAGE_DOWN = "page_up", "page_down"
ACTIVATE, BACK, ACTIONS, CLOSE = "activate", "back", "actions", "close"
NEXT_TAB, PREV_TAB = "next_tab", "prev_tab"
HOME = "home"  # back to the first category, first item

VERBS = (UP, DOWN, LEFT, RIGHT, PAGE_UP, PAGE_DOWN, ACTIVATE, BACK, ACTIONS, CLOSE, NEXT_TAB, PREV_TAB, HOME)

# Qt's key names (Qt.Key's members).
_KEYS = {
    "Key_Up": UP, "Key_Down": DOWN, "Key_Left": LEFT, "Key_Right": RIGHT,
    "Key_PageUp": PAGE_UP, "Key_PageDown": PAGE_DOWN,
    "Key_Return": ACTIVATE, "Key_Enter": ACTIVATE, "Key_Space": ACTIVATE, "Key_Select": ACTIVATE,
    "Key_Escape": BACK, "Key_Backspace": BACK, "Key_Back": BACK,
    "Key_Menu": ACTIONS,
    "Key_Delete": CLOSE,
    "Key_Tab": NEXT_TAB, "Key_Backtab": PREV_TAB,
}

# hostd's display.nav verbs (internal/evdev/nav.go), from every controller:
# its own codes are canonical buttons by then, so one table serves them all.
_NAV = {"up": UP, "down": DOWN, "left": LEFT, "right": RIGHT,
        "choose": ACTIVATE, "back": BACK, "actions": ACTIONS, "close": CLOSE,
        "prev_tab": PREV_TAB, "next_tab": NEXT_TAB, "page_up": PAGE_UP, "page_down": PAGE_DOWN, "home": HOME}


def verb_for_key(name, shift=False, typing=False):
    """The verb for a key (Qt's name, "Key_Up"), or None for a key the
    shell does not take (a letter: typed into the filter).

    typing: the filter has text, so Space and Backspace edit it."""
    if name == "Key_F10" and shift:
        return ACTIONS
    if name == "Key_Tab" and shift:
        return PREV_TAB
    if typing and name in ("Key_Space", "Key_Backspace"):
        return None
    return _KEYS.get(name)


def verb_for_nav(what):
    """The verb for a controller's display.nav event, or None."""
    return _NAV.get(what)


# What each verb is called on the screen, per input: the hints show the
# buttons of whatever was used last. A controller's are the ones printed on
# it: its family's labels (display.nav says which family it is).
KEYS = {ACTIVATE: "Enter", BACK: "Esc", ACTIONS: "Menu", CLOSE: "Del", NEXT_TAB: "◀ ▶", UP: "▲ ▼"}

# Positions of the face buttons: south (bottom), east (right), west (left),
# north (top). Labels are what is printed there.
FAMILIES = {
    "xbox": {"south": "A", "east": "B", "west": "X", "north": "Y", "start": "Menu"},
    "playstation": {"south": "✕", "east": "○", "west": "□", "north": "△", "start": "Options"},
    "nintendo": {"south": "B", "east": "A", "west": "Y", "north": "X", "start": "+"},
    "wii": {"south": "B", "east": "A", "west": "1", "north": "2", "start": "+"},
}
FAMILIES["generic"] = FAMILIES["xbox"]
# The face buttons that choose and go back: where the confirming A is.
CONFIRM = {"xbox": ("south", "east"), "playstation": ("south", "east"), "nintendo": ("east", "south"),
           "wii": ("east", "south"), "generic": ("south", "east")}


def controller_names(family):
    """The buttons of a controller of this family, by verb."""
    family = family if family in FAMILIES else "generic"
    labels = FAMILIES[family]
    confirm, back = CONFIRM[family]
    return {ACTIVATE: labels[confirm], BACK: labels[back], ACTIONS: labels["start"], CLOSE: labels["north"],
            NEXT_TAB: "◀ ▶", UP: "▲ ▼"}


def hint_pairs(pairs, device="keys", family=""):
    """[(button, what)] for [(verb, what)]; verbs the device has no button
    for are left out."""
    names = controller_names(family) if device == "controller" else KEYS
    return [(names[v], what) for v, what in pairs if v in names]


def hint_text(pairs, device="keys", family=""):
    """'Enter open   Esc back' for [(ACTIVATE, "open"), (BACK, "back")]."""
    return "   ".join(f"{b} {what}" for b, what in hint_pairs(pairs, device, family))
