"""What each screen lists, from what hostd reports: plain data, no GTK.

A page is a list of Rows. A row says what it shows and what choosing it
does (a Call: an HTTP request to hostd). The menu draws the rows with the
components; the tests check them without a screen.
"""

from dataclasses import dataclass, field

from hostd_shell import background

MENU_ID = "hostd-menu"  # the menu's own app: never listed

SERVICES = "Services"  # the folder of background apps that name none


@dataclass(frozen=True)
class Category:
    """One of the menu's categories. An app tab (kind "apps") lists the
    apps in the folder root: () is the Apps category, ("Games",) a tab the
    menu file marks."""
    key: str
    name: str
    icon: str
    kind: str
    root: tuple = ()
    view: str = "list"  # an app tab's: list, or tiles


APPS_CATEGORY = Category("apps", "Apps", "layout-grid", "apps")
FIXED = (Category("users", "Users", "users", "users"),
         Category("settings", "Settings", "sliders-horizontal", "settings"),
         Category("status", "Status", "activity", "status"))


def categories(tabs=()):
    """The categories, left to right: Apps, the marked folders' tabs
    (menu.Tab, in the menu file's order), Users, Settings and Status."""
    return [APPS_CATEGORY, *(Category("tab:" + t.folder, t.folder, t.icon, "apps", (t.folder,), t.view)
                             for t in tabs), *FIXED]


@dataclass(frozen=True)
class Call:
    """A request to hostd: method, path and JSON body. Method LOCAL is a
    change on this screen alone (the background), done by the menu."""
    method: str
    path: str
    body: dict | None = None


def local(what, value):
    return Call("LOCAL", what, value)


def action(kind, **args):
    """An action sent by type (POST /v1/actions)."""
    return Call("POST", "/v1/actions", {"type": kind, "args": args})


# Row kinds: how a row is drawn and what left/right do on it.
HEADER, ITEM, SLIDER, TOGGLE, FOLDER = "header", "item", "slider", "toggle", "folder"
LAUNCHER = "launcher"  # a tiles tab's group heading that is an app: choosing it opens it


@dataclass
class Row:
    title: str
    kind: str = ITEM
    subtitle: str = ""  # a second line, muted
    value: str = ""  # on the right: a state, a count
    badge: str = ""  # a short highlighted word: "running", "on screen"
    tone: str = ""  # the badge's color: positive, warning, danger, or accent
    mark: str = ""  # a small square in a tone: a running app
    call: Call | None = None  # what choosing it does
    confirm: str = ""  # asked first ("Switch the screen to guest?")
    sheet: list = field(default_factory=list)  # rows of its actions sheet
    instance: str = ""  # a running app's instance: what Delete closes
    app: str = ""
    under: bool = False  # an app listed under another: shown only when filtering
    # Sliders: the value now, and what left and right send.
    level: int = 0
    step: Call | None = None  # up
    step_back: Call | None = None  # down
    icon: str = ""  # hostd's icon (icons/NAME.svg)
    app_icon: str = ""  # the app's own icon, preferred to icon
    tile: str = ""  # the app's wide picture (a path), on its tile in a tiles tab
    banner: str = ""  # its banner (a path), beside the current tile
    details: str = ""  # a line or two about it, under the banner

    def key(self):
        """Its identity across refreshes, to keep the selection: an app's
        row is the same whether it runs or not."""
        return (self.kind, self.app or self.instance, self.title)

    def matches(self, text):
        """Whether a filter's words are all in it."""
        hay = " ".join((self.title, self.subtitle, self.value, self.app, self.instance)).lower()
        return all(w in hay for w in text.lower().split())


def visible(rows, text):
    """The rows a filter shows: with no text, all but the nested apps; with
    text, the matching ones (in any folder), headers and folders left out."""
    text = text.strip()
    if not text:
        return [r for r in rows if not r.under]
    return [r for r in rows if r.kind not in (HEADER, FOLDER) and r.matches(text)]


# --- Apps ------------------------------------------------------------------


def plural(n, word):
    return f"{n} {word}{'' if n == 1 else 's'}"


