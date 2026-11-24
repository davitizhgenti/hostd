"""The status strip at the top left: the services that are up (a square
each, in the state's colour) and the devices in use (a square in each
one's colour), each line capped with a "+N". What it shows is core.status;
this keeps the latest answers and tells QML when the lines change."""

from PySide6.QtCore import Property, QObject, Signal
from PySide6.QtQml import QmlElement, QmlUncreatable

from hostd_shell.core import status
from hostd_shell.ui import QML_IMPORT_MAJOR_VERSION, QML_IMPORT_NAME  # noqa: F401 - read by @QmlElement
from hostd_shell.ui.pointers import Pointers


@QmlElement
@QmlUncreatable("The shell makes it.")
class Strip(QObject):
    servicesChanged = Signal()
    devicesChanged = Signal()

    def __init__(self, pointers: Pointers, parent=None):
        super().__init__(parent)
        self._pointers = pointers
        self._instances, self._apps = [], []
        self._services, self._services_more = [], 0
        self._devices, self._devices_more = [], 0
        pointers.stripChanged.connect(self._devices_now)
        self._devices_now()

    # --- from hostd ---

    def set_instances(self, instances):
        self._instances = instances or []
        self._services_now()

    def set_apps(self, items):
        self._apps = [it["spec"] for it in items or [] if isinstance(it, dict) and it.get("spec")]
        self._services_now()

    def _services_now(self):
        shown, more = status.capped(status.services(self._instances, self._apps))
        if (shown, more) != (self._services, self._services_more):
            self._services, self._services_more = shown, more
            self.servicesChanged.emit()

    def _devices_now(self):
        shown, more = status.capped(self._pointers.strip)
        if (shown, more) != (self._devices, self._devices_more):
            self._devices, self._devices_more = shown, more
            self.devicesChanged.emit()

    # --- for QML ---

    @Property("QVariantList", notify=servicesChanged)
    def services(self):
        return self._services

    @Property(int, notify=servicesChanged)
    def servicesMore(self):  # noqa: N802
        return self._services_more

    @Property("QVariantList", notify=devicesChanged)
    def devices(self):
        return self._devices

    @Property(int, notify=devicesChanged)
    def devicesMore(self):  # noqa: N802
        return self._devices_more

    @Property(int, constant=True)
    def maxSquares(self):  # noqa: N802
        return status.MAX
