"""The background behind the apps (its colours from the look setting and the
hour), and the clock: what changes on its own as time goes by."""

import datetime
import os

from PySide6.QtCore import Property, QFileSystemWatcher, QObject, QTimer, Signal
from PySide6.QtQml import QmlElement, QmlUncreatable

from hostd_shell import background
from hostd_shell.ui import QML_IMPORT_MAJOR_VERSION, QML_IMPORT_NAME  # noqa: F401 - read by @QmlElement/

BACKGROUND_MS = 5 * 60 * 1000  # the month mode's light follows the hour
CLOCK_MS = 10000


@QmlElement
@QmlUncreatable("The shell makes it.")
class Backdrop(QObject):
    changed = Signal()  # its colours or its image
    folderChanged = Signal()  # something in the folders its files are in: the shell reads the other files too

    def __init__(self, tokens, now=datetime.datetime.now, look_path=None, parent=None):
        super().__init__(parent)
        self.tokens, self.now = tokens, now
        self.path = look_path or background.path()
        self.look = background.load(self.path)
        self._watcher = None
        self._hour = QTimer(self, interval=BACKGROUND_MS, timeout=self.changed.emit)

    @Property(str, notify=changed)
    def top(self):
        return (background.colors(self.look, self.now(), self.tokens) or ("", ""))[0]

    @Property(str, notify=changed)
    def bottom(self):
        return (background.colors(self.look, self.now(), self.tokens) or ("", ""))[1]

    @Property(str, notify=changed)
    def image(self):
        return ("file://" + self.look["image"]) if self.look["mode"] == "image" else ""

    def start(self, folders=()):
        """Watches the folders (this one's file and the menu file's) and follows the hour."""
        folders = sorted({os.path.dirname(self.path), *folders})
        for folder in folders:
            os.makedirs(folder, exist_ok=True)
        self._watcher = QFileSystemWatcher(folders, self)
        self._watcher.directoryChanged.connect(lambda _p: self.folderChanged.emit())
        self._hour.start()

    def reload(self):
        """Reads the setting again. Whether it changed."""
        look = background.load(self.path)
        if look == self.look:
            return False
        self.look = look
        self.changed.emit()
        return True

    def save(self, setting):
        """Writes the setting, then reads it again. Raises OSError."""
        background.save(setting, self.path)
        return self.reload()


@QmlElement
@QmlUncreatable("The shell makes it.")
class Clock(QObject):
    changed = Signal()

    def __init__(self, now=datetime.datetime.now, parent=None):
        super().__init__(parent)
        self.now = now
        self._timer = QTimer(self, interval=CLOCK_MS, timeout=self.changed.emit)

    def start(self):
        self._timer.start()

    @Property(str, notify=changed)
    def time(self):
        return self.now().strftime("%H:%M")

    @Property(str, notify=changed)
    def date(self):
        return self.now().strftime("%a %-d %B")