def folder_of(app):
    """An app's folder as a path. A background app that names none is in
    Services."""
    path = tuple(n for n in (app.get("folder") or "").split("/") if n)
    if not path and app.get("surface") == "background":
        return (SERVICES,)
    return path


def folder_row(path, apps):
    """A folder as a row: choosing it opens it (the menu's own change)."""
    n = sum(1 for a in apps if folder_of(a)[:len(path)] == path)
    return Row(path[-1], FOLDER, icon="folder", value=plural(n, "app"), call=local("folder", list(path)))


LIVE = ("starting", "running", "stopping")
# A state's word and color: a window app shows the color alone (a mark), a
# service the word.
TONES = {"starting": "warning", "stopping": "warning", "running": "positive", "failed": "danger"}


def apps_page(instances, apps, path=(), root=(), tabs=(), flat=False):
    """An app tab's rows: the folder open (path; root is the tab's own
    folder, () for Apps) as its subfolders and its apps, running apps first.
    A running app is its own row, marked: there is no separate list of
    them. Apps under another (Steam's games) are in its options, unless
    they run. Every other app is a hidden row, shown when filtering. Apps
    leaves out the folders that are tabs (tabs: their names).

    flat (a tiles tab): every app anywhere in the tab, no folders, in
    groups by launcher (tiles_rows).

    instances: /v1/instances?all=true (an ended one tells a failed service)."""
    path = tuple(path) if tuple(path)[:len(root)] == tuple(root) else tuple(root)
    root, tabs = tuple(root), set(tabs)
    listed = [a for a in apps if a.get("id") != MENU_ID and a.get("surface") in ("window", "background")]
    by_id = {a["id"]: a for a in listed}
    live, ended = {}, {}
    for inst in instances or []:
        app = inst.get("app") or ""
        if inst.get("state") in LIVE:
            live.setdefault(app, []).append(inst)
        else:
            ended[app] = inst  # the latest last
    children = {}
    for a in listed:
        if a.get("under"):
            children.setdefault(a["under"], []).append(a)

    def place(a):
        """Where an app is listed; None when only in another's options."""
        if a.get("under") and not a.get("folder"):
            if a["id"] not in live:
                return None
            return folder_of(by_id.get(a["under"]) or {})  # a running game, next to its launcher
        return folder_of(a)

    def mine(folder):
        """Whether a folder is this tab's: in its root, and, in Apps, not a tab."""
        if folder[:len(root)] != root:
            return False
        return bool(root) or not (folder and folder[0] in tabs)

    placed = [(a, place(a)) for a in listed]
    if flat:
        here = [a for a, f in placed if f is not None and mine(f)]
        rows = tiles_rows(here, by_id, live, ended, children, root)
        shown_ids = {r.app for r in rows}
        deeper = []
    else:
        here = [a for a, f in placed if f == path]
        here.sort(key=lambda a: a["id"] not in live)  # running first, else the catalog's order
        deeper = [a for a, f in placed if f is not None and mine(f) and f[:len(path)] == path and len(f) > len(path)]
    subfolders = sorted({folder_of(a)[:len(path) + 1] for a in deeper})
    if not flat:
        shown_ids = {a["id"] for a in here}
        rows = []
    if not root and not path:
        # Instances of no app in the catalog (app.run commands).
        for inst in (i for app, insts in live.items() if app not in by_id for i in insts):
            if inst.get("surface") == "window" and inst.get("app") != MENU_ID:
                rows.append(instance_row(inst))
    folders = [folder_row(sub, [a for a, f in placed if f is not None and mine(f)]) for sub in subfolders]
    running_here = sum(1 for a in here if a["id"] in live)
    for n, a in enumerate([] if flat else here):
        if n == running_here:
            rows += folders  # after the running apps, before the rest
        if a.get("surface") == "background":
            rows.append(service_row(a, live.get(a["id"], []), ended.get(a["id"])))
        else:
            rows.append(app_row(a, live.get(a["id"], []), children))
    if not flat and running_here == len(here):
        rows += folders
    # Shown only when filtering: every other app, wherever it is.
    for a in listed:
        if a["id"] in shown_ids:
            continue
        if a.get("under") and not a.get("folder"):
            value = (by_id.get(a["under"]) or {}).get("name") or a["under"]
        else:
            value = " / ".join(folder_of(a))
        if a.get("surface") == "background":
            row = service_row(a, live.get(a["id"], []), ended.get(a["id"]))
        else:
            row = app_row(a, live.get(a["id"], []), children)
        row.value, row.under = value or row.value, True
        rows.append(row)
    running = sum(1 for a in listed if a["id"] in live and mine(place(a) or ()) and place(a) is not None
                  and a.get("surface") == "window")
    counted = [a for a, f in placed if f is not None and mine(f) and not (a.get("under") and not a.get("folder"))]
    services = sum(1 for a in counted if a.get("surface") == "background")
    apps_n = len(counted) - services
    parts = (f"{running} running" if running else "", plural(apps_n, "app") if apps_n or not services else "",
             plural(services, "service") if services else "")
    return rows, "   ".join(x for x in parts if x)


