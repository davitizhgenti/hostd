"""The menu file, ~/.config/hostd/menu.toml: which folders of apps are tabs
of their own, next to Apps.

    [[tab]]
    folder = "Games"     # a top-level folder (an app file's folder = "Games/...")
    icon = "gamepad-2"   # one of the menu's icons; the folder icon if not
    view = "tiles"       # "list" (the default), or every app in it as tiles in one column, by launcher

The tabs come after Apps, in the file's order. With no file there are no
tabs; a file that cannot be used gives none and says why.
"""

import os
import tomllib
from typing import NamedTuple

DEFAULT_ICON = "folder"
MAX_TABS = 8
VIEWS = ("list", "tiles")


class Tab(NamedTuple):
    folder: str
    icon: str = DEFAULT_ICON
    view: str = "list"


def path():
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config")
    return os.path.join(base, "hostd", "menu.toml")


def load(file=None, has_icon=None):
    """(tabs, problem): tabs as Tabs; problem is "" or what is wrong with
    the file. has_icon tells whether an icon name exists."""
    has_icon = has_icon or (lambda name: True)
    try:
        with open(file or path(), "rb") as f:
            raw = tomllib.load(f)
    except FileNotFoundError:
        return [], ""
    except (OSError, ValueError, RecursionError) as e:  # ValueError: bad TOML, or bytes that are no UTF-8
        return [], f"menu.toml: {e}"
    tabs = raw.get("tab", [])
    if not isinstance(tabs, list) or not all(isinstance(t, dict) for t in tabs):
        return [], "menu.toml: tab must be a list of tables ([[tab]])"
    out, seen = [], set()
    for n, t in enumerate(tabs, 1):
        folder = t.get("folder")
        if not isinstance(folder, str) or not folder.strip() or "/" in folder.strip("/"):
            return [], f"menu.toml: tab {n}: folder must be one folder's name, such as \"Games\""
        folder = folder.strip().strip("/")
        if folder in seen:
            return [], f"menu.toml: tab {n}: {folder} is a tab already"
        seen.add(folder)
        icon = t.get("icon")
        view = t.get("view", "list")
        if view not in VIEWS:
            return [], f"menu.toml: tab {n}: view must be one of {', '.join(VIEWS)}"
        out.append(Tab(folder, icon if isinstance(icon, str) and has_icon(icon) else DEFAULT_ICON, view))
    if len(out) > MAX_TABS:
        return [], f"menu.toml: at most {MAX_TABS} tabs"
    return out, ""


def mtime(file=None):
    """When the file last changed (0 if there is none): to read it again
    only then."""
    try:
        return os.stat(file or path()).st_mtime_ns
    except OSError:
        return 0
