"""The shell's state and behaviour: everything the screens show and do,
without drawing anything.

Shell holds what is current (the category, the item, an open options panel,
a question), turns verbs (input.py) into changes and requests to hostd, and
keeps the data fresh from hostd's events. The QML surfaces only show its
properties and models and hand it keys, clicks and the window's state, so
all of the behaviour is tested here without a screen.
"""

import datetime
import json
import os

from PySide6.QtCore import Property, QFileSystemWatcher, QObject, QTimer, Signal, Slot

from hostd_shell import api as hapi
from hostd_shell import background, notices, views
from hostd_shell import input as inp
from hostd_shell.models import RowsModel

APPS, USERS, SETTINGS, STATUS = range(len(views.CATEGORIES))
# Events after which a category lists again.
REFRESH_ON = {
    APPS: ("instance.", "app.", "hostd.", "bus."),
    USERS: ("seat.", "hostd."),
    SETTINGS: ("audio.", "display.output", "display.mode", "display.power", "hostd."),
    STATUS: ("hostd.", "action."),
}
OSD_MS = 1600
BACKGROUND_MS = 5 * 60 * 1000  # the month mode's light follows the hour


def fetch(api, category, look, tokens):
    """A category's rows and its summary line, from hostd."""
    if category == APPS:
        instances = api.get("/v1/instances") or []
        apps = [it["spec"] for it in (api.get("/v1/apps") or []) if it.get("spec")]
        return views.apps_page(instances, apps)
    if category == USERS:
        try:
            seats = api.get("/v1/seat")
        except hapi.APIError as e:
            if e.status != 404:
                raise
            return [views.Row("Switching users is not set up", icon="info",
                              subtitle="the seats add-on: sudo hostd-admin add seats")], ""
        return views.users_page(seats), ""
    if category == SETTINGS:
        try:
            audio = api.get("/v1/audio")
        except hapi.APIError:
            audio = None
        return views.settings_page(audio, api.get("/v1/display"), look, tokens), ""
    return views.status_page(api.get("/v1/version"), api.get("/v1/display"), api.get("/v1/actions?limit=12")), ""


class NoticesModel(RowsModel):
    """The notices on screen, as rows: title the summary, subtitle the body,
    value the app, tone the urgency."""

    def show(self, shown):
        self.set_rows([views.Row(n.summary, subtitle=n.body, value=n.app, app=str(n.id),
                                 tone={notices.CRITICAL: "danger", notices.LOW: "low"}.get(n.urgency, ""))
                       for n in shown])


