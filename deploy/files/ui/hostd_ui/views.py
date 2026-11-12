"""What each screen lists, from what hostd reports: plain data, no GTK.

A page is a list of Rows. A row says what it shows and what choosing it
does (a Call: an HTTP request to hostd). The menu draws the rows with the
components; the tests check them without a screen.
"""

from dataclasses import dataclass, field

MENU_ID = "hostd-menu"  # the menu's own app: never listed


@dataclass(frozen=True)
class Call:
    """A request to hostd: method, path and JSON body."""
    method: str
    path: str
    body: dict | None = None


def action(kind, **args):
    """An action sent by type (POST /v1/actions)."""
    return Call("POST", "/v1/actions", {"type": kind, "args": args})


# Row kinds: how a row is drawn and what left/right do on it.
HEADER, ITEM, SLIDER, TOGGLE = "header", "item", "slider", "toggle"


@dataclass
class Row:
    title: str
    kind: str = ITEM
    subtitle: str = ""  # a second line, muted
    value: str = ""  # on the right: a state, a count
    badge: str = ""  # a short highlighted word: "running", "on screen"
    tone: str = ""  # the badge's color: positive, warning, danger, or accent
    call: Call | None = None  # what choosing it does
    confirm: str = ""  # asked first ("Switch the screen to guest?")
    sheet: list = field(default_factory=list)  # rows of its actions sheet
    instance: str = ""  # a running app's instance: what Delete closes
    app: str = ""
    under: bool = False  # an app listed under another: shown only when filtering
    # Sliders: the value now, and what left and right send.
    level: int = 0
    step: Call | None = None  # right
    step_back: Call | None = None  # left

    def key(self):
        """Its identity across refreshes, to keep the selection."""
        return (self.kind, self.instance, self.app, self.title)

    def matches(self, text):
        """Whether a filter's words are all in it."""
        hay = " ".join((self.title, self.subtitle, self.value, self.app, self.instance)).lower()
        return all(w in hay for w in text.lower().split())


def visible(rows, text):
    """The rows a filter shows: with no text, all but the nested apps; with
    text, the matching ones, headers left out."""
    text = text.strip()
    if not text:
        return [r for r in rows if not r.under]
    return [r for r in rows if r.kind != HEADER and r.matches(text)]


# --- Apps ------------------------------------------------------------------


def plural(n, word):
    return f"{n} {word}{'' if n == 1 else 's'}"


def apps_page(instances, apps):
    """Running apps, then the catalog; apps under another (Steam's games)
    are in its sheet, and in the list only when filtering."""
    running = [i for i in instances
               if i.get("app") != MENU_ID and i.get("surface") == "window"
               and i.get("state") in ("starting", "running")]
    listed = [a for a in apps if a.get("id") != MENU_ID and a.get("surface") == "window"]
    by_id = {a["id"]: a for a in listed}
    children = {}
    for a in listed:
        if a.get("under"):
            children.setdefault(a["under"], []).append(a)
    top = [a for a in listed if not a.get("under")]

    rows = []
    if running:
        rows.append(Row("Running", HEADER))
    for inst in running:
        app = by_id.get(inst.get("app")) or {}
        switch = Call("POST", f"/v1/instances/{inst['id']}/switch")
        rows.append(Row(inst.get("name") or inst["id"], subtitle=inst["id"],
                        badge="starting" if inst.get("state") == "starting" else "running",
                        tone="warning" if inst.get("state") == "starting" else "positive",
                        value=inst.get("action") or "", call=switch,
                        sheet=instance_sheet(inst, app, children, switch),
                        instance=inst["id"], app=inst.get("app") or ""))
    rows.append(Row("Apps", HEADER))
    for a in top:
        more = []
        if a.get("actions"):
            more.append(plural(len(a["actions"]), "action"))
        if children.get(a["id"]):
            more.append(plural(len(children[a["id"]]), "app"))
        rows.append(app_row(a, "  ".join(more), children))
    for a in listed:
        if a.get("under"):
            parent = (by_id.get(a["under"]) or {}).get("name") or a["under"]
            row = app_row(a, parent, children)
            row.under = True
            rows.append(row)
    return rows, f"{len(running)} running   {plural(len(top), 'app')}"


def app_row(app, value, children):
    opening = open_call(app["id"])
    return Row(app.get("name") or app["id"], subtitle=app["id"], value=value, call=opening,
               sheet=[Row("Start", call=opening)] + app_sheet(app, children), app=app["id"])


def open_call(app_id, action_id=None):
    return Call("POST", f"/v1/apps/{app_id}/open", {"action": action_id} if action_id else None)


def app_sheet(app, children):
    """An app's own actions, then the apps under it."""
    rows = [Row(a["name"], subtitle=a["id"], call=open_call(app["id"], a["id"])) for a in app.get("actions") or []]
    rows += [Row(c.get("name") or c["id"], subtitle=c["id"], call=open_call(c["id"]), app=c["id"])
             for c in children.get(app.get("id"), [])]
    return rows


def instance_sheet(inst, app, children, switch):
    return [Row("Bring to front", call=switch),
            Row("Close", call=Call("POST", f"/v1/instances/{inst['id']}/close"), tone="danger")] + app_sheet(app, children)


