"""hostd's components: GTK widgets for the rows of views.py, styled only by
the classes of theme.css. Every screen builds from these, so they look and
behave the same everywhere (docs/design.md).

The menu is a cross (the PSP's XMB): Cross holds the category row and the
current category's item column and slides them; OptionsPanel comes in at
the right edge with a row's options (or the volume's level); ConfirmBand
asks across the middle of the screen."""

import time

import gi

gi.require_version("Gtk", "4.0")
from gi.repository import Gtk  # noqa: E402

from hostd_ui import cross, icons, views  # noqa: E402

END = 3  # Pango.EllipsizeMode.END


def label(text, *classes, xalign=0.0, expand=False, wrap=False, whole=False):
    """A label; long text ends in "…", unless wrap (more lines) or whole
    (always all of it: names placed freely, which would shrink to "…")."""
    w = Gtk.Label(label=text, xalign=xalign, hexpand=expand)
    if wrap:
        w.set_wrap(True)
        w.set_max_width_chars(60)
    elif not whole:
        w.set_ellipsize(END)
    for c in classes:
        w.add_css_class(c)
    return w


def clear(box):
    child = box.get_first_child()
    while child is not None:
        nxt = child.get_next_sibling()
        box.remove(child)
        child = nxt


def level_bar(level, vertical=False):
    bar = Gtk.LevelBar(min_value=0, max_value=150, value=min(150, level))
    if vertical:
        bar.set_orientation(Gtk.Orientation.VERTICAL)
        bar.set_inverted(True)
    # No offsets: one color for the filled part, whatever the level.
    for name in ("low", "high", "full"):
        bar.remove_offset_value(name)
    return bar


class Item(Gtk.Box):
    """One row of a column: its icon and title; when current also its second
    line, its state and its value. A header is a small section label."""

    def __init__(self, row, width, icon_size, color):
        super().__init__(valign=Gtk.Align.START)
        self.model = row
        self.set_size_request(width, -1)
        if row.kind == views.HEADER:
            self.add_css_class("section")
            self.append(label(row.title.upper(), "section-label", whole=True))
            return
        self.add_css_class("item")
        icon = icons.widget(row.icon, row.app_icon, row.title, icon_size, color)
        icon.set_valign(Gtk.Align.CENTER)
        self.append(icon)
        text = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, hexpand=True, valign=Gtk.Align.CENTER)
        text.add_css_class("item-text")
        self.title = label(row.title, "item-title")
        if row.tone == "danger" and not row.badge:
            self.title.add_css_class("danger")
        text.append(self.title)
        self.subtitle = None
        if row.subtitle:
            self.subtitle = label(row.subtitle, "item-subtitle")
            if row.subtitle in (row.app, row.instance):
                self.subtitle.add_css_class("mono")  # an id
            text.append(self.subtitle)
        self.append(text)
        if row.badge:
            badge = label(row.badge, "badge", xalign=1.0)
            badge.set_valign(Gtk.Align.CENTER)
            if row.tone:
                badge.add_css_class(row.tone)
            self.append(badge)
        self.level = None
        if row.kind == views.SLIDER:
            self.level = level_bar(row.level)
            self.level.add_css_class("slider")
            self.level.set_valign(Gtk.Align.CENTER)
            self.append(self.level)
        self.value = None
        if row.value:
            self.value = label(row.value, "item-value", xalign=1.0)
            self.value.set_valign(Gtk.Align.CENTER)
            self.append(self.value)
        self.set_current(False)

    def set_current(self, current):
        (self.add_css_class if current else self.remove_css_class)("current")
        if self.model.kind == views.HEADER:
            return
        for w in (self.subtitle, self.value, self.level):
            if w is not None:
                w.set_visible(current)

    def set_side(self, before):
        """Above the category row (before the current item) or below."""
        (self.add_css_class if before else self.remove_css_class)("before")