class Shell(QObject):
    changed = Signal()  # any of the UI state below
    clockChanged = Signal()
    backgroundChanged = Signal()
    osdChanged = Signal()
    menuVisibleChanged = Signal()
    menuClosed = Signal()  # the menu was hidden: its launcher can end
    noticeClosed = Signal(int, int)  # id, reason (the spec's)
    _posted = Signal(object)

    def __init__(self, api, tokens, now=datetime.datetime.now, look_path=None, has_icon=None, parent=None):
        super().__init__(parent)
        self.api, self.tokens, self.now = api, tokens, now
        # Whether an app's icon can be drawn: rows keep only those, so the
        # screen shows the app's icon or its letter, never an empty space.
        self.has_icon = has_icon or (lambda name: True)
        self.look_path = look_path or background.path()
        self.look = background.load(self.look_path)
        self.pages = [RowsModel(self) for _ in views.CATEGORIES]
        self.all_rows = [[] for _ in views.CATEGORIES]
        self.summaries = ["" for _ in views.CATEGORIES]
        self.currents = [-1 for _ in views.CATEGORIES]
        self.options_model = RowsModel(self)
        self.notices_model = NoticesModel(self)
        self.queue = notices.Notices()

        self._category = APPS
        self._panel = ""  # "", "options" or "level"
        self._panel_row = None
        self._option = 0
        self._question = ""
        self._asked = None  # the call the question is about
        self._yes = False
        self._filter = ""
        self._message, self._error = "", False
        self._device = "keys"
        self._family = ""  # the kind of the controller used last (xbox, playstation, ...)
        self._menu_visible = False
        self._menu_active = False
        self._osd = ("", "", -1)
        self._osd_visible = False
        self.fullscreen = True  # the tests show the menu in a window of a set size

        self._posted.connect(lambda job: job[0](*job[1]))
        self.pending = set()
        self.debounce = QTimer(self, singleShot=True, interval=100, timeout=self._refresh_pending)
        self.notice_timer = QTimer(self, singleShot=True, timeout=self._expire_notices)
        self.osd_timer = QTimer(self, singleShot=True, interval=OSD_MS, timeout=self._hide_osd)
        self.events = None

    # --- starting ---

    def start(self):
        """Follow hostd's events and watch the background setting."""
        self.events = hapi.EventStream(self.api, self.on_event, lambda fn, *a: self._posted.emit((fn, a)))
        self.events.start()
        QTimer(self, interval=5000, timeout=self._poll).start()
        QTimer(self, interval=BACKGROUND_MS, timeout=self.backgroundChanged.emit).start()
        QTimer(self, interval=10000, timeout=self.clockChanged.emit).start()
        folder = os.path.dirname(self.look_path)
        os.makedirs(folder, exist_ok=True)
        self.watcher = QFileSystemWatcher([folder], self)
        self.watcher.directoryChanged.connect(lambda _p: self._reload_look())
        for c in range(len(views.CATEGORIES)):
            self.refresh(c)

    # --- data ---

    def refresh(self, category=None, keep=True):
        c = self._category if category is None else category
        try:
            rows, summary = fetch(self.api, c, self.look, self.tokens)
        except Exception as e:  # noqa: BLE001 - shown on the screen
            self.say(f"Cannot reach hostd: {e}", error=True)
            return
        if c == self._category and self._message.startswith("Cannot reach hostd"):
            self.say("")
        for r in rows:
            for x in [r, *r.sheet]:
                if x.app_icon and not self.has_icon(x.app_icon):
                    x.app_icon = ""
        self.all_rows[c] = rows
        self.summaries[c] = summary
        self._show_rows(c, keep)

    def _show_rows(self, c, keep=True):
        model = self.pages[c]
        old = model.row(self.currents[c]) if keep else None
        shown = views.visible(self.all_rows[c], self._filter if c == APPS else "")
        model.set_rows(shown)
        i = model.index_of(old.key()) if old is not None else -1
        if i < 0 or model.row(i).kind == views.HEADER:
            i = model.first()
        self.currents[c] = i
        if self._panel == "level" and c == self._category:
            self._panel_row = next((r for r in shown if r.key() == self._panel_row.key()), self._panel_row)
        self.changed.emit()

    def on_event(self, ev):
        kind = ev.get("type", "")
        if kind == "display.nav":  # a controller, while the menu is in front
            data = ev.get("data") or {}
            verb = inp.verb_for_nav(data.get("what", ""))
            if verb and self._menu_visible and self._menu_active:
                family = data.get("family") if isinstance(data.get("family"), str) else ""
                if family != self._family:
                    self._family = family
                    self.changed.emit()
                self._set_device("controller")
                self.handle(verb)
            return
        osd = notices.osd_for(ev)
        if osd is not None:
            self.show_osd(*osd)
        for c, prefixes in REFRESH_ON.items():
            if kind.startswith(prefixes):
                self.pending.add(c)
        if self.pending and not self.debounce.isActive():
            self.debounce.start()

    def _refresh_pending(self):
        busy = self._panel == "options" or self._question
        for c in sorted(self.pending):
            if not (busy and c == self._category):
                self.refresh(c)
        self.pending.clear()

    def _poll(self):
        if self.events is not None and not self.events.connected:
            self.refresh()  # the event stream is down: ask every few seconds

    # --- what is current ---

    def page(self):
        return self.pages[self._category]

    def section(self):
        """The title of the header the current item is under; ""."""
        model, i = self.page(), self.currents[self._category]
        for j in range(i, -1, -1):
            row = model.row(j)
            if row is not None and row.kind == views.HEADER:
                return row.title
        return ""

    def offset(self):
        """(headers, rows) before the current item: where the list scrolls
        to put it at the top. The view multiplies them by their heights."""
        model, i = self.page(), max(0, self.currents[self._category])
        headers = sum(1 for j in range(min(i, model.rowCount())) if model.row(j).kind == views.HEADER)
        return headers, max(0, i - headers)

    def current_row(self):
        row = self.page().row(self.currents[self._category])
        return row if row is not None and row.kind != views.HEADER else None

    def set_category(self, c):
        c %= len(views.CATEGORIES)
        if c == self._category:
            return
        self._close_overlays()
        if self._category == APPS and self._filter:
            self._filter = ""
            self._show_rows(APPS, keep=False)
        self._category = c
        self.refresh(c)
        self.changed.emit()

    def go_home(self):
        """The first category, its first item, with no filter."""
        if self._filter:
            self._filter = ""
            self._show_rows(APPS, keep=False)
        self._category = APPS
        self.currents[APPS] = self.pages[APPS].first()
        self.refresh(APPS)
        self.changed.emit()

    def move(self, steps):
        model, c = self.page(), self._category
        i = self.currents[c]
        for _ in range(abs(steps)):
            i = model.step(i, 1 if steps > 0 else -1)
        if i != self.currents[c]:
            self.currents[c] = i
            self.changed.emit()

    # --- what choosing does ---

    def choose(self, row):
        if row is None:
            return
        if row.confirm:
            self._asked, self._question, self._yes = row.call, row.confirm, False
        elif row.kind == views.SLIDER:
            self._panel, self._panel_row = "level", row
        elif row.call is not None:
            self.send(row.call)
        elif row.sheet:
            self.open_options(row)
        self.changed.emit()

    def open_options(self, row):
        if row is None or not row.sheet:
            return
        self._panel, self._panel_row, self._option = "options", row, 0
        self.options_model.set_rows(row.sheet)
        self.changed.emit()

    def send(self, call, refresh=True):
        if call.method == "LOCAL":
            return self._local(call)
        try:
            res = self.api.call(call.method, call.path, call.body)
        except Exception as e:  # noqa: BLE001 - shown on the screen
            self.say(str(e), error=True)
            return None
        text = views.result_text(res)
        self.say(text, error=bool(text))
        if refresh:
            QTimer.singleShot(150, lambda: self.refresh(self._category))
        return res

    def _local(self, call):
        """A change on this screen alone: the background."""
        if call.path == "background":
            try:
                background.save(call.body, self.look_path)
            except OSError as e:
                self.say(f"Cannot save the background: {e}", error=True)
                return None
            self._reload_look()
        return None

    def _reload_look(self):
        look = background.load(self.look_path)
        if look != self.look:
            self.look = look
            self.backgroundChanged.emit()
            self.refresh(SETTINGS)

    def back(self):
        if self._category == APPS and self._filter:
            self.set_filter("")
        elif self._category != APPS:
            self.set_category(APPS)
        else:
            self.send(views.Call("POST", "/v1/windows/back"), refresh=False)

    def _close_overlays(self):
        self._panel, self._panel_row, self._question, self._asked = "", None, "", None

    # --- verbs: the one place input turns into behaviour ---

    @Slot(str)
    def handle(self, verb):
        if self._question:
            if verb in (inp.LEFT, inp.RIGHT, inp.UP, inp.DOWN):
                self._yes = verb in (inp.RIGHT, inp.DOWN)
            elif verb == inp.ACTIVATE:
                call, yes = self._asked, self._yes
                self._close_overlays()
                if yes:
                    self.send(call)
            elif verb == inp.BACK:
                self._close_overlays()
            self.changed.emit()
            return
        if self._panel == "level":
            row = self._panel_row
            if verb == inp.UP:
                self.send(row.step)
            elif verb == inp.DOWN:
                self.send(row.step_back)
            elif verb in (inp.BACK, inp.ACTIVATE, inp.ACTIONS, inp.LEFT):
                self._close_overlays()
            self.changed.emit()
            return
        if self._panel == "options":
            n = self.options_model.rowCount()
            if verb in (inp.UP, inp.DOWN):
                self._option = max(0, min(n - 1, self._option + (1 if verb == inp.DOWN else -1)))
            elif verb == inp.ACTIVATE:
                row = self.options_model.row(self._option)
                self._close_overlays()
                self.choose(row)
            elif verb in (inp.BACK, inp.ACTIONS, inp.LEFT):
                self._close_overlays()
            self.changed.emit()
            return
        row = self.current_row()
        if verb in (inp.UP, inp.DOWN):
            self.move(1 if verb == inp.DOWN else -1)
        elif verb in (inp.PAGE_UP, inp.PAGE_DOWN):
            self.move(5 if verb == inp.PAGE_DOWN else -5)
        elif verb == inp.HOME:
            self.go_home()
        elif verb in (inp.LEFT, inp.PREV_TAB):
            self.set_category(self._category - 1)
        elif verb in (inp.RIGHT, inp.NEXT_TAB):
            self.set_category(self._category + 1)
        elif verb == inp.ACTIVATE:
            self.choose(row)
        elif verb == inp.ACTIONS:
            self.open_options(row)
        elif verb == inp.CLOSE and row is not None and row.instance:
            self.send(views.Call("POST", f"/v1/instances/{row.instance}/close"))
        elif verb == inp.BACK:
            self.back()
        self.changed.emit()

    @Slot(str, str, bool, result=bool)
    def key(self, name, text, shift):
        """A key on the menu: a verb, or typing into the Apps filter.
        Whether the shell took it."""
        self._set_device("keys")
        filtering = self._category == APPS and not self._panel and not self._question
        verb = inp.verb_for_key(name, shift, typing=filtering and bool(self._filter))
        if verb is None:
            if filtering and name == "Key_Backspace":
                self.set_filter(self._filter[:-1])
                return True
            if filtering and text and text.isprintable():
                self.set_filter(self._filter + text)
                return True
            return False
        self.handle(verb)
        return True

    @Slot(int, str, bool, result=bool)
    def keyCode(self, code, text, shift):  # noqa: N802 - called from QML
        """key() for a Qt key code."""
        from PySide6.QtCore import Qt
        try:
            name = Qt.Key(code).name
        except ValueError:
            name = ""
        return self.key(name, text, shift)

    @Slot(int)
    def clickItem(self, i):  # noqa: N802 - called from QML
        self._set_device("keys")
        if self._panel or self._question:
            return
        if i == self.currents[self._category]:
            self.choose(self.current_row())
        elif self.page().row(i) is not None and self.page().row(i).kind != views.HEADER:
            self.currents[self._category] = i
            self.changed.emit()

    @Slot(int)
    def clickCategory(self, c):  # noqa: N802
        if not self._panel and not self._question:
            self.set_category(c)

    @Slot(int)
    def clickOption(self, i):  # noqa: N802
        if self._panel == "options":
            self._option = i
            self.handle(inp.ACTIVATE)

    @Slot()
    def dismiss(self):
        """A click outside a panel or question."""
        self._close_overlays()
        self.changed.emit()

    @Slot(int)
    def wheel(self, dy):
        self.handle(inp.UP if dy > 0 else inp.DOWN)

    def set_filter(self, text):
        if text != self._filter:
            self._filter = text
            self._show_rows(APPS, keep=False)

    def say(self, text, error=False):
        self._message, self._error = text, error
        self.changed.emit()

    def _set_device(self, device):
        if device != self._device:
            self._device = device
            self.changed.emit()

    # --- the menu window ---

    @Slot()
    def showMenu(self):  # noqa: N802
        if not self._menu_visible:
            self._menu_visible = True
            self.menuVisibleChanged.emit()
        self.refresh()

    @Slot()
    def hideMenu(self):  # noqa: N802
        if self._menu_visible:
            self._menu_visible = False
            self._close_overlays()
            self.menuVisibleChanged.emit()
            self.menuClosed.emit()

    @Slot(bool)
    def setMenuActive(self, active):  # noqa: N802
        """The menu window gained or lost the focus: back in front, it lists
        again."""
        self._menu_active = active
        if active and not self._panel and not self._question:
            self.refresh()

    # --- notices and the OSD ---

    def notify(self, app, summary, body, replaces_id, urgency, expire_ms):
        nid = self.queue.notify(app, summary, body, replaces_id, urgency, expire_ms)
        self._show_notices()
        return nid

    def close_notice(self, nid):
        if self.queue.close(nid):
            self.noticeClosed.emit(nid, notices.CLOSED_BY_CALL)
            self._show_notices()

    def _show_notices(self):
        self.notices_model.show(self.queue.shown())
        wait = self.queue.next_expiry()
        self.notice_timer.stop()
        if wait is not None:
            self.notice_timer.start(int(wait * 1000) + 50)

    def _expire_notices(self):
        for nid in self.queue.expire():
            self.noticeClosed.emit(nid, notices.EXPIRED)
        self._show_notices()

    def show_osd(self, name, value, level):
        self._osd = (name, value, -1 if level is None else int(level))
        self._osd_visible = True
        self.osd_timer.start()
        self.osdChanged.emit()

    def _hide_osd(self):
        self._osd_visible = False
        self.osdChanged.emit()

    # --- for QML: what to show ---

    def _hints(self):
        if self._question:
            pairs = [(inp.NEXT_TAB, "answer"), (inp.ACTIVATE, "choose"), (inp.BACK, "cancel")]
        elif self._panel == "level":
            pairs = [(inp.UP, "change"), (inp.BACK, "done")]
        elif self._panel == "options":
            pairs = [(inp.UP, "move"), (inp.ACTIVATE, "choose"), (inp.BACK, "close")]
        else:
            row = self.current_row()
            pairs = []
            if row is not None and (row.call or row.sheet or row.kind == views.SLIDER or row.confirm):
                pairs.append((inp.ACTIVATE, "open" if self._category == APPS else "choose"))
            if row is not None and row.sheet:
                pairs.append((inp.ACTIONS, "options"))
            if row is not None and row.instance:
                pairs.append((inp.CLOSE, "close"))
            pairs.append((inp.NEXT_TAB, "category"))
            pairs.append((inp.BACK, "back"))
        return [{"button": b, "what": w} for b, w in inp.hint_pairs(pairs, self._device, self._family)]

    def _colors(self):
        return background.colors(self.look, self.now(), self.tokens) or ("", "")

    categories = Property("QVariantList", lambda s: [{"name": n, "icon": i} for n, i in views.CATEGORIES], constant=True)
    pageModels = Property("QVariantList", lambda s: s.pages, constant=True)
    options = Property(QObject, lambda s: s.options_model, constant=True)
    noticeModel = Property(QObject, lambda s: s.notices_model, constant=True)

    category = Property(int, lambda s: s._category, notify=changed)
    section = Property(str, section, notify=changed)
    headersBefore = Property(int, lambda s: s.offset()[0], notify=changed)
    rowsBefore = Property(int, lambda s: s.offset()[1], notify=changed)
    clockTime = Property(str, lambda s: s.now().strftime("%H:%M"), notify=clockChanged)
    clockDate = Property(str, lambda s: s.now().strftime("%a %-d %B"), notify=clockChanged)
    windowed = Property(bool, lambda s: not s.fullscreen, constant=True)
    current = Property(int, lambda s: s.currents[s._category], notify=changed)
    summary = Property(str, lambda s: s.summaries[s._category], notify=changed)
    filter = Property(str, lambda s: s._filter, notify=changed)
    panel = Property(str, lambda s: s._panel, notify=changed)
    panelTitle = Property(str, lambda s: s._panel_row.title if s._panel_row else "", notify=changed)
    option = Property(int, lambda s: s._option, notify=changed)
    levelValue = Property(str, lambda s: s._panel_row.value if s._panel == "level" else "", notify=changed)
    levelLevel = Property(int, lambda s: s._panel_row.level if s._panel == "level" else 0, notify=changed)
    question = Property(str, lambda s: s._question, notify=changed)
    answerYes = Property(bool, lambda s: s._yes, notify=changed)
    message = Property(str, lambda s: s._message, notify=changed)
    messageError = Property(bool, lambda s: s._error, notify=changed)
    hints = Property("QVariantList", _hints, notify=changed)
    menuVisible = Property(bool, lambda s: s._menu_visible, notify=menuVisibleChanged)

    bgTop = Property(str, lambda s: s._colors()[0], notify=backgroundChanged)
    bgBottom = Property(str, lambda s: s._colors()[1], notify=backgroundChanged)
    bgImage = Property(str, lambda s: ("file://" + s.look["image"]) if s.look["mode"] == "image" else "",
                       notify=backgroundChanged)

    osdVisible = Property(bool, lambda s: s._osd_visible, notify=osdChanged)
    osdName = Property(str, lambda s: s._osd[0], notify=osdChanged)
    osdValue = Property(str, lambda s: s._osd[1], notify=osdChanged)
    osdLevel = Property(int, lambda s: s._osd[2], notify=osdChanged)

    def state(self):
        """The UI state as plain data, for tests and debugging."""
        return json.loads(json.dumps({
            "category": self._category, "current": self.currents[self._category], "panel": self._panel,
            "option": self._option, "question": self._question, "yes": self._yes, "filter": self._filter,
            "message": self._message}))
