"""The on-screen display: a name, a value and a level (the volume) shown
for a moment, as a QObject of its own that QML binds to (shell.osd)."""

from PySide6.QtCore import Property, QObject, QTimer, Signal
from PySide6.QtQml import QmlElement, QmlUncreatable

from hostd_shell.ui import QML_IMPORT_MAJOR_VERSION, QML_IMPORT_NAME  # noqa: F401 - read by @QmlElement

OSD_MS = 1600  # how long it stays


@QmlElement
@QmlUncreatable("The shell makes it.")
class Osd(QObject):
    changed = Signal()

    def __init__(self, parent=None, ms=OSD_MS):
        super().__init__(parent)
        self._name, self._value, self._level = "", "", -1
        self._visible = False
        self.timer = QTimer(self, singleShot=True, interval=ms, timeout=self.hide)

    def show(self, name, value, level):
        """Show name: value, with a bar at level (0-100), or none if level is None."""
        self._name, self._value, self._level = name, value, -1 if level is None else int(level)
        self._visible = True
        self.timer.start()
        self.changed.emit()

    def hide(self):
        self._visible = False
        self.changed.emit()

    @Property(bool, notify=changed)
    def visible(self):
        return self._visible

    @Property(str, notify=changed)
    def name(self):
        return self._name

    @Property(str, notify=changed)
    def value(self):
        return self._value

    @Property(int, notify=changed)
    def level(self):
        return self._level
