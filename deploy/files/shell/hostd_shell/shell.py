"""The shell: the menu's objects (ui/) put together, and hostd's events
sent to the ones they are for.

Until the QML reads the objects directly (shell.menu, shell.pointers...) this
also has the flat surface the QML was written against: every property and
slot it has used, each handed to the object that owns it."""

import datetime

from PySide6.QtCore import Property, QObject, Signal, Slot

from hostd_shell import input as inp
from hostd_shell import notices
from hostd_shell.core import nav, sources
from hostd_shell.daemon import Events, Result
from hostd_shell.ui.backdrop import Backdrop, Clock
from hostd_shell.ui.menu import APPS, Menu
from hostd_shell.ui.notices import Notices
from hostd_shell.ui.osd import Osd
from hostd_shell.ui.pointers import MOUSE, Pointers

SETTINGS, STATUS, USERS = "settings", "status", "users"  # the fixed categories' keys; a tab's is "tab:" and its folder


class Shell(QObject):
    # What the QML listens to, each from the object that says it.
    changed = Signal()  # anything of the menu's, the pointers' or the message
    clockChanged = Signal()
    categoriesChanged = Signal()
    backgroundChanged = Signal()
    menuVisibleChanged = Signal()
    menuClosed = Signal()  # the menu was hidden: its launcher can end
    noticeClosed = Signal(int, int)  # id, reason (the notification spec's)
    pointerChanged = Signal()
    hoversChanged = Signal()
    stripChanged = Signal()
    pointerIdsChanged = Signal()
    pointerPressed = Signal(str)  # a Wii Remote's button went down: click where it points
    devicesChanged = Signal()
    calibrationChanged = Signal()

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
        self.backdrop = Backdrop(tokens, now, look_path, self)
        self.clock = Clock(now, self)
        self.osd_ = Osd(self)
        self.notices = Notices(self)
        self.pointers_ = Pointers(self)
        self.menu = Menu(tokens, ask, post, self.backdrop, now, has_icon, menu_path, has_tab_icon, self)
        self.menu.pointers = self.pointers_
        self.menu.stream_up = lambda: self.events is not None and self.events.connected
        self.pointers_.said.connect(self.menu.say)
        self.backdrop.folderChanged.connect(self.menu.look_changed)
        self.menu.loader.watch(sources.DEVICES, self.pointers_.set_devices, absent=[])  # an older hostd has none
        # what each says, said again as the flat surface says it
        self.menu.categoriesChanged.connect(self.categoriesChanged)
        for signal in (self.menu.cursorChanged, self.menu.overlayChanged, self.menu.hintsChanged, self.menu.messageChanged):
            signal.connect(self.changed)
        self.menu.visibleChanged.connect(self.menuVisibleChanged)
        self.menu.closed.connect(self.menuClosed)
        self.clock.changed.connect(self.clockChanged)
        self.backdrop.changed.connect(self.backgroundChanged)
        self.notices.closed.connect(self.noticeClosed)
        p = self.pointers_
        for ours, theirs in ((self.pointerChanged, p.pointerChanged), (self.hoversChanged, p.hoversChanged),
                             (self.stripChanged, p.stripChanged), (self.pointerIdsChanged, p.idsChanged),
                             (self.devicesChanged, p.devicesChanged), (self.calibrationChanged, p.calibrationChanged),
                             (self.pointerPressed, p.pressed)):
            theirs.connect(ours)

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
        self.clock.start()
        self.backdrop.start({self._dir(self.menu.menu_path)})
        self.menu.start()
        self._load_devices()

    @staticmethod
    def _dir(path):
        import os
        return os.path.dirname(path)

    # --- hostd's events, sent to the object they are for ---

    def on_event(self, ev):
        kind = ev.get("type", "")
        data = ev.get("data") or {}
        listening = self.menu.window.listening
        if kind == "display.pointer":  # a Wii Remote's, while the menu is in front
            if listening:
                text = lambda k: data.get(k) if isinstance(data.get(k), str) else ""  # noqa: E731
                visible = self.pointers_.remote_at(data.get("x"), data.get("y"), bool(data.get("visible")),
                                                   bool(data.get("down")), text("family"), text("device") or "wii",
                                                   text("name"), text("colour"))
                if visible:
                    self.menu.use("controller")
            return
        if kind == "display.nav":  # a controller, while the menu is in front
            verb = inp.verb_for_nav(data.get("what", ""))
            if verb and listening:
                family = data.get("family") if isinstance(data.get("family"), str) else ""
                self.menu.use("controller", family)
                device = data.get("device") if isinstance(data.get("device"), str) else ""
                if family != "wii":  # a Wii Remote's buttons go with its pointer
                    self.pointers_.hide_mouse()
                elif verb == inp.ACTIVATE and self.pointers_.is_pointing(device or "wii"):
                    return  # its A button clicks where it points (pointerPressed), not the highlighted row
                self.menu.handle(verb)
            return
        if kind == "display.calibrate":  # how a Wii Remote's calibration goes
            self.pointers_.calibration_event(data)
            return
        osd = notices.osd_for(ev)
        if osd is not None:
            self.osd_.show(*osd)
        self.menu.on_event(kind)

    # --- notices ---

    def notify(self, app, summary, body, replaces_id, urgency, expire_ms):
        return self.notices.notify(app, summary, body, replaces_id, urgency, expire_ms)

    def close_notice(self, nid):
        self.notices.close_notice(nid)

    # --- the flat surface: slots ---

    @Slot(str)
    def handle(self, verb):
        self.menu.handle(verb)

    @Slot(str, str, bool, result=bool)
    def key(self, name, text, shift):
        return self.menu.key(name, text, shift)

    @Slot(int, str, bool, result=bool)
    def keyCode(self, code, text, shift):  # noqa: N802
        return self.menu.keyCode(code, text, shift)

    @Slot(int, str)
    def clickItem(self, i, button="left"):  # noqa: N802
        self.menu.clickItem(i, button)

    @Slot(int)
    def clickCategory(self, c):  # noqa: N802
        self.menu.clickCategory(c)

    @Slot(int)
    def clickOption(self, i):  # noqa: N802
        self.menu.clickOption(i)

    @Slot()
    def dismiss(self):
        self.menu.dismiss()

    @Slot(int, int)
    def wheel(self, dx, dy):
        self.menu.wheel(dx, dy)

    @Slot(str)
    def mouseButton(self, button):  # noqa: N802
        self.menu.mouseButton(button)

    @Slot(int)
    def hover(self, i):
        self.pointers_.hover(i)

    @Slot(str, int)
    def pointerHover(self, device, i):  # noqa: N802
        self.pointers_.pointerHover(device, i)

    @Slot(float, float, int, int)
    def mouseAt(self, x, y, width, height):  # noqa: N802
        self.pointers_.mouseAt(x, y, width, height)

    @Slot(bool)
    def mouseDown(self, down):  # noqa: N802
        self.pointers_.mouseDown(down)

    @Slot()
    def hide_pointer(self):
        self.pointers_.hide_mouse()

    @Slot()
    def showMenu(self):  # noqa: N802
        self.menu.showMenu()
        self.notices.expire()

    @Slot()
    def hideMenu(self):  # noqa: N802
        self.menu.hideMenu()

    @Slot(bool)
    def setMenuActive(self, active):  # noqa: N802
        self.menu.setMenuActive(active)

    # --- the flat surface: properties ---

    categories = Property("QVariantList", lambda s: s.menu.categories, notify=categoriesChanged)
    pageModels = Property("QVariantList", lambda s: s.menu.pageModels, notify=categoriesChanged)
    options = Property(QObject, lambda s: s.menu.options, constant=True)
    noticeModel = Property(QObject, lambda s: s.notices.model, constant=True)
    pointer = Property("QVariantMap", lambda s: s.pointers_.pointer, notify=pointerChanged)
    pointers = Property("QVariantList", lambda s: s.pointers_.pointers, notify=pointerChanged)
    calibrating = Property("QVariantMap", lambda s: s.pointers_.calibrating, notify=calibrationChanged)
    strip = Property("QVariantList", lambda s: s.pointers_.strip, notify=stripChanged)
    pointerIds = Property("QVariantList", lambda s: s.pointers_.pointerIds, notify=pointerIdsChanged)
    hovers = Property("QVariantList", lambda s: s.pointers_.hovers, notify=hoversChanged)
    deviceList = Property("QVariantList", lambda s: s.pointers_.deviceList, notify=devicesChanged)
    category = Property(int, lambda s: s.menu.category, notify=changed)
    view = Property(str, lambda s: s.menu.view, notify=changed)
    folderPath = Property("QVariantList", lambda s: s.menu.folderPath, notify=changed)  # noqa: N815
    focus = Property("QVariantMap", lambda s: s.menu.focus, notify=changed)
    section = Property(str, lambda s: s.menu.section, notify=changed)
    headersBefore = Property(int, lambda s: s.menu.headersBefore, notify=changed)
    rowsBefore = Property(int, lambda s: s.menu.rowsBefore, notify=changed)
    clockTime = Property(str, lambda s: s.clock.time, notify=clockChanged)
    clockDate = Property(str, lambda s: s.clock.date, notify=clockChanged)
    windowed = Property(bool, lambda s: not s.menu.fullscreen, constant=True)
    current = Property(int, lambda s: s.menu.current, notify=changed)
    summary = Property(str, lambda s: s.menu.summary, notify=changed)
    filter = Property(str, lambda s: s.menu.filter, notify=changed)
    panel = Property(str, lambda s: s.menu.panel, notify=changed)
    panelTitle = Property(str, lambda s: s.menu.panelTitle, notify=changed)
    option = Property(int, lambda s: s.menu.option, notify=changed)
    levelValue = Property(str, lambda s: s.menu.levelValue, notify=changed)
    levelLevel = Property(int, lambda s: s.menu.levelLevel, notify=changed)
    question = Property(str, lambda s: s.menu.question, notify=changed)
    answerYes = Property(bool, lambda s: s.menu.answerYes, notify=changed)
    message = Property(str, lambda s: s.menu.message, notify=changed)
    messageError = Property(bool, lambda s: s.menu.messageError, notify=changed)
    hints = Property("QVariantList", lambda s: s.menu.hintList, notify=changed)
    menuVisible = Property(bool, lambda s: s.menu.visible, notify=menuVisibleChanged)
    bgTop = Property(str, lambda s: s.backdrop.top, notify=backgroundChanged)
    bgBottom = Property(str, lambda s: s.backdrop.bottom, notify=backgroundChanged)
    bgImage = Property(str, lambda s: s.backdrop.image, notify=backgroundChanged)
    osd = Property(QObject, lambda s: s.osd_, constant=True)  # name, value, level, visible

    # --- what the tests reach ---

    def __getattr__(self, name):
        """What is not the shell's own is the menu's (page, refresh, state, set_filter...)."""
        if name in ("menu", "__setstate__") or name.startswith("__"):
            raise AttributeError(name)
        return getattr(self.menu, name)

    @property
    def cats(self):
        return self.menu.nav.cats

    @property
    def currents(self):
        return self.menu.nav.currents

    @property
    def paths(self):
        return self.menu.nav.paths

    @property
    def all_rows(self):
        return self.menu.nav.all_rows

    @property
    def devices(self):
        return self.pointers_.core.devices

    @devices.setter
    def devices(self, value):
        self.pointers_.set_devices(value)

    @property
    def look(self):
        return self.backdrop.look

    @property
    def mouse_ms(self):
        return self.pointers_.mouse_ms

    @mouse_ms.setter
    def mouse_ms(self, value):
        self.pointers_.mouse_ms = value

    @property
    def pointer_timer(self):
        return self.pointers_._fade

    @property
    def mouse_timer(self):
        return self.pointers_._mouse_timer

    @property
    def fullscreen(self):
        return self.menu.fullscreen

    @fullscreen.setter
    def fullscreen(self, value):
        self.menu.fullscreen = value

    def _hints(self):
        return self.menu.hintList

    def _load_devices(self):
        self.menu.loader.refresh_watched({sources.DEVICES})

    def _refresh_page(self):
        self.menu._refresh_page()

    @property
    def _menu_visible(self):
        return self.menu.window.visible

    @_menu_visible.setter
    def _menu_visible(self, value):
        self.menu.window.visible = value

    @property
    def _filter(self):
        return self.menu.nav.filter


__all__ = ["APPS", "MOUSE", "SETTINGS", "STATUS", "USERS", "Shell", "nav"]
