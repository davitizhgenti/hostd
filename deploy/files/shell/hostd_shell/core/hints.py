"""What the screen tells the person about the buttons, and about the row the
cursor is on: derived from the navigator's state, never stored."""

from dataclasses import dataclass

from hostd_shell import input as inp
from hostd_shell import views
from hostd_shell.core import nav


@dataclass
class Mode:
    """Which input was used last: its buttons are the ones the hints show.
    device is "keys" or "controller"; family the controller's kind (xbox,
    playstation, nintendo, wii, generic; "" when unknown)."""
    device: str = "keys"
    family: str = ""

    def use(self, device):
        """The input in use is device. Whether that changed it."""
        if device == self.device:
            return False
        self.device = device
        return True

    def use_family(self, family):
        if family == self.family:
            return False
        self.family = family
        return True


def hints(n, mode):
    """[{button, what}]: what each button does now, for the open overlay or
    the current row; only what the row can do, and back where there is one."""
    ov = n.overlay
    if isinstance(ov, nav.Question):
        pairs = [(inp.NEXT_TAB, "answer"), (inp.ACTIVATE, "choose"), (inp.BACK, "cancel")]
    elif isinstance(ov, nav.Level):
        pairs = [(inp.UP, "change"), (inp.BACK, "done")]
    elif isinstance(ov, nav.Options):
        pairs = [(inp.UP, "move"), (inp.ACTIVATE, "choose"), (inp.BACK, "close")]
    else:
        row = n.current_row()
        pairs = []
        if row is not None and (row.call or row.kind == views.SLIDER or row.confirm):
            pairs.append((inp.ACTIVATE, "open" if n.apps_tab() else "choose"))
            if row.sheet:
                pairs.append((inp.ACTIONS, "options"))
        elif row is not None and row.sheet:
            pairs.append((inp.ACTIVATE, "options"))
        if row is not None and row.instance:
            pairs.append((inp.CLOSE, "close"))
        if n.filter or n.in_folder() or n.in_page():
            pairs.append((inp.BACK, "back"))
    return [{"button": b, "what": w} for b, w in inp.hint_pairs(pairs, mode.device, mode.family)]


def focus_info(row):
    """The current row as the tiles view's right side shows it: title,
    second line, state, banner (a file URL) and details."""
    if row is None:
        return {}
    return {"title": row.title, "subtitle": row.value if row.kind == views.LAUNCHER else row.subtitle,
            "badge": row.badge, "tone": row.tone, "mark": row.mark, "launcher": row.kind == views.LAUNCHER,
            "banner": "file://" + row.banner if row.banner else "", "details": row.details}