def launcher_of(app, by_id):
    """The app an app is started through: the one it is under, else the
    first one it requires that is listed (Dolphin, for a Wii game)."""
    if app.get("under") in by_id:
        return app["under"]
    return next((r for r in app.get("requires") or [] if r in by_id), "")


def tiles_rows(members, by_id, live, ended, children, root):
    """A tiles tab's rows: a group per launcher, by its name, each headed
    by the launcher itself (a LAUNCHER row: choosing it opens it) and then
    its apps, running first; then the apps with no launcher under a label.
    A game's second line is its state alone: its group says where it is
    from."""
    groups = {}
    for a in members:
        lid = launcher_of(a, by_id)
        if lid:
            groups.setdefault(lid, []).append(a)
    launchers = set(groups)
    loose = [a for a in members if not launcher_of(a, by_id) and a["id"] not in launchers]
    noun = root[-1].lower().rstrip("s") if root else "app"  # Games: "3 games"

    def row(a):
        if a.get("surface") == "background":
            r = service_row(a, live.get(a["id"], []), ended.get(a["id"]))
        else:
            r = app_row(a, live.get(a["id"], []), children)
            r.subtitle = {"positive": "running", "warning": "starting", "danger": "failed"}.get(r.mark, "")
        return r

    def running_first(apps):
        return sorted(apps, key=lambda a: a["id"] not in live)

    rows = []
    for lid in sorted(groups, key=lambda i: (by_id[i].get("name") or i).lower()):
        head = row(by_id[lid])
        head.kind, head.value, head.subtitle = LAUNCHER, plural(len(groups[lid]), noun), ""
        rows.append(head)
        rows += [row(a) for a in running_first(groups[lid])]
    if loose:
        rows.append(Row(f"Other {noun}s" if root else "Other apps", HEADER))
        rows += [row(a) for a in running_first(loose)]
    return rows


def app_row(app, running, children):
    """A window app. Running: marked, choosing it brings it to the front,
    Delete closes it; its options are each instance's, then the app's."""
    opening = open_call(app["id"])
    row = Row(app.get("name") or app["id"], call=opening, app=app["id"],
              icon="app-window", app_icon=app.get("icon") or "",
              tile=app.get("tile") or "", banner=app.get("banner") or "", details=app.get("details") or "")
    if not running:
        row.sheet = [Row("Start", call=opening, icon="play")] + app_sheet(app, children)
        return row
    first = running[0]
    row.mark = TONES.get(first.get("state"), "positive")
    row.call = switch_call(first["id"])
    row.instance = first["id"]
    row.sheet = []
    for inst in running:
        which = f"  {inst['id']}" if len(running) > 1 else ""
        row.sheet += [Row("Bring to front" + which, call=switch_call(inst["id"]), icon="arrow-up-right"),
                      Row("Close" + which, call=Call("POST", f"/v1/instances/{inst['id']}/close"), tone="danger", icon="x")]
    row.sheet += app_sheet(app, children)
    return row


