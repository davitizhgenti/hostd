"""The pointers (the mouse's and each Wii Remote's), the devices strip and a
remote's calibration, for QML. The logic is core.pointers.Pointers; this owns
the clocks (the mouse's moves are applied at most every few milliseconds, and
its pointer fades) and says what changed through signals of their own, so
only what changed is re-read."""

import time

from PySide6.QtCore import Property, QObject, QTimer, Signal, Slot
from PySide6.QtQml import QmlElement, QmlUncreatable

from hostd_shell.core import pointers as core
from hostd_shell.ui import QML_IMPORT_MAJOR_VERSION, QML_IMPORT_NAME  # noqa: F401 - read by @QmlElement

MOUSE = core.MOUSE
POINTER_MS = 3000  # a mouse's pointer fades after this long without moving
MOUSE_MS = 8  # a mouse's moves are applied at most this often (a gaming mouse sends a thousand a second)


@QmlElement
@QmlUncreatable("The shell makes it.")
class Pointers(QObject):
    pointerChanged = Signal()  # any pointer moved, showed, went, or changed its look
    idsChanged = Signal()  # a device's pointer came to exist
    hoversChanged = Signal()  # the rows with a pointer over them, or their colours
    stripChanged = Signal()  # the devices strip
    devicesChanged = Signal()
    calibrationChanged = Signal()
    pressed = Signal(str)  # a Wii Remote's button went down: click where it points
    said = Signal(str, bool)  # a calibration's outcome, for the status line: text, error

    def __init__(self, parent=None, mouse_ms=MOUSE_MS, pointer_ms=POINTER_MS, clock=time.monotonic):
        super().__init__(parent)
        self.core = core.Pointers()
        self.mouse_ms, self.clock = mouse_ms, clock  # mouse_ms 0: every move is applied at once (the tests)
        self._calibrating = {}
        self._mouse_timer = QTimer(self, singleShot=True, timeout=self._apply_mouse)
        self._fade = QTimer(self, singleShot=True, interval=pointer_ms, timeout=self.hide_mouse)

    # --- for QML ---

    @Property("QVariantMap", notify=pointerChanged)
    def pointer(self):
        """The pointer that moved last (all that one pointer showed once)."""
        return self.core.last_shown()

    @Property("QVariantList", notify=pointerChanged)
    def pointers(self):
        return self.core.all_shown()

    @Property("QVariantList", notify=idsChanged)
    def pointerIds(self):
        return list(self.core.pointers)

    @Property("QVariantList", notify=hoversChanged)
    def hovers(self):
        return self.core.hovers()

    @Property("QVariantList", notify=stripChanged)
    def strip(self):
        return self.core.strip()

    @Property("QVariantList", notify=devicesChanged)
    def deviceList(self):
        return list(self.core.devices)

    @Property("QVariantMap", notify=calibrationChanged)
    def calibrating(self):
        return dict(self._calibrating)

    @Slot(float, float, int, int)
    def mouseAt(self, x, y, width, height):  # noqa: N802 - called from QML
        """The mouse moved to x, y in a window of width x height."""
        changed, wait = self.core.mouse_moved(x, y, width, height, self.clock(), self.mouse_ms / 1000)
        self._tell(changed)
        if changed:
            self._fade.start()
        if wait is not None and not self._mouse_timer.isActive():
            self._mouse_timer.start(max(1, int(wait * 1000)))

    def _apply_mouse(self):
        changed = self.core.apply_mouse(self.clock())
        self._tell(changed)
        self._fade.start()

    @Slot(int)
    def hover(self, row):
        """The mouse is over row `row` of the list (-1: over none)."""
        self._tell(self.core.hover(MOUSE, row))

    @Slot(str, int)
    def pointerHover(self, device, row):  # noqa: N802 - called from QML
        """A Wii Remote's pointer is over row `row` of the list (-1: none)."""
        self._tell(self.core.hover(device, row))

    @Slot(bool)
    def mouseDown(self, down):  # noqa: N802 - called from QML
        self._tell(self.core.mouse_down(down))

    @Slot()
    def hide_mouse(self):
        """The mouse's pointer goes (a key or a pad is used, or it was still)."""
        self._fade.stop()
        self._tell(self.core.hide_mouse())

    # --- for the shell ---

    def reset_mouse(self):
        """The menu opens: the first sample is where the cursor already was."""
        self.core.reset_mouse()

    def remote_at(self, x, y, visible, down, source="wii", device="wii", name="", colour=""):
        """A Wii Remote's pointer: x, y as shares of the screen; not visible
        when it points away. Whether it is visible after this."""
        self._tell(self.core.remote_at(x, y, visible, down, source, device, name, colour))
        return bool(visible and isinstance(x, (int, float)) and isinstance(y, (int, float)))

    def is_pointing(self, device):
        return self.core.is_visible(device)

    def mouse_hover(self):
        return self.core.mouse_hover()

    def set_devices(self, devices):
        self._tell(self.core.set_devices(devices))

    def calibration_event(self, data):
        """A remote's calibration: the corner to aim at, or how it ended."""
        self._calibrating, text, error = core.calibration(data)
        if text:
            self.said.emit(text, error)
        self.calibrationChanged.emit()

    def _tell(self, changed):
        if changed.pointer:
            self.pointerChanged.emit()
        if changed.ids:
            self.idsChanged.emit()
        if changed.hovers:
            self.hoversChanged.emit()
        if changed.strip:
            self.stripChanged.emit()
        if changed.devices:
            self.devicesChanged.emit()
        if changed.pressed:
            self.pressed.emit(changed.pressed)
