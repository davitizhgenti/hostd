"""One set of verbs for every input: keys, the mouse, and controllers.

A screen acts on verbs only, so a keyboard, a mouse and a controller do the
same thing everywhere. Controllers reach the UI as hostd's display.nav
events (hostd holds them while the menu is in front, so apps do not act on
them too); their "what" is already a verb.
"""

UP, DOWN, LEFT, RIGHT = "up", "down", "left", "right"
PAGE_UP, PAGE_DOWN = "page_up", "page_down"
ACTIVATE, BACK, ACTIONS, CLOSE = "activate", "back", "actions", "close"
NEXT_TAB, PREV_TAB = "next_tab", "prev_tab"

VERBS = (UP, DOWN, LEFT, RIGHT, PAGE_UP, PAGE_DOWN, ACTIVATE, BACK, ACTIONS, CLOSE, NEXT_TAB, PREV_TAB)

_KEYS = {
    "Up": UP, "Down": DOWN, "Left": LEFT, "Right": RIGHT,
    "Page_Up": PAGE_UP, "Page_Down": PAGE_DOWN,
    "Return": ACTIVATE, "KP_Enter": ACTIVATE, "space": ACTIVATE,
    "Escape": BACK, "BackSpace": BACK,
    "Menu": ACTIONS,
    "Delete": CLOSE,
    "Tab": NEXT_TAB, "ISO_Left_Tab": PREV_TAB,
}

_NAV = {"up": UP, "down": DOWN, "left": LEFT, "right": RIGHT,
        "choose": ACTIVATE, "back": BACK, "actions": ACTIONS}


def verb_for_key(name, shift=False, typing=False):
    """The verb for a key (Gdk's key name), or None for a key the screen
    does not take (a letter: typed into the filter).

    typing: a filter has text, so Space and BackSpace edit it."""
    if name == "F10" and shift:
        return ACTIONS
    if name == "Tab" and shift:
        return PREV_TAB
    if typing and name in ("space", "BackSpace"):
        return None
    return _KEYS.get(name)


def verb_for_nav(what):
    """The verb for a controller's display.nav event, or None."""
    return _NAV.get(what)


# What each verb is called on the screen, per input: the hint bar shows the
# buttons of whatever was used last.
HINTS = {
    "keys": {ACTIVATE: "Enter", BACK: "Esc", ACTIONS: "Menu", CLOSE: "Del", NEXT_TAB: "◀ ▶", UP: "▲ ▼"},
    "controller": {ACTIVATE: "A", BACK: "B", ACTIONS: "X", NEXT_TAB: "◀ ▶", UP: "▲ ▼"},
}


def hint_text(pairs, device="keys"):
    """'Enter open   Esc back' for [(ACTIVATE, "open"), (BACK, "back")];
    verbs the device has no button for are left out."""
    names = HINTS.get(device, HINTS["keys"])
    return "   ".join(f"{names[v]} {what}" for v, what in pairs if v in names)
