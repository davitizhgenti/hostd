"""The menu: categories, their lists, the cursor, the overlays, the
filter, the hints and the status line, for QML.

The logic is the navigator (core.nav) and what it asks for is done here:
lists come through the loader, calls go to hostd, and nothing waits for
either. After each intent the groups of what QML reads are compared with
what they were, and only those that changed are announced."""

import json

from PySide6.QtCore import Property, QObject, QTimer, Signal, Slot
from PySide6.QtQml import QmlElement, QmlUncreatable

from hostd_shell import input as inp
from hostd_shell import menu as menufile
from hostd_shell import views
from hostd_shell.core import context, grid, hints, loader, nav
from hostd_shell.models import RowsModel
from hostd_shell.ui import QML_IMPORT_MAJOR_VERSION, QML_IMPORT_NAME  # noqa: F401 - read by @QmlElement/

APPS = "apps"
SAY_MS, ERROR_MS = 4000, 8000  # how long a message stays
PAGE_MS = 3000  # a service's page asks again this often while it is open
POLL_MS = 5000  # with the event stream down, the current category asks this often
UNREACHABLE = "Cannot reach hostd"


@QmlElement
@QmlUncreatable("The shell makes it.")
class Menu(QObject):
    categoriesChanged = Signal()  # the tabs changed
    cursorChanged = Signal()  # the category, the cursor, the section, the filter, the folder, the focus panel
    overlayChanged = Signal()  # the options panel, the level panel, the question
    hintsChanged = Signal()
    messageChanged = Signal()
    visibleChanged = Signal()
    closed = Signal()  # the menu was hidden: its launcher can end

    def __init__(self, tokens, ask, post, backdrop, now, has_icon=None, menu_path=None, has_tab_icon=None, parent=None):
        """ask(path, callback(sources.Answer)) and post(method, path, body,
        callback(daemon.Result)) are how hostd is reached (asynchronous, or
        answering at once in the tests)."""
        super().__init__(parent)
        self.post, self.backdrop = post, backdrop
        self.has_icon = has_icon or (lambda name: True)
        self.has_tab_icon = has_tab_icon or (lambda name: True)
        self.menu_path = menu_path or menufile.path()
        self.menu_time, self.tabs = None, []
        self.window = context.Window()
        self.mode = hints.Mode()
        self.pointers = None  # set by the shell: a key hides the mouse's pointer
        self.stream_up = lambda: True  # set by the shell: whether the event stream is open
        self.fullscreen = True  # the tests show the menu in a window of a set size
        self._message, self._error = "", False
        self._told, self._options_row = None, None
        self.pending = set()
        self._debounce = QTimer(self, singleShot=True, interval=100, timeout=self._refresh_pending)
        self._say_timer = QTimer(self, singleShot=True, timeout=lambda: self.say(""))
        self.page_timer = QTimer(self, interval=PAGE_MS, timeout=self._refresh_page)
        self._poll_timer = QTimer(self, interval=POLL_MS, timeout=self._poll)
        self.nav = nav.Navigator(views.categories(()))
        self.loader = loader.Loader(self.nav, ask, look=lambda: backdrop.look, tokens=tokens, say=self.say,
                                    has_icon=self.has_icon)
        self.pages, self.options_model = {}, RowsModel(self)
        self._load_menu()
        self.nav.on_rows = self._rows
        self._sync_models()
        self._told = self._snapshot()

    # --- starting ---

    def start(self):
        for c in self.nav.cats:
            self.refresh(c.key)
        self._poll_timer.start()

    # --- the categories ---

    def _load_menu(self):
        """Reads the menu file if it changed: the effects of other tabs, or
        None when they are the same."""
        t = menufile.mtime(self.menu_path)
        if t == self.menu_time:
            return None
        self.menu_time = t
        tabs, problem = menufile.load(self.menu_path, self.has_tab_icon)
        if problem:
            self.say(problem, error=True)
        if tabs == self.tabs:
            return None
        self.tabs = tabs
        return self.nav.set_categories(views.categories(tabs), tabs)

    def _sync_models(self):
        keys = [c.key for c in self.nav.cats]
        for k in keys:
            if k not in self.pages:
                self.pages[k] = RowsModel(self)
        for k in [k for k in self.pages if k not in keys]:
            del self.pages[k]

    def reload_menu(self):
        """Reads the menu file again if it changed, and lists the tabs."""
        effects = self._load_menu()
        if effects is None:
            return
        self._sync_models()
        self.categoriesChanged.emit()
        self._run(effects)

    @Property("QVariantList", notify=categoriesChanged)
    def categories(self):
        return [{"name": c.name, "icon": c.icon} for c in self.nav.cats]

    @Property("QVariantList", notify=categoriesChanged)
    def pageModels(self):  # noqa: N802
        return [self.pages[c.key] for c in self.nav.cats]

    @Property(QObject, constant=True)
    def options(self):
        return self.options_model

    # --- what the screen reads ---

    @Property(int, notify=cursorChanged)
    def category(self):
        return self.nav.index()

    @Property(str, notify=cursorChanged)
    def view(self):
        return "list" if self.nav.in_page() else self.nav.cat().view

    @Property(int, constant=True)
    def columns(self):  # tiles across a wall (Place.tileColumns is held equal by a test)
        return grid.COLUMNS

    @Property("QVariantMap", notify=cursorChanged)
    def focus(self):
        return hints.focus_info(self.nav.current_row())

    @Property(str, notify=cursorChanged)
    def section(self):
        return self.nav.section()

    @Property(int, notify=cursorChanged)
    def headersBefore(self):  # noqa: N802
        return self.nav.offset()[0]

    @Property(int, notify=cursorChanged)
    def rowsBefore(self):  # noqa: N802
        return self.nav.offset()[1]

    @Property(int, notify=cursorChanged)
    def current(self):
        return self.nav.currents[self.nav.category]

    @Property(str, notify=cursorChanged)
    def summary(self):
        return self.nav.summaries[self.nav.category]

    @Property(str, notify=cursorChanged)
    def filter(self):
        return self.nav.filter

    @Property("QVariantList", notify=cursorChanged)
    def folderPath(self):  # noqa: N802
        return list(self.nav.paths[self.nav.category])

    @Property(str, notify=overlayChanged)
    def panel(self):
        ov = self.nav.overlay
        return "options" if isinstance(ov, nav.Options) else "level" if isinstance(ov, nav.Level) else ""

    @Property(str, notify=overlayChanged)
    def panelTitle(self):  # noqa: N802
        ov = self.nav.overlay
        return ov.row.title if isinstance(ov, (nav.Options, nav.Level)) else ""

    @Property(int, notify=overlayChanged)
    def option(self):
        ov = self.nav.overlay
        return ov.option if isinstance(ov, nav.Options) else 0

    @Property(str, notify=overlayChanged)
    def levelValue(self):  # noqa: N802
        ov = self.nav.overlay
        return ov.row.value if isinstance(ov, nav.Level) else ""

    @Property(int, notify=overlayChanged)
    def levelLevel(self):  # noqa: N802
        ov = self.nav.overlay
        return ov.row.level if isinstance(ov, nav.Level) else 0

    @Property(str, notify=overlayChanged)
    def question(self):
        ov = self.nav.overlay
        return ov.text if isinstance(ov, nav.Question) else ""

    @Property(bool, notify=overlayChanged)
    def answerYes(self):  # noqa: N802
        ov = self.nav.overlay
        return ov.yes if isinstance(ov, nav.Question) else False

    @Property(str, notify=messageChanged)
    def message(self):
        return self._message

    @Property(bool, notify=messageChanged)
    def messageError(self):  # noqa: N802
        return self._error

    @Property("QVariantList", notify=hintsChanged)
    def hintList(self):  # noqa: N802
        return hints.hints(self.nav, self.mode)

    @Property(bool, notify=visibleChanged)
    def visible(self):
        return self.window.visible

    @Property(bool, constant=True)
    def windowed(self):
        return not self.fullscreen

    # --- what the shell and the tests ask ---

    def page(self):
        return self.pages[self.nav.category]

    def current_row(self):
        return self.nav.current_row()

    def offset(self):
        """(headers, rows) before the current item."""
        return self.nav.offset()

    def in_page(self):
        return self.nav.in_page()

    def in_folder(self):
        return self.nav.in_folder()

    def apps_tab(self):
        return self.nav.apps_tab()

    def cat(self, key=None):
        return self.nav.cat(key)

    def index(self):
        return self.nav.index()

    def state(self):
        """The UI state as plain data, for tests and debugging."""
        return json.loads(json.dumps({
            "category": self.nav.category, "current": self.nav.currents[self.nav.category], "panel": self.panel,
            "option": self.option, "question": self.question, "yes": self.answerYes, "filter": self.nav.filter,
            "message": self._message}))

    def refresh(self, category=None, keep=True):
        key = self.nav.category if category is None else category
        self._run([nav.Refresh(key, nav.KEEP if keep else nav.FIRST)])

    # --- intents ---

    @Slot(str)
    def handle(self, verb):
        """The one place input turns into behaviour."""
        self._run(self.nav.handle(verb))

    @Slot(str, str, bool, result=bool)
    def key(self, name, text, shift):
        """A key on the menu: a verb, or typing into the Apps filter.
        Whether the menu took it."""
        self.use("keys")
        if self.pointers is not None:
            self.pointers.hide_mouse()
        filtering = self.nav.apps_tab() and self.nav.overlay is None
        verb = inp.verb_for_key(name, shift, typing=filtering and bool(self.nav.filter))
        if verb is None:
            if filtering and name == "Key_Backspace":
                self.set_filter(self.nav.filter[:-1])
                return True
            if filtering and text and text.isprintable():
                self.set_filter(self.nav.filter + text)
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

    @Slot(int, str)
    def clickItem(self, i, button="left"):  # noqa: N802 - called from QML
        """A click on a row: it becomes current and is opened (a right click:
        its options)."""
        self._run(self.nav.click_item(i, right=button == "right"))

    @Slot(int)
    def clickCategory(self, c):  # noqa: N802
        self._run(self.nav.click_category(c))

    @Slot(int)
    def clickOption(self, i):  # noqa: N802
        self._run(self.nav.click_option(i))

    @Slot(bool)
    def answer(self, yes):
        """The question is answered: yes or no."""
        if isinstance(self.nav.overlay, nav.Question):
            self.handle(inp.RIGHT if yes else inp.LEFT)
            self.handle(inp.ACTIVATE)

    @Slot()
    def dismiss(self):
        """A click outside a panel or question."""
        self.nav.dismiss()
        self._notify()

    @Slot(int, int)
    def wheel(self, dx, dy):
        """The wheel moves items; a horizontal wheel or tilt changes category."""
        self.use("keys")
        if abs(dx) > abs(dy):
            self.handle(inp.NEXT_TAB if dx < 0 else inp.PREV_TAB)
        elif dy:
            self.handle(inp.UP if dy > 0 else inp.DOWN)

    @Slot(str)
    def mouseButton(self, button):  # noqa: N802 - called from QML
        """A click that is on no row: the middle and side buttons, and the
        right button (options of what is current)."""
        self.use("keys")
        hover = self.pointers.mouse_hover() if self.pointers is not None else -1
        if button == "right" and hover >= 0 and self.nav.overlay is None:
            self.clickItem(hover, "right")  # the options of the row under the pointer
            return
        verb = {"right": inp.ACTIONS, "middle": inp.HOME, "back": inp.BACK, "forward": inp.NEXT_TAB}.get(button)
        if verb:
            self.handle(verb)

    def set_filter(self, text):
        self.nav.set_filter(text)
        self._notify()

    def use(self, device, family=None):
        """The input in use is device (keys or controller), of this family."""
        changed = self.mode.use(device)
        if family is not None:
            changed = self.mode.use_family(family) or changed
        if changed:
            self._notify()

    # --- the window ---

    @Slot()
    def showMenu(self):  # noqa: N802
        if self.window.show():
            if self.pointers is not None:
                self.pointers.reset_mouse()
            self.visibleChanged.emit()
        self.reload_menu()
        self.refresh()

    @Slot()
    def hideMenu(self):  # noqa: N802
        if self.window.hide():
            self.nav.dismiss()
            self._notify()
            self.visibleChanged.emit()
            self.closed.emit()

    @Slot(bool)
    def setMenuActive(self, active):  # noqa: N802
        """The menu window gained or lost the focus: back in front, it lists
        again."""
        self.window.set_active(active)
        if active and self.nav.overlay is None:
            self.refresh()

    # --- data ---

    def on_event(self, event_type):
        """An event: what it made stale is listed again, after a moment for
        the rest of a burst."""
        self.pending |= set(self.loader.on_event(event_type, check_busy=False))
        if self.pending and not self._debounce.isActive():
            self._debounce.start()

    def _refresh_pending(self):
        busy = self.loader.busy()
        for c in sorted(self.pending):
            if c in self.nav.rows and not (busy and c == self.nav.category):
                self.refresh(c)
        self.pending.clear()

    def _poll(self):
        if not self.stream_up():
            self.refresh()  # the event stream is down: ask every few seconds

    def _refresh_page(self):
        c = self.nav.category
        if self.nav.svc_page.get(c) and self.nav.overlay is None and self.window.visible:
            self.refresh(c)

    # --- the status line ---

    @Slot(str, bool)
    def say(self, text, error=False):
        """A message at the bottom left, for a few seconds."""
        self._message, self._error = text, error
        if text:
            self._say_timer.start(ERROR_MS if error else SAY_MS)
        else:
            self._say_timer.stop()
        self._notify()

    # --- carrying out what the navigator asks ---

    def _run(self, effects):
        for e in self.loader.run(effects):
            if isinstance(e, nav.Send):
                self._send(e)
            elif isinstance(e, nav.SaveBackground):
                self._save_background(e.body)
            elif isinstance(e, nav.ClearMessage) and self._message and not self._message.startswith(UNREACHABLE):
                self.say("")  # it was about the category left
        self._notify()

    def _send(self, e):
        self.post(e.call.method, e.call.path, e.call.body, lambda r, refresh=e.refresh: self._sent(r, refresh))

    def _sent(self, result, refresh):
        if not result.ok:
            self.say(result.message, error=True)
            return
        text = views.result_text(result.payload)
        self.say(text, error=bool(text))
        if refresh:
            QTimer.singleShot(150, lambda: self.refresh())

    def _save_background(self, setting):
        try:
            self.backdrop.save(setting)
        except OSError as e:
            self.say(f"Cannot save the background: {e}", error=True)
            return
        self.look_changed()

    def look_changed(self):
        """The background setting or the menu file changed on disk."""
        if self.backdrop.reload():
            self.refresh("settings")
        self.reload_menu()

    # --- telling the screen ---

    def _rows(self, key):
        """The navigator set the rows of a category."""
        self.pages[key].set_rows(self.nav.rows[key])
        if key == self.nav.category:
            self._notify()

    def _snapshot(self):
        n = self.nav
        return {"cursor": (n.category, n.currents[n.category], n.section(), n.offset(), hints.focus_info(n.current_row()),
                           self.view, n.filter, tuple(n.paths[n.category]), n.summaries[n.category]),
                "overlay": (self.panel, self.panelTitle, self.option, self.levelValue, self.levelLevel, self.question,
                            self.answerYes),
                "hints": hints.hints(n, self.mode), "message": (self._message, self._error)}

    def _notify(self):
        ov = self.nav.overlay
        if isinstance(ov, nav.Options):
            if self._options_row is not ov.row:
                self._options_row = ov.row
                self.options_model.set_rows(ov.row.sheet)
        else:
            self._options_row = None
        now, told = self._snapshot(), self._told or {}
        self._told = now
        pages_open = any(self.nav.svc_page.values())
        if pages_open and not self.page_timer.isActive():
            self.page_timer.start()
        elif not pages_open and self.page_timer.isActive():
            self.page_timer.stop()
        for name, signal in (("cursor", self.cursorChanged), ("overlay", self.overlayChanged), ("hints", self.hintsChanged),
                             ("message", self.messageChanged)):
            if now[name] != told[name]:
                signal.emit()
