"""Where things go in the cross (the XMB layout). Plain numbers, no GTK.

The category row runs left and right through the cross point; the current
category sits on it. Its items run up and down through the same point: the
current one just under the row, the ones after it below, the ones before it
above the row, as on the PSP. Sizes are the [layout] tokens, already scaled.
"""

from dataclasses import dataclass


@dataclass(frozen=True)
class Metrics:
    x: int  # the cross point
    y: int
    category_step: int
    icon_category: int
    item_height: int
    item_current: int
    header_height: int
    gap: int  # between the category row and the items
    label_height: int  # the current category's name, under its icon

    @classmethod
    def of(cls, layout, scale, gap, label_height):
        def px(name):
            return round(layout[name] * scale)
        return cls(px("cross-x"), px("cross-y"), px("category-step"), px("icon-category"),
                   px("item-height"), px("item-current"), px("header-height"), round(gap * scale),
                   round(label_height * scale))

    @property
    def row_bottom(self):
        """The bottom of the category row: its icon and the current one's name."""
        return self.y + self.icon_category + self.label_height + self.gap


def category_x(m, index, current):
    """Where category index's left edge is when current is chosen."""
    return m.x + (index - current) * m.category_step


def item_ys(m, kinds, selected):
    """The top of every item of a column, for kinds ("header" or not), with
    selected (an index into kinds) current. Items after it are below the
    category row; items before it, above."""
    if not kinds:
        return []

    def height(i):
        if kinds[i] == "header":
            return m.header_height
        return m.item_current if i == selected else m.item_height

    selected = max(0, min(len(kinds) - 1, selected))
    ys = [0] * len(kinds)
    y = m.row_bottom
    for i in range(selected, len(kinds)):
        ys[i] = y
        y += height(i)
    y = m.y - m.gap
    for i in range(selected - 1, -1, -1):
        y -= height(i)
        ys[i] = y
    return ys


def ease(t):
    """An ease-out curve over 0..1: quick, then settling."""
    t = max(0.0, min(1.0, t))
    return 1 - (1 - t) ** 3
