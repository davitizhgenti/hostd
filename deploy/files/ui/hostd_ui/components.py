"""hostd's components: GTK widgets for the rows of views.py, styled only by
the classes of theme.css. Every screen builds from these, so they look and
behave the same everywhere (docs/design.md)."""

import gi

gi.require_version("Gtk", "4.0")
from gi.repository import Gtk  # noqa: E402

from hostd_ui import views  # noqa: E402

END = 3  # Pango.EllipsizeMode.END


def label(text, *classes, xalign=0.0, expand=False, wrap=False):
    w = Gtk.Label(label=text, xalign=xalign, hexpand=expand)
    if wrap:
        w.set_wrap(True)
        w.set_max_width_chars(60)
    else:
        w.set_ellipsize(END)
    for c in classes:
        w.add_css_class(c)
    return w


def row_widget(row):
    """A list row for a views.Row; the row is kept as .model."""
    w = Gtk.ListBoxRow(selectable=row.kind != views.HEADER, activatable=row.kind != views.HEADER)
    w.model = row
    if row.kind == views.HEADER:
        w.add_css_class("header")
        w.set_child(label(row.title.upper()))
        return w
    box = Gtk.Box(spacing=0)
    text = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, hexpand=True, valign=Gtk.Align.CENTER)
    title = label(row.title, "row-title")
    if row.tone == "danger" and not row.badge:
        title.add_css_class("danger")
    text.append(title)
    if row.subtitle:
        sub = label(row.subtitle, "row-subtitle")
        if row.subtitle == row.app or row.subtitle == row.instance:
            sub.add_css_class("mono")  # an id
        text.append(sub)
    box.append(text)
    if row.badge:
        badge = label(row.badge, "badge")
        badge.set_valign(Gtk.Align.CENTER)
        if row.tone:
            badge.add_css_class(row.tone)
        box.append(badge)
    if row.kind == views.SLIDER:
        bar = Gtk.LevelBar(min_value=0, max_value=150, value=min(150, row.level), valign=Gtk.Align.CENTER)
        bar.add_css_class("slider")
        # No offsets: one color for the filled part, whatever the level.
        for name in ("low", "high", "full"):
            bar.remove_offset_value(name)
        box.append(bar)
    if row.value:
        v = label(row.value, "row-value")
        v.set_valign(Gtk.Align.CENTER)
        box.append(v)
    w.set_child(box)
    return w


def list_box():
    lst = Gtk.ListBox(selection_mode=Gtk.SelectionMode.BROWSE, activate_on_single_click=True)
    lst.set_focusable(False)
    return lst


def fill(lst, rows):
    """Puts rows in lst; returns the widgets that can be chosen."""
    lst.remove_all()
    out = []
    for r in rows:
        w = row_widget(r)
        w.set_focusable(False)  # the window's keys move the selection
        lst.append(w)
        if r.kind != views.HEADER:
            out.append(w)
    return out


def scrolled(child, max_height=None):
    s = Gtk.ScrolledWindow(hscrollbar_policy=Gtk.PolicyType.NEVER, vexpand=max_height is None)
    if max_height:
        s.set_propagate_natural_height(True)
        s.set_propagate_natural_width(True)
        s.set_max_content_height(max_height)
    s.set_child(child)
    return s


def scroll_to(scroll, widget, within):
    """Scrolls scroll so widget (a child of within) is in view, with the
    section header just above it when scrolling up."""
    ok, b = widget.compute_bounds(within)
    if not ok:
        return
    adj = scroll.get_vadjustment()
    y, h = b.get_y(), b.get_height()
    above = widget.get_prev_sibling()
    if above is not None and getattr(getattr(above, "model", None), "kind", "") == views.HEADER:
        ok, hb = above.compute_bounds(within)
        if ok:
            h += y - hb.get_y()
            y = hb.get_y()
    if y < adj.get_value():
        adj.set_value(y)
    elif y + h > adj.get_value() + adj.get_page_size():
        adj.set_value(y + h - adj.get_page_size())


class TabBar(Gtk.Box):
    """The pages' names; the current one in the accent color."""

    def __init__(self, names):
        super().__init__(spacing=0)
        self.add_css_class("tabs")
        self.tabs = []
        for n in names:
            t = label(n, "tab")
            self.append(t)
            self.tabs.append(t)

    def set_current(self, i):
        for j, t in enumerate(self.tabs):
            (t.add_css_class if i == j else t.remove_css_class)("current")


class Sheet(Gtk.Box):
    """A row's actions, floating over the page."""

    def __init__(self):
        super().__init__(orientation=Gtk.Orientation.VERTICAL, halign=Gtk.Align.CENTER, valign=Gtk.Align.CENTER)
        self.add_css_class("sheet")
        self.title = label("", "summary")
        self.list = list_box()
        self.scroll = scrolled(self.list, max_height=640)
        self.append(self.title)
        self.append(self.scroll)
        self.set_visible(False)


class Dialog(Gtk.Box):
    """A question with two answers; the safe one (No) is chosen first."""

    def __init__(self):
        super().__init__(orientation=Gtk.Orientation.VERTICAL, halign=Gtk.Align.CENTER, valign=Gtk.Align.CENTER)
        self.add_css_class("dialog")
        self.question = label("", "question", wrap=True)
        buttons = Gtk.Box(halign=Gtk.Align.END)
        self.no, self.yes = label("No", "button", xalign=0.5), label("Yes", "button", xalign=0.5)
        buttons.append(self.no)
        buttons.append(self.yes)
        self.append(self.question)
        self.append(buttons)
        self.answer = False
        self.set_visible(False)

    def ask(self, question):
        self.question.set_text(question)
        self.choose(False)
        self.set_visible(True)

    def choose(self, yes):
        self.answer = yes
        (self.yes.add_css_class if yes else self.yes.remove_css_class)("current")
        (self.no.remove_css_class if yes else self.no.add_css_class)("current")


def notice_widget(app_name, summary, body, urgency=1):
    """A notice card: urgency 0 low, 1 normal, 2 critical (the D-Bus spec's)."""
    box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL)
    box.add_css_class("notice")
    box.add_css_class({0: "low", 2: "critical"}.get(urgency, "normal"))
    if app_name:
        box.append(label(app_name, "app-name"))
    box.append(label(summary, "summary-text", wrap=True))
    if body:
        box.append(label(body, "body-text", wrap=True))
    return box


class OSD(Gtk.Box):
    """A value being changed (the volume), or a short state (whose screen)."""

    def __init__(self):
        super().__init__(orientation=Gtk.Orientation.VERTICAL)
        self.add_css_class("osd")
        top = Gtk.Box()
        self.name = label("", "osd-label", expand=True)
        self.value = label("", "osd-value", xalign=1.0)
        top.append(self.name)
        top.append(self.value)
        self.bar = Gtk.LevelBar(min_value=0, max_value=150)
        for name in ("low", "high", "full"):
            self.bar.remove_offset_value(name)
        self.append(top)
        self.append(self.bar)

    def show_level(self, name, value, level):
        self.name.set_text(name)
        self.value.set_text(value)
        self.bar.set_visible(level is not None)
        if level is not None:
            self.bar.set_value(min(150, level))
