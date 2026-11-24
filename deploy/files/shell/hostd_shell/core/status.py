"""The status strip's content: which services to show as squares, and how
many squares a line holds before "+N". Pure."""

from hostd_shell import views

MAX = 6  # squares per line (Place.stripMax is the same number: a test holds them equal)
STATES = ("starting", "running", "stopping", "failed")


def services(instances, apps):
    """[{id, name, state, tone}] of the background apps that are up, coming
    up or have failed (a stopped one has no square), failed first, then in
    the apps' order."""
    live, last = {}, {}
    for inst in instances or []:
        app = inst.get("app") or ""
        if inst.get("state") in views.LIVE:
            live.setdefault(app, inst)
        else:
            last[app] = inst
    out = []
    for a in apps or []:
        if a.get("surface") != "background" or a.get("id") == views.MENU_ID:
            continue
        inst = live.get(a["id"])
        state = (inst.get("state") or "running") if inst else ("failed" if last.get(a["id"], {}).get("state") == "failed" else "")
        if state in STATES:
            out.append({"id": a["id"], "name": a.get("name") or a["id"], "state": state, "tone": views.TONES.get(state, "")})
    return sorted(out, key=lambda s: s["state"] != "failed")


def capped(items, limit=MAX):
    """(the first `limit` items, how many more there are). A line that
    overflows gives its last place to the count, so it never holds more
    than `limit` squares and a count."""
    items = list(items)
    if len(items) <= limit:
        return items, 0
    return items[:limit - 1], len(items) - (limit - 1)
