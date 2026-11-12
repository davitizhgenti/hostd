"""The look: tokens.css and theme.css, scaled to the screen.

Both files sit beside this package. Sizes in them are px for a
1080-pixel-high screen; every one is scaled by the screen's height, so the
UI fills a 4K TV as it fills a 1280x800 projector. Below 1080 it shrinks
less than the screen (MIN_SCALE), so text stays readable from a distance.
"""

import os
import re

BASE_HEIGHT = 1080
MIN_SCALE = 0.85
HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def scale_for(height):
    return max(MIN_SCALE, (height or BASE_HEIGHT) / BASE_HEIGHT)


def scaled(css, height):
    """css with every px size scaled for a screen this high."""
    s = scale_for(height)
    return re.sub(r"(\d+(?:\.\d+)?)px", lambda m: "%dpx" % max(1, round(float(m.group(1)) * s)), css)


def stylesheet(height, root=HERE):
    """tokens.css then theme.css, scaled."""
    parts = []
    for name in ("tokens.css", "theme.css"):
        with open(os.path.join(root, name)) as f:
            parts.append(f.read())
    return scaled("\n".join(parts), height)


def tokens(root=HERE):
    """The tokens as data (tokens.json): {"color": {"bg": "#..."}, ...}."""
    import json
    with open(os.path.join(root, "tokens.json")) as f:
        return json.load(f)


def apply(display=None):
    """Loads the stylesheet for the display's first monitor; returns its size."""
    from gi.repository import Gdk, Gtk  # only here: the rest needs no GTK

    display = display or Gdk.Display.get_default()
    size = screen_size(display)
    provider = Gtk.CssProvider()
    provider.load_from_string(stylesheet(size[1]))
    Gtk.StyleContext.add_provider_for_display(display, provider, Gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
    return size


def screen_size(display):
    monitors = display.get_monitors()
    if monitors.get_n_items():
        geo = monitors.get_item(0).get_geometry()
        return geo.width, geo.height
    return 1920, 1080
