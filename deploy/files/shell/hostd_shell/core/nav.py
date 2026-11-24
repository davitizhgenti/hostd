"""The navigator: the menu's cursor, categories, folders, service pages, the
filter and the three overlays (options, level, question), with no Qt and no
I/O. An intent changes its state and returns *effects* for the caller to
carry out: fetch a category, send a call to hostd, save the background.

Data comes in through set_rows (what a Refresh effect asked for), so the
navigator never waits for anything: a hung hostd cannot stop it moving.
"""

from dataclasses import dataclass

from hostd_shell import input as inp
from hostd_shell import views
from hostd_shell.core import rowlist

APPS = "apps"

# --- effects: what the caller must do ---------------------------------------


@dataclass(frozen=True)
class Select:
    """Where the cursor lands when a refresh's rows arrive. With none of
    these it stays on the row it was on (by key), else the first."""
    first: bool = False
    app: str = ""  # the row of this app (a service page closed)
    folder: str = ""  # the folder row with this title (a folder closed)


KEEP, FIRST = Select(), Select(first=True)


@dataclass(frozen=True)
class Refresh:
    """List a category again (what to ask for: Navigator.source(key))."""
    key: str
    select: Select = KEEP


@dataclass(frozen=True)
class Send:
    """Send a call to hostd; refresh the current category after it."""
    call: views.Call
    refresh: bool = True


@dataclass(frozen=True)
class SaveBackground:
    """Write the background setting (a LOCAL call), then read it again."""
    body: dict


@dataclass(frozen=True)
class ClearMessage:
    """The message was about what was left; clear it, but not the notice
    that hostd cannot be reached."""


# --- overlays: at most one is open ------------------------------------------


@dataclass(frozen=True)
class Options:
    row: views.Row
    option: int = 0


@dataclass(frozen=True)
class Level:
    row: views.Row


@dataclass(frozen=True)
class Question:
    text: str
    call: views.Call
    yes: bool = False


# --- what a refresh asks for ------------------------------------------------


@dataclass(frozen=True)
class Source:
    """What to fetch for a category: its kind and where in it we are."""
    category: views.Category
    path: tuple = ()
    page_app: str = ""  # a service's page is open: its status, not the list
    tabs: tuple = ()  # the tabs' folders


