"""The tiles view's wall: the rows of a tab laid out in lines, and moving
over them. A heading (a section label, or a launcher that heads its group)
is a line of its own; the games after it fill lines of `columns` tiles.
Up and down go by line, left and right along one; at a line's end they
return None, which the caller takes as "leave to the next category". Pure,
over lists of views.Row, as rowlist is."""

from hostd_shell import views

COLUMNS = 4  # tiles across; Place.tileColumns is the same number (a test holds them equal)


def heading(row):
    return row.kind in (views.HEADER, views.LAUNCHER)


def lines(rows, columns=COLUMNS):
    """[(heading?, [row indexes])]: the wall as it is drawn, top to bottom."""
    out, run = [], []
    for i, row in enumerate(rows):
        if heading(row):
            if run:
                out.append((False, run))
                run = []
            out.append((True, [i]))
        else:
            run.append(i)
            if len(run) == columns:
                out.append((False, run))
                run = []
    if run:
        out.append((False, run))
    return out


def locate(wall, i):
    """(line, column) of row i; (-1, 0) when it is on none."""
    for n, (_, members) in enumerate(wall):
        if i in members:
            return n, members.index(i)
    return -1, 0


def vertical(rows, i, direction, columns=COLUMNS):
    """The row one line up or down from i, in the same column (or the
    line's last tile when it is shorter); i itself at either end. A section
    label is passed over: it cannot be chosen."""
    wall = lines(rows, columns)
    n, col = locate(wall, i)
    if n < 0:
        return i
    n += direction
    while 0 <= n < len(wall):
        is_heading, members = wall[n]
        if not is_heading:
            return members[min(col, len(members) - 1)]
        if rows[members[0]].kind != views.HEADER:
            return members[0]
        n += direction
    return i


def horizontal(rows, i, direction, columns=COLUMNS):
    """The tile beside i on its line, or None at the line's end (and on a
    heading, which has no neighbours): the caller changes category."""
    wall = lines(rows, columns)
    n, col = locate(wall, i)
    if n < 0 or wall[n][0]:
        return None
    members = wall[n][1]
    j = col + direction
    return members[j] if 0 <= j < len(members) else None


def offset(rows, i, columns=COLUMNS):
    """(headings, tile lines) above the line of row i: where the wall
    scrolls to put it at the top."""
    wall = lines(rows, columns)
    n, _ = locate(wall, max(0, i))
    n = max(0, n)
    above = wall[:n]
    headings = sum(1 for is_heading, _ in above if is_heading)
    return headings, len(above) - headings