class Category(Gtk.Box):
    """A category on the row: its icon; its name only when it is current."""

    def __init__(self, name, icon, size, color):
        super().__init__(orientation=Gtk.Orientation.VERTICAL, valign=Gtk.Align.START)
        self.add_css_class("category")
        img = icons.widget(icon, "", name, size, color)
        img.set_halign(Gtk.Align.START)
        self.append(img)
        self.name = label(name, "category-name", whole=True)
        self.append(self.name)

    def set_current(self, current):
        (self.add_css_class if current else self.remove_css_class)("current")
        self.name.set_visible(current)


class Cross(Gtk.Fixed):
    """The category row and the current category's items, placed by
    cross.py and slid there over the motion token's time."""

    def __init__(self, metrics, width, icon_item, color, duration_ms):
        super().__init__(hexpand=True, vexpand=True)
        self.m = metrics
        self.item_width = max(200, width - metrics.x - metrics.x // 2)
        self.icon_item = icon_item
        self.color = color
        self.duration = duration_ms / 1000
        self.categories, self.items = [], []
        self.current_category = 0
        self.selected = 0
        self.on_click = None  # (item) -> None: a click on an item
        self.on_category = None  # (index) -> None: a click on a category
        self.moves = {}  # widget -> (from, to, start)
        self.places = {}  # widget -> where it is now
        self.tick = 0

    # --- what it shows ---

    def set_categories(self, entries):
        for i, (name, icon) in enumerate(entries):
            c = Category(name, icon, self.m.icon_category, self.color)
            click = Gtk.GestureClick()
            click.connect("released", lambda *_a, i=i: self.on_category and self.on_category(i))
            c.add_controller(click)
            self.categories.append(c)
            self.put(c, 0, 0)
        self.set_category(0, animate=False)

    def set_category(self, index, animate=True):
        self.current_category = index
        for i, c in enumerate(self.categories):
            c.set_current(i == index)
            self.place(c, cross.category_x(self.m, i, index), self.m.y, animate)
            c.set_opacity(1.0 if i == index else 0.5)

    def set_items(self, rows, selected):
        """Shows rows (views.Row) with selected (an index into rows) current."""
        for w in self.items:
            self.remove(w)
            self.places.pop(w, None)
            self.moves.pop(w, None)
        self.items = []
        for r in rows:
            w = Item(r, self.item_width, self.icon_item, self.color)
            if r.kind != views.HEADER:
                click = Gtk.GestureClick()
                click.connect("released", lambda *_a, w=w: self.on_click and self.on_click(w))
                w.add_controller(click)
            self.items.append(w)
            self.put(w, self.m.x, self.m.row_bottom)
        self.select(selected, animate=False)

    def select(self, index, animate=True):
        if not self.items:
            self.selected = 0
            return
        self.selected = max(0, min(len(self.items) - 1, index))
        ys = cross.item_ys(self.m, [w.model.kind for w in self.items], self.selected)
        for i, (w, y) in enumerate(zip(self.items, ys, strict=True)):
            w.set_current(i == self.selected)
            w.set_side(i < self.selected)
            self.place(w, self.m.x, y, animate)

    def current(self):
        return self.items[self.selected] if self.items else None

    # --- sliding ---

    def place(self, w, x, y, animate):
        start = self.places.get(w)
        if not animate or start is None or self.duration <= 0:
            self.places[w] = (x, y)
            self.moves.pop(w, None)
            self.move(w, x, y)
            return
        self.moves[w] = (start, (x, y), time.monotonic())
        if not self.tick:
            self.tick = self.add_tick_callback(self.on_tick)

    def on_tick(self, _widget, _clock):
        now = time.monotonic()
        for w, (a, b, t0) in list(self.moves.items()):
            k = cross.ease((now - t0) / self.duration)
            x, y = a[0] + (b[0] - a[0]) * k, a[1] + (b[1] - a[1]) * k
            self.places[w] = (x, y)
            self.move(w, x, y)
            if k >= 1:
                del self.moves[w]
        if not self.moves:
            self.tick = 0
            return False
        return True


class OptionsPanel(Gtk.Box):
    """At the right edge: a row's options (the PSP's triangle menu), or the
    level of a slider (the volume), changed with up and down."""

    def __init__(self, width, icon_size, color):
        super().__init__(orientation=Gtk.Orientation.VERTICAL, halign=Gtk.Align.END, vexpand=True)
        self.add_css_class("panel")
        self.set_size_request(width, -1)
        self.icon_size, self.color = icon_size, color
        self.title = label("", "panel-title")
        self.body = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, vexpand=True)
        self.append(self.title)
        self.append(self.body)
        self.items, self.index = [], 0
        self.row = None  # the row it is for
        self.slider = False
        self.set_visible(False)

    def show_options(self, row):
        self.row, self.slider = row, False
        self.title.set_text(row.title)
        clear(self.body)
        self.items = []
        for r in row.sheet:
            w = Gtk.Box()
            w.add_css_class("option")
            w.model = r
            if r.icon:
                img = icons.widget(r.icon, r.app_icon, r.title, self.icon_size, self.color)
                img.set_valign(Gtk.Align.CENTER)
                w.append(img)
            t = label(r.title, "option-title", expand=True)
            if r.tone == "danger" and not r.badge:
                t.add_css_class("danger")
            w.append(t)
            if r.badge:
                b = label(r.badge, "badge", xalign=1.0)
                if r.tone:
                    b.add_css_class(r.tone)
                w.append(b)
            self.body.append(w)
            self.items.append(w)
        self.choose(0)
        self.set_visible(True)

    def show_level(self, row):
        self.row, self.slider = row, True
        self.title.set_text(row.title)
        clear(self.body)
        self.items = []
        box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, vexpand=True, halign=Gtk.Align.START,
                      valign=Gtk.Align.CENTER)
        self.value = label(row.value, "panel-value", whole=True)
        self.bar = level_bar(row.level, vertical=True)
        self.bar.add_css_class("panel-level")
        self.bar.set_halign(Gtk.Align.START)
        box.append(self.value)
        box.append(self.bar)
        self.body.append(box)
        self.set_visible(True)

    def update_level(self, row):
        if self.slider and row is not None:
            self.row = row
            self.value.set_text(row.value)
            self.bar.set_value(min(150, row.level))

    def choose(self, index):
        if not self.items:
            return
        self.index = max(0, min(len(self.items) - 1, index))
        for i, w in enumerate(self.items):
            (w.add_css_class if i == self.index else w.remove_css_class)("current")

    def move(self, step):
        self.choose(self.index + step)

    def current(self):
        return self.items[self.index].model if self.items else None


class ConfirmBand(Gtk.Box):
    """A question across the middle of the screen, with No and Yes; the
    safe one (No) is chosen first."""

    def __init__(self):
        super().__init__(orientation=Gtk.Orientation.VERTICAL, valign=Gtk.Align.CENTER, hexpand=True)
        self.add_css_class("band")
        inner = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, halign=Gtk.Align.CENTER)
        self.question = label("", "question", wrap=True, xalign=0.5)
        buttons = Gtk.Box(halign=Gtk.Align.CENTER)
        self.no, self.yes = label("No", "button", xalign=0.5), label("Yes", "button", xalign=0.5)
        buttons.append(self.no)
        buttons.append(self.yes)
        inner.append(self.question)
        inner.append(buttons)
        self.append(inner)
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
    """A notice: urgency 0 low, 1 normal, 2 critical (the D-Bus spec's)."""
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
        self.bar = level_bar(0)
        self.append(top)
        self.append(self.bar)

    def show_level(self, name, value, level):
        self.name.set_text(name)
        self.value.set_text(value)
        self.bar.set_visible(level is not None)
        if level is not None:
            self.bar.set_value(min(150, level))