# --- Users -------------------------------------------------------------------


def users_page(seats):
    """Who may hold the screen; choosing one gives it to them, after asking."""
    rows = [Row("Users", HEADER)]
    for s in seats or []:
        user = s.get("user", "")
        holds = bool(s.get("holds"))
        rows.append(Row(user, subtitle=f"screen {s.get('vt', '?')}",
                        badge="on screen" if holds else "", tone="accent" if holds else "",
                        call=None if holds else action("seat.switch", user=user),
                        confirm="" if holds else f"Give the screen, keyboard and controllers to {user}?"))
    if len(rows) == 1:
        rows.append(Row("Only one user can use this screen", subtitle="hostd-admin seats add USER VT adds another"))
    return rows


# --- Settings ----------------------------------------------------------------


def mode_text(m):
    hz = m.get("refresh") or 0
    return f"{m.get('width')}×{m.get('height')}" + (f" @ {hz:g} Hz" if hz else "")


def mode_arg(m):
    hz = m.get("refresh") or 0
    return f"{m.get('width')}x{m.get('height')}" + (f"@{hz:g}" if hz else "")


def settings_page(audio, display):
    rows = [Row("Sound", HEADER)]
    if audio and audio.get("available", True):
        pct = int(audio.get("percent") or 0)
        muted = bool(audio.get("muted"))
        rows.append(Row("Volume", SLIDER, value=f"{pct}%", level=pct,
                        step=action("audio.volume.set", percent="+5"),
                        step_back=action("audio.volume.set", percent="-5"),
                        badge="muted" if muted else "", tone="warning" if muted else ""))
        rows.append(Row("Mute", TOGGLE, value="on" if muted else "off",
                        call=action("audio.mute.set", muted="toggle")))
        outputs = audio.get("outputs") or []
        if len(outputs) > 1:
            current = next((o for o in outputs if o.get("default")), {})
            rows.append(Row("Output", value=current.get("description") or current.get("name", ""),
                            sheet=[Row(o.get("description") or o["name"], subtitle=o["name"],
                                       badge="in use" if o.get("default") else "", tone="accent" if o.get("default") else "",
                                       call=action("audio.output.set", output=o["name"]))
                                   for o in outputs]))
    else:
        rows.append(Row("No sound server", subtitle="PipeWire is not running"))

    rows.append(Row("Screen", HEADER))
    outputs = [o for o in (display or {}).get("outputs") or [] if o.get("active")]
    for o in outputs:
        now = {"width": o.get("width"), "height": o.get("height"), "refresh": o.get("refresh")}
        # The largest first, and the fastest of each size.
        modes = sorted(o.get("modes") or [],
                       key=lambda m: (-(m.get("width") or 0) * (m.get("height") or 0), -(m.get("refresh") or 0)))
        name = " ".join(x for x in (o.get("make"), o.get("model")) if x) or o.get("name", "")
        rows.append(Row("Resolution", subtitle=f"{name} ({o.get('name', '')})" if len(outputs) > 1 or name else "",
                        value=mode_text(now),
                        sheet=[Row(mode_text(m), badge="now" if mode_text(m) == mode_text(now) else "", tone="accent",
                                   call=action("display.mode", output=o["name"], mode=mode_arg(m)))
                               for m in modes]))
    if not outputs:
        rows.append(Row("No screen attached", subtitle="Sway is not running"))
    else:
        rows.append(Row("Turn the screen off", call=action("display.power", on=False),
                        confirm="Turn the screen off? Any key or button turns it on again."))
    return rows


# --- Status ------------------------------------------------------------------


def gib(n):
    return f"{n / (1 << 30):.1f} GB"


def status_page(version, display, log):
    rows = [Row("hostd", HEADER)]
    v = version or {}
    rows.append(Row("Version", value=v.get("version", "unknown"),
                    subtitle=f"before: {v['previous']}" if v.get("previous") else ""))
    gpu = (display or {}).get("gpu_memory") or {}
    if gpu.get("total"):
        used = gpu.get("used", 0) / gpu["total"]
        rows.append(Row("Video memory", value=f"{gib(gpu.get('used', 0))} of {gib(gpu['total'])}",
                        badge="nearly full" if used > 0.9 else "", tone="warning"))
    rows.append(Row("Recent", HEADER))
    for e in (log or [])[:12]:
        status = e.get("status", "")
        src = e.get("source") or {}
        who = src.get("name") or src.get("kind") or ""
        rows.append(Row(" ".join(x for x in (e.get("type", ""), e.get("summary", "")) if x),
                        subtitle=f"{(e.get('time') or '')[11:19]}  {who}".strip(),
                        badge="" if status == "applied" else status,
                        tone={"failed": "danger", "skipped": "warning"}.get(status, "")))
    if not log:
        rows.append(Row("Nothing done yet"))
    return rows


# --- results -----------------------------------------------------------------


def result_text(res):
    """What to say about an action's result: "" when it simply worked."""
    if not isinstance(res, dict):
        return ""
    if res.get("status") == "skipped":
        return "Skipped: " + (res.get("reason") or "nothing to do")
    return ""