class Navigator:
    def __init__(self, cats, tabs=()):
        self.cats, self.tabs = list(cats), list(tabs)
        self.category = APPS
        self.filter = ""
        self.overlay = None  # None, Options, Level or Question
        self.rows, self.all_rows, self.summaries = {}, {}, {}  # by category key: shown, all, summary line
        self.currents, self.paths, self.svc_page = {}, {}, {}
        self._sync()

    # --- the categories ---

    def _sync(self):
        """State for every category there is, and none for those gone."""
        keys = [c.key for c in self.cats]
        for k in keys:
            if k not in self.rows:
                self.rows[k], self.all_rows[k], self.summaries[k], self.currents[k] = [], [], "", -1
                self.paths[k] = self.cat(k).root
        for k in [k for k in self.rows if k not in keys]:
            for d in (self.rows, self.all_rows, self.summaries, self.currents, self.paths):
                del d[k]
            self.svc_page.pop(k, None)

    def set_categories(self, cats, tabs):
        """The menu file changed: other tabs. Overlays close; the apps lists
        are asked for again. Returns the effects."""
        self.cats, self.tabs = list(cats), list(tabs)
        self._sync()
        if self.category not in self.rows:
            self.category = APPS
        self.overlay = None
        return [Refresh(c.key) for c in self.cats if c.kind == "apps"]

    def cat(self, key=None):
        key = self.category if key is None else key
        return next(c for c in self.cats if c.key == key)

    def index(self):
        return next(i for i, c in enumerate(self.cats) if c.key == self.category)

    def apps_tab(self):
        return self.cat().kind == "apps"

    def source(self, key):
        """What a Refresh of key asks hostd for."""
        page = self.svc_page.get(key)
        return Source(self.cat(key), self.paths[key], page["app"] if page else "", tuple(t.folder for t in self.tabs))

    # --- data in ---

    def set_rows(self, key, rows, summary="", select=KEEP):
        """The rows of a category arrived (all of them: the filter applies
        here). The cursor stays on its row, or goes where select says."""
        if key not in self.rows:
            return
        self.all_rows[key], self.summaries[key] = list(rows), summary
        self._show(key, select)

    def _show(self, key, select=KEEP):
        old = None if select != KEEP else rowlist.at(self.rows[key], self.currents[key])
        shown = views.visible(self.all_rows[key], self.filter if key == self.category else "")
        self.rows[key] = shown
        if select.app or select.folder:
            i = self._find(shown, select)
        else:
            i = rowlist.index_of(shown, old.key()) if old is not None else -1
        if i < 0 or shown[i].kind == views.HEADER:
            i = rowlist.first(shown)
        self.currents[key] = i
        if isinstance(self.overlay, Level) and key == self.category:
            self.overlay = Level(next((r for r in shown if r.key() == self.overlay.row.key()), self.overlay.row))

    @staticmethod
    def _find(shown, select):
        if select.app:
            return next((j for j, r in enumerate(shown) if r.app == select.app), -1)
        return next((j for j, r in enumerate(shown) if r.kind == views.FOLDER and r.title == select.folder), -1)

    # --- what is current ---

    def page_rows(self):
        return self.rows[self.category]

    def current_row(self):
        row = rowlist.at(self.rows[self.category], self.currents[self.category])
        return row if row is not None and row.kind != views.HEADER else None

    def in_page(self):
        return bool(self.svc_page.get(self.category))

    def in_folder(self):
        return self.apps_tab() and len(self.paths[self.category]) > len(self.cat().root)

    def section(self):
        """In an app tab, the folder open below the tab's own; elsewhere the
        title of the header the current item is under; ""."""
        if self.apps_tab() and not self.in_page():
            path = self.paths[self.category]
            return "" if self.filter or len(path) <= len(self.cat().root) else " / ".join(path)
        rows, i = self.rows[self.category], self.currents[self.category]
        for j in range(i, -1, -1):
            row = rowlist.at(rows, j)
            if row is not None and row.kind == views.HEADER:
                return row.title
        return ""

    def offset(self):
        """(headers, rows) before the current item: where the list scrolls
        to put it at the top (a launcher row is as high as a header)."""
        rows, i = self.rows[self.category], max(0, self.currents[self.category])
        headers = sum(1 for j in range(min(i, len(rows))) if rows[j].kind in (views.HEADER, views.LAUNCHER))
        return headers, max(0, i - headers)

    # --- intents ---

    def handle(self, verb):
        """The one place a verb becomes behaviour. Returns effects."""
        ov = self.overlay
        if isinstance(ov, Question):
            return self._question(ov, verb)
        if isinstance(ov, Level):
            return self._level(ov, verb)
        if isinstance(ov, Options):
            return self._options(ov, verb)
        row = self.current_row()
        if verb in (inp.UP, inp.DOWN):
            self.move(1 if verb == inp.DOWN else -1)
        elif verb in (inp.PAGE_UP, inp.PAGE_DOWN):
            self.move(5 if verb == inp.PAGE_DOWN else -5)
        elif verb == inp.HOME:
            return self.go_home()
        elif verb in (inp.LEFT, inp.PREV_TAB):
            return self.set_category(self.index() - 1)
        elif verb in (inp.RIGHT, inp.NEXT_TAB):
            return self.set_category(self.index() + 1)
        elif verb == inp.ACTIVATE:
            return self.choose(row)
        elif verb == inp.ACTIONS:
            self.open_options(row)
        elif verb == inp.CLOSE and row is not None and row.instance:
            return [Send(views.Call("POST", f"/v1/instances/{row.instance}/close"))]
        elif verb == inp.BACK:
            return self.back()
        return []

    def _question(self, q, verb):
        if verb in (inp.LEFT, inp.RIGHT, inp.UP, inp.DOWN):
            self.overlay = Question(q.text, q.call, verb in (inp.RIGHT, inp.DOWN))
        elif verb == inp.ACTIVATE:
            self.overlay = None
            return self.send(q.call) if q.yes else []
        elif verb == inp.BACK:
            self.overlay = None
        return []

    def _level(self, lv, verb):
        if verb == inp.UP:
            return self.send(lv.row.step)
        if verb == inp.DOWN:
            return self.send(lv.row.step_back)
        if verb in (inp.BACK, inp.ACTIVATE, inp.ACTIONS, inp.LEFT):
            self.overlay = None
        return []

    def _options(self, op, verb):
        n = len(op.row.sheet)
        if verb in (inp.UP, inp.DOWN):
            self.overlay = Options(op.row, max(0, min(n - 1, op.option + (1 if verb == inp.DOWN else -1))))
        elif verb == inp.ACTIVATE:
            row = rowlist.at(op.row.sheet, op.option)
            self.overlay = None
            return self.choose(row)
        elif verb in (inp.BACK, inp.ACTIONS, inp.LEFT):
            self.overlay = None
        return []

    def move(self, steps):
        rows, c = self.rows[self.category], self.category
        i = self.currents[c]
        for _ in range(abs(steps)):
            i = rowlist.step(rows, i, 1 if steps > 0 else -1)
        self.currents[c] = i

    def choose(self, row):
        if row is None:
            return []
        if row.confirm:
            self.overlay = Question(row.confirm, row.call)
        elif row.kind == views.SLIDER:
            self.overlay = Level(row)
        elif row.call is not None:
            return self.send(row.call)
        elif row.sheet:
            self.open_options(row)
        return []

    def open_options(self, row):
        if row is not None and row.sheet:
            self.overlay = Options(row)

    def send(self, call, refresh=True):
        """A call: on this screen alone (LOCAL), or to hostd."""
        if call.method == "LOCAL":
            return self._local(call)
        return [Send(call, refresh)]

    def _local(self, call):
        if call.path == "folder":
            return self.open_folder(tuple(call.body or ()))
        if call.path == "page":
            return self.open_page(call.body or {})
        if call.path == "background":
            return [SaveBackground(call.body)]
        return []

    def set_category(self, c):
        """Goes to a category: by key, or by its place (wrapping around)."""
        if isinstance(c, int):
            c = self.cats[c % len(self.cats)].key
        if c == self.category or c not in self.rows:
            return []
        self.overlay = None
        old, effects = self.category, []
        if self.filter:
            self.filter = ""
            self._show(old, FIRST)
        if self.svc_page.get(old):
            effects += self._close_page(old, came_from=False)
        if self.paths[old] != self.cat(old).root:
            self.paths[old] = self.cat(old).root
            effects.append(Refresh(old, FIRST))
        self.category = c
        return [ClearMessage(), *effects, Refresh(c)]

    def go_home(self):
        """The first category, its first item, with no filter."""
        effects = []
        if self.filter:
            self.filter = ""
            self._show(self.category, FIRST)
        for k in list(self.svc_page):
            effects += self._close_page(k, came_from=False)
        for c in self.cats:
            if self.paths[c.key] != c.root:
                self.paths[c.key] = c.root
                if c.key != APPS:
                    effects.append(Refresh(c.key, FIRST))
        self.category = APPS
        return [*effects, Refresh(APPS, FIRST)]

    def back(self):
        if self.in_page():
            return self._close_page(self.category)
        if self.apps_tab() and self.filter:
            self.set_filter("")
            return []
        if self.in_folder():
            return self.open_folder(self.paths[self.category][:-1])
        if self.category != APPS:
            return self.set_category(APPS)
        return self.send(views.Call("POST", "/v1/windows/back"), refresh=False)

    def open_folder(self, path):
        """Opens a folder in the app tab (its root the top); going up lands
        on the folder it came out of."""
        c = self.category
        came_from = self.paths[c]
        self.paths[c] = path
        select = FIRST
        if len(came_from) > len(path) and came_from[:len(path)] == path:
            select = Select(folder=came_from[len(path)])
        return [Refresh(c, select)]

    def open_page(self, page):
        """A service's page in place of the list."""
        c = self.category
        self.svc_page[c] = {"app": str(page.get("app", "")), "title": str(page.get("title", ""))}
        return [Refresh(c, FIRST)]

    def _close_page(self, c, came_from=True):
        """Back to the list the page was opened from, on the service."""
        page = self.svc_page.pop(c, None)
        if page is None:
            return []
        return [Refresh(c, Select(app=page["app"]) if came_from else FIRST)]

    def set_filter(self, text):
        if text != self.filter:
            self.filter = text
            self._show(self.category, FIRST)

    def click_item(self, i, right=False):
        """A click on a row: it becomes current and is opened (right click:
        its options)."""
        if self.overlay is not None:
            return []
        row = rowlist.at(self.rows[self.category], i)
        if row is None or row.kind == views.HEADER:
            return []
        self.currents[self.category] = i
        if right:
            self.open_options(row)
            return []
        return self.choose(row)

    def click_category(self, c):
        return [] if self.overlay is not None else self.set_category(c)

    def click_option(self, i):
        if isinstance(self.overlay, Options):
            self.overlay = Options(self.overlay.row, i)
            return self.handle(inp.ACTIVATE)
        return []

    def dismiss(self):
        """A click outside a panel or question."""
        self.overlay = None