def instance_row(inst):
    """A running instance of no app in the catalog."""
    switch = switch_call(inst["id"])
    return Row(inst.get("name") or inst["id"], call=switch, instance=inst["id"], app=inst.get("app") or "",
               mark=TONES.get(inst.get("state"), "positive"), icon="app-window",
               sheet=[Row("Bring to front", call=switch, icon="arrow-up-right"),
                      Row("Close", call=Call("POST", f"/v1/instances/{inst['id']}/close"), tone="danger", icon="x")])


def service_row(app, running, last):
    """A background app: its state in a word; choosing it opens its options,
    so nothing starts or stops on one press."""
    if running:
        inst = running[0]
        state = inst.get("state") or "running"
        sheet = [Row("Stop", call=Call("POST", f"/v1/instances/{inst['id']}/close"), tone="danger", icon="square"),
                 Row("Restart", call=Call("POST", f"/v1/instances/{inst['id']}/restart"), icon="rotate-ccw")]
    else:
        state = "failed" if last and last.get("state") == "failed" else "stopped"
        sheet = [Row("Start", call=open_call(app["id"]), icon="play")]
    return Row(app.get("name") or app["id"], app=app["id"], badge=state, tone=TONES.get(state, ""),
               icon="server", app_icon=app.get("icon") or "", sheet=sheet,
               tile=app.get("tile") or "", banner=app.get("banner") or "", details=app.get("details") or "")


def switch_call(instance):
    return Call("POST", f"/v1/instances/{instance}/switch")


def open_call(app_id, action_id=None):
    return Call("POST", f"/v1/apps/{app_id}/open", {"action": action_id} if action_id else None)


def app_sheet(app, children):
    """An app's own actions, then the apps under it."""
    rows = [Row(a["name"], call=open_call(app["id"], a["id"]), icon="play")
            for a in app.get("actions") or []]
    rows += [Row(c.get("name") or c["id"], call=open_call(c["id"]), app=c["id"],
                 icon="app-window", app_icon=c.get("icon") or "")
             for c in children.get(app.get("id"), [])]
    return rows


# --- Users -------------------------------------------------------------------


def users_page(seats):
    """Who may hold the screen; choosing one gives it to them, after asking."""
    rows = [Row("Users", HEADER)]
    for s in seats or []:
        user = s.get("user", "")
        holds = bool(s.get("holds"))
        rows.append(Row(user, subtitle=f"screen {s.get('vt', '?')}", icon="user",
                        badge="on screen" if holds else "", tone="accent" if holds else "",
                        call=None if holds else action("seat.switch", user=user),
                        confirm="" if holds else f"Give the screen, keyboard and controllers to {user}?"))
    if len(rows) == 1:
        rows.append(Row("Only one user can use this screen", subtitle="hostd-admin seats add USER VT adds another",
                        icon="info"))
    return rows


# --- Settings ----------------------------------------------------------------


def mode_text(m):
    hz = m.get("refresh") or 0
    return f"{m.get('width')}×{m.get('height')}" + (f" @ {hz:g} Hz" if hz else "")


def mode_arg(m):
    hz = m.get("refresh") or 0
    return f"{m.get('width')}x{m.get('height')}" + (f"@{hz:g}" if hz else "")


