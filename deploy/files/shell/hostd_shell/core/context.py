"""Whether the menu is in front and listening: a window that is shown, and
that has the focus. Controller and pointer events count only then (hostd
holds the pads while the menu is in front; at other times apps own them).

A state machine, kept plain on purpose: Qt's QStateMachine carries out its
transitions through the event queue, so what is read right after an intent
is the state before it, and input must see its own effects at once."""


class Window:
    def __init__(self):
        self.visible = False
        self.active = False

    @property
    def listening(self):
        return self.visible and self.active

    def show(self):
        """The menu opened. Whether that changed anything."""
        was, self.visible = self.visible, True
        return not was

    def hide(self):
        was, self.visible = self.visible, False
        return was

    def set_active(self, active):
        """The window gained or lost the focus."""
        changed, self.active = self.active != bool(active), bool(active)
        return changed
