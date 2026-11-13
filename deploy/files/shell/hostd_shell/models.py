"""Qt list models over views.Row lists, for the QML views.

A RowsModel holds one list of rows (a category's items, or an item's
options). When the new rows are the same items as before (the same keys in
the same order) only their data changes, so views keep their place and
animate nothing; otherwise the model resets.
"""

from PySide6.QtCore import QAbstractListModel, QByteArray, Qt

from hostd_shell import views

ROLES = ("title", "subtitle", "value", "badge", "tone", "mark", "kind", "icon", "appIcon", "app",
         "level", "mono", "options", "key", "tile", "banner", "details")


def role_value(row, name):
    if name == "appIcon":
        return row.app_icon
    if name == "mono":
        # A second line that is an id is set in the monospace face.
        return bool(row.subtitle) and row.subtitle in (row.app, row.instance)
    if name == "options":
        return bool(row.sheet)
    if name in ("tile", "banner"):
        pic = getattr(row, name)
        return "file://" + pic if pic else ""
    if name == "key":
        return "\x1f".join(str(x) for x in row.key())
    return getattr(row, name)


class RowsModel(QAbstractListModel):
    def __init__(self, parent=None):
        super().__init__(parent)
        self.rows = []

    # --- Qt's model interface ---

    def rowCount(self, parent=None):  # noqa: N802 - Qt's name
        return 0 if parent is not None and parent.isValid() else len(self.rows)

    def roleNames(self):  # noqa: N802
        return {Qt.UserRole + i: QByteArray(n.encode()) for i, n in enumerate(ROLES)}

    def data(self, index, role=Qt.DisplayRole):
        if not index.isValid() or not 0 <= index.row() < len(self.rows):
            return None
        i = role - Qt.UserRole
        if 0 <= i < len(ROLES):
            return role_value(self.rows[index.row()], ROLES[i])
        if role == Qt.DisplayRole:
            return self.rows[index.row()].title
        return None

    # --- for the shell ---

    def set_rows(self, rows):
        rows = list(rows)
        if [r.key() for r in rows] == [r.key() for r in self.rows]:
            self.rows = rows
            if rows:
                self.dataChanged.emit(self.index(0), self.index(len(rows) - 1))
            return False
        self.beginResetModel()
        self.rows = rows
        self.endResetModel()
        return True

    def row(self, i):
        return self.rows[i] if 0 <= i < len(self.rows) else None

    def index_of(self, key):
        return next((i for i, r in enumerate(self.rows) if r.key() == key), -1)

    def step(self, i, direction):
        """The next row that can be chosen from i, going direction (1 or
        -1); i itself if there is none."""
        j = i + direction
        while 0 <= j < len(self.rows):
            if self.rows[j].kind != views.HEADER:
                return j
            j += direction
        return i

    def first(self):
        return self.step(-1, 1) if self.rows else -1
