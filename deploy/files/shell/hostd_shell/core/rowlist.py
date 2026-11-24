"""Finding things in a list of views.Row: pure functions over lists."""

from hostd_shell import views


def step(rows, i, direction):
    """The next row that can be chosen from i, going direction (1 or -1);
    i itself if there is none."""
    j = i + direction
    while 0 <= j < len(rows):
        if rows[j].kind != views.HEADER:
            return j
        j += direction
    return i


def first(rows):
    """The first row that can be chosen, -1 for an empty list."""
    return step(rows, -1, 1) if rows else -1


def index_of(rows, key):
    return next((i for i, r in enumerate(rows) if r.key() == key), -1)


def at(rows, i):
    return rows[i] if 0 <= i < len(rows) else None
