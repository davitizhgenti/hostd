"""The shell: the menu's objects (ui/) put together, and hostd's events
sent to the ones they are for. QML reads the objects (shell.menu,
shell.pointers, ...) and nothing else of it."""

import datetime

from PySide6.QtCore import Property, QObject, Signal
from PySide6.QtQml import QmlElement, QmlUncreatable

from hostd_shell import input as inp
from hostd_shell import notices
from hostd_shell.core import sources
from hostd_shell.daemon import Events, Result
from hostd_shell.ui import QML_IMPORT_MAJOR_VERSION, QML_IMPORT_NAME  # noqa: F401 - read by @QmlElement
from hostd_shell.ui.backdrop import Backdrop, Clock
from hostd_shell.ui.menu import Menu
from hostd_shell.ui.notices import Notices
from hostd_shell.ui.osd import Osd
from hostd_shell.ui.pointers import Pointers
from hostd_shell.ui.status import Strip


@QmlElement
@QmlUncreatable("The app makes it.")
class Shell(QObject):
    # What the app listens to.
    menuClosed = Signal()  # the menu was hidden: its launcher can end
    noticeClosed = Signal(int, int)  # id, reason (the notification spec's)

    def __init__(self, api, tokens, now=datetime.datetime.now, look_path=None, has_icon=None, menu_path=None,
                 has_tab_icon=None, parent=None, daemon=None):
        """api: hostd's blocking client (answers at once: the tests, and
        anything that cannot wait); daemon: the asynchronous one the real
        shell uses."""
        super().__init__(parent)
        self.api, self.tokens, self.now = daemon or api, tokens, now
        if daemon is not None:
            ask, post = daemon.ask, daemon.request
        else:
            ask, post = self._ask_now, self._post_now
        self.daemon, self.events = daemon, None
        self._backdrop = Backdrop(tokens, now, look_path, self)
        self._clock = Clock(now, self)
        self._osd = Osd(self)
        self._notices = Notices(self)
        self._pointers = Pointers(self)
        self._strip = Strip(self._pointers, self)
        self._menu = Menu(tokens, ask, post, self._backdrop, now, has_icon, menu_path, has_tab_icon, self)
        self._menu.pointers = self._pointers
        self._menu.stream_up = lambda: self.events is not None and self.events.connected
        self._pointers.said.connect(self._menu.say)
        self._backdrop.folderChanged.connect(self._menu.look_changed)
        self._menu.loader.watch(sources.DEVICES, self._pointers.set_devices, absent=[])  # an older hostd has none
        self._menu.loader.watch(sources.INSTANCES, self._strip.set_instances)
        self._menu.loader.watch(sources.APPS, self._strip.set_apps)
        self._menu.closed.connect(self.menuClosed)
        self._notices.closed.connect(self.noticeClosed)

    # --- reaching hostd when it can be had at once ---

    def _ask_now(self, path, callback):
        try:
            answer = sources.ask(self.api, path)
        except Exception as e:  # noqa: BLE001 - hostd is not reachable
            answer = sources.Answer(False, message=str(e))
        callback(answer)

    def _post_now(self, method, path, body, callback, timeout_ms=None):
        try:
            payload = self.api.call(method, path, body)
        except Exception as e:  # noqa: BLE001 - said on the screen
            status = getattr(e, "status", 0)
            callback(Result(False, status, None, str(e), getattr(e, "code", ""), reached=bool(status)))
        else:
            callback(Result(True, 200, payload))

    # --- starting ---

    def start(self):
        """Follow hostd's events and watch the background setting."""
        if self.daemon is not None:
            self.events = Events(self.daemon, parent=self)
            self.events.event.connect(self.on_event)
            self.events.start()
        self._clock.start()
        self._backdrop.start({self._dir(self._menu.menu_path)})
        self._menu.start()
        self._menu.loader.refresh_watched({sources.DEVICES, sources.INSTANCES, sources.APPS})

    @staticmethod
    def _dir(path):
        import os
        return os.path.dirname(path)

    # --- hostd's events, sent to the object they are for ---

    def on_event(self, ev):
        kind = ev.get("type", "")
        data = ev.get("data") or {}
        listening = self._menu.window.listening
        if kind == "display.pointer":  # a Wii Remote's, while the menu is in front
            if listening:
                text = lambda k: data.get(k) if isinstance(data.get(k), str) else ""  # noqa: E731
                visible = self._pointers.remote_at(data.get("x"), data.get("y"), bool(data.get("visible")),
                                                   bool(data.get("down")), text("family"), text("device") or "wii",
                                                   text("name"), text("colour"))
                if visible:
                    self._menu.use("controller")
            return
        if kind == "display.nav":  # a controller, while the menu is in front
            verb = inp.verb_for_nav(data.get("what", ""))
            if verb and listening:
                family = data.get("family") if isinstance(data.get("family"), str) else ""
                self._menu.use("controller", family)
                device = data.get("device") if isinstance(data.get("device"), str) else ""
                if family != "wii":  # a Wii Remote's buttons go with its pointer
                    self._pointers.hide_mouse()
                elif verb == inp.ACTIVATE and self._pointers.is_pointing(device or "wii"):
                    return  # its A button clicks where it points (pointerPressed), not the highlighted row
                self._menu.handle(verb)
            return
        if kind == "display.calibrate":  # how a Wii Remote's calibration goes
            self._pointers.calibration_event(data)
            return
        osd = notices.osd_for(ev)
        if osd is not None:
            self._osd.show(*osd)
        self._menu.on_event(kind)

    # --- for the bus (notifications) and the app ---

    def notify(self, app, summary, body, replaces_id, urgency, expire_ms):
        return self._notices.notify(app, summary, body, replaces_id, urgency, expire_ms)

    def close_notice(self, nid):
        self._notices.close_notice(nid)

    def showMenu(self):  # noqa: N802 - the bus calls it
        self._menu.showMenu()
        self._notices.expire()

    def hideMenu(self):  # noqa: N802
        self._menu.hideMenu()

    # --- for QML: the objects, each typed ---

    @Property(Menu, constant=True)
    def menu(self):
        return self._menu

    @Property(Pointers, constant=True)
    def pointers(self):
        return self._pointers

    @Property(Strip, constant=True)
    def strip(self):
        return self._strip

    @Property(Notices, constant=True)
    def notices(self):
        return self._notices

    @Property(Osd, constant=True)
    def osd(self):
        return self._osd

    @Property(Backdrop, constant=True)
    def backdrop(self):
        return self._backdrop

    @Property(Clock, constant=True)
    def clock(self):
        return self._clock
