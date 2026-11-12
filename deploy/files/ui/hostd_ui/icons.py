"""Icons: hostd's own line icons (icons/, Lucide, ISC license) and the apps'.

hostd's icons are strokes in currentColor; they are drawn in one token
color with thinner lines than Lucide's default, the way the design wants.
An app's icon is its desktop entry's (a name in the icon theme, or a
path); an app with none gets its first letter.
"""

import os

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Gdk", "4.0")
gi.require_version("GdkPixbuf", "2.0")
from gi.repository import Gdk, GdkPixbuf, Gtk  # noqa: E402

HERE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "icons")
STROKE = "1.5"
# Where Flatpak puts its apps' icons (the user's, then the system's).
FLATPAK_ICONS = (os.path.expanduser("~/.local/share/flatpak/exports/share/icons"),
                 "/var/lib/flatpak/exports/share/icons")

_cache = {}
_theme_ready = False


def svg(name, color):
    """The icon's SVG source in color; None when hostd has no such icon."""
    try:
        with open(os.path.join(HERE, name + ".svg")) as f:
            src = f.read()
    except OSError:
        return None
    return src.replace("currentColor", color).replace('stroke-width="2"', f'stroke-width="{STROKE}"')


def texture(name, size, color):
    """hostd's icon name at size px in color, as a Gdk.Texture (cached)."""
    key = (name, size, color)
    if key not in _cache:
        src = svg(name, color)
        if src is None:
            return None
        loader = GdkPixbuf.PixbufLoader.new_with_type("svg")
        loader.set_size(size, size)
        loader.write(src.encode())
        loader.close()
        _cache[key] = Gdk.Texture.new_for_pixbuf(loader.get_pixbuf())
    return _cache[key]


def _theme():
    global _theme_ready
    theme = Gtk.IconTheme.get_for_display(Gdk.Display.get_default())
    if not _theme_ready:
        for p in FLATPAK_ICONS:
            if os.path.isdir(p):
                theme.add_search_path(p)
        _theme_ready = True
    return theme


def app_paintable(icon, size):
    """An app's icon (a theme name or a file) at size, or None."""
    if not icon:
        return None
    try:
        if os.path.isabs(icon):
            if not os.path.isfile(icon):
                return None
            pix = GdkPixbuf.Pixbuf.new_from_file_at_scale(icon, size, size, True)
            return Gdk.Texture.new_for_pixbuf(pix)
        theme = _theme()
        if not theme.has_icon(icon):
            return None
        return theme.lookup_icon(icon, None, size, 1, Gtk.TextDirection.LTR, 0)
    except Exception:  # noqa: BLE001 - a broken icon file: the letter instead
        return None


def widget(name, app_icon, title, size, color):
    """An icon widget for a row: the app's own icon, else hostd's named
    icon, else the title's first letter in a square."""
    paintable = app_paintable(app_icon, size) or (texture(name, size, color) if name else None)
    if paintable is not None:
        img = Gtk.Image.new_from_paintable(paintable)
        img.set_pixel_size(size)
        img.add_css_class("icon")
        return img
    letter = Gtk.Label(label=(title or "?")[:1].upper())
    letter.add_css_class("letter")
    letter.set_size_request(size, size)
    return letter