def settings_page(audio, display, look=None, tokens=None):
    """Sound, the screen, and the background (look: background.load()'s
    setting; tokens: the design tokens, for its colors)."""
    rows = [Row("Sound", HEADER)]
    if audio and audio.get("available", True):
        pct = int(audio.get("percent") or 0)
        muted = bool(audio.get("muted"))
        rows.append(Row("Volume", SLIDER, value=f"{pct}%", level=pct,
                        step=action("audio.volume.set", percent="+5"),
                        step_back=action("audio.volume.set", percent="-5"),
                        badge="muted" if muted else "", tone="warning" if muted else "",
                        icon="volume-x" if muted else "volume-2"))
        rows.append(Row("Mute", TOGGLE, value="on" if muted else "off",
                        call=action("audio.mute.set", muted="toggle"), icon="volume-x"))
        outputs = audio.get("outputs") or []
        if len(outputs) > 1:
            current = next((o for o in outputs if o.get("default")), {})
            rows.append(Row("Output", value=current.get("description") or current.get("name", ""), icon="speaker",
                            sheet=[Row(o.get("description") or o["name"], subtitle=o["name"],
                                       badge="in use" if o.get("default") else "", tone="accent" if o.get("default") else "",
                                       call=action("audio.output.set", output=o["name"]))
                                   for o in outputs]))
    else:
        rows.append(Row("No sound server", subtitle="PipeWire is not running", icon="info"))

    rows.append(Row("Screen", HEADER))
    outputs = [o for o in (display or {}).get("outputs") or [] if o.get("active")]
    for o in outputs:
        now = {"width": o.get("width"), "height": o.get("height"), "refresh": o.get("refresh")}
        # The largest first, and the fastest of each size.
        modes = sorted(o.get("modes") or [],
                       key=lambda m: (-(m.get("width") or 0) * (m.get("height") or 0), -(m.get("refresh") or 0)))
        name = " ".join(x for x in (o.get("make"), o.get("model")) if x) or o.get("name", "")
        rows.append(Row("Resolution", subtitle=f"{name} ({o.get('name', '')})" if len(outputs) > 1 or name else "",
                        value=mode_text(now), icon="monitor",
                        sheet=[Row(mode_text(m), badge="now" if mode_text(m) == mode_text(now) else "", tone="accent",
                                   call=action("display.mode", output=o["name"], mode=mode_arg(m)))
                               for m in modes]))
    if not outputs:
        rows.append(Row("No screen attached", subtitle="Sway is not running", icon="info"))
    else:
        rows.append(Row("Turn the screen off", call=action("display.power", on=False), icon="power",
                        confirm="Turn the screen off? Any key or button turns it on again."))
    if look is not None and tokens is not None:
        options = background.choices(look, tokens)
        now = next((t for t, s in options if s["mode"] == look["mode"]
                    and (look["mode"] != "color" or s["color"] == look["color"])), background.describe(look))
        rows.append(Row("Background", value=now, icon="palette",
                        sheet=[Row(t, call=local("background", s), icon="image" if s["mode"] == "image" else "palette",
                                   badge="now" if t == now else "", tone="accent")
                               for t, s in options]))
    return rows


# --- Status ------------------------------------------------------------------


def gib(n):
    return f"{n / (1 << 30):.1f} GB"


def status_page(version, display, log):
    rows = [Row("hostd", HEADER)]
    v = version or {}
    rows.append(Row("Version", value=v.get("version", "unknown"), icon="info",
                    subtitle=f"before: {v['previous']}" if v.get("previous") else ""))
    gpu = (display or {}).get("gpu_memory") or {}
    if gpu.get("total"):
        used = gpu.get("used", 0) / gpu["total"]
        rows.append(Row("Video memory", value=f"{gib(gpu.get('used', 0))} of {gib(gpu['total'])}", icon="memory-stick",
                        badge="nearly full" if used > 0.9 else "", tone="warning"))
    rows.append(Row("Recent", HEADER))
    for e in (log or [])[:12]:
        status = e.get("status", "")
        src = e.get("source") or {}
        who = src.get("name") or src.get("kind") or ""
        rows.append(Row(" ".join(x for x in (e.get("type", ""), e.get("summary", "")) if x),
                        subtitle=f"{(e.get('time') or '')[11:19]}  {who}".strip(), icon="history",
                        badge="" if status == "applied" else status,
                        tone={"failed": "danger", "skipped": "warning"}.get(status, "")))
    if not log:
        rows.append(Row("Nothing done yet", icon="history"))
    return rows


# --- results -----------------------------------------------------------------


def result_text(res):
    """What to say about an action's result: "" when it simply worked."""
    if not isinstance(res, dict):
        return ""
    if res.get("status") == "skipped":
        return "Skipped: " + (res.get("reason") or "nothing to do")
    return ""
