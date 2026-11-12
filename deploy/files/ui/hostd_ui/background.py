"""The background: what is behind the menu and every app. Plain data, no GTK.

The setting is ~/.config/hostd/ui.toml:

    [background]
    mode = "month"    # month (the default), color or image
    color = "#1b2433" # mode = "color"
    image = "/path"   # mode = "image"

In "month" mode the month gives the hue (the [month] tokens, as the PSP
did) and the time of day the lightness: darkest at night, lightest in the
afternoon. A color is never lighter than MAX_LIGHT, so white text reads on
it. The menu draws it as a gradient, lighter at the top; Sway, which has no
gradients, gets the color between the two.
"""

import colorsys
import os
import tomllib

MODES = ("month", "color", "image")
MONTHS = ("jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec")
MAX_LIGHT = 0.30
NIGHT_LIGHT, DAY_LIGHT = 0.07, 0.24
BOTTOM = 0.45  # the bottom of the gradient, as a share of the top's lightness


def path():
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config")
    return os.path.join(base, "hostd", "ui.toml")


def load(file=None):
    """The setting, with defaults for what is missing or wrong."""
    try:
        with open(file or path(), "rb") as f:
            raw = tomllib.load(f).get("background") or {}
    except (OSError, tomllib.TOMLDecodeError):
        raw = {}
    mode = raw.get("mode") if raw.get("mode") in MODES else "month"
    color = raw.get("color") if is_color(raw.get("color")) else ""
    image = raw.get("image") if isinstance(raw.get("image"), str) else ""
    if (mode == "color" and not color) or (mode == "image" and not image):
        mode = "month"
    return {"mode": mode, "color": color, "image": image}


def save(setting, file=None):
    """Writes the setting; keeps a color or image set before, for later."""
    file = file or path()
    os.makedirs(os.path.dirname(file), exist_ok=True)
    lines = ["# hostd's look on this screen; the menu's Settings change it.", "[background]",
             f'mode = "{setting["mode"]}"']
    for key in ("color", "image"):
        if setting.get(key):
            lines.append(f"{key} = {quote(setting[key])}")
    tmp = file + ".new"
    with open(tmp, "w") as f:
        f.write("\n".join(lines) + "\n")
    os.replace(tmp, file)


def quote(s):
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def is_color(s):
    if not isinstance(s, str) or len(s) != 7 or not s.startswith("#"):
        return False
    try:
        int(s[1:], 16)
    except ValueError:
        return False
    return True


def rgb(hex_color):
    return tuple(int(hex_color[i:i + 2], 16) / 255 for i in (1, 3, 5))


def hex_of(r, g, b):
    return "#%02x%02x%02x" % tuple(max(0, min(255, round(c * 255))) for c in (r, g, b))


def daylight(hour):
    """0 at night (22:00 to 05:00), 1 in the afternoon (12:00 to 16:00),
    in between at dawn and dusk. hour may have a fraction (19.5)."""
    h = hour % 24
    if 12 <= h <= 16:
        return 1.0
    if 5 < h < 12:
        return (h - 5) / 7
    if 16 < h < 22:
        return (22 - h) / 6
    return 0.0


def with_light(hex_color, light):
    h, _, s = colorsys.rgb_to_hls(*rgb(hex_color))
    return hex_of(*colorsys.hls_to_rgb(h, min(light, MAX_LIGHT), min(s, 0.55)))


def colors(setting, now, tokens):
    """The gradient's top and bottom colors; None in image mode."""
    if setting["mode"] == "image":
        return None
    if setting["mode"] == "color":
        _, light, _ = colorsys.rgb_to_hls(*rgb(setting["color"]))
        top = light
        base = setting["color"]
    else:
        base = tokens["month"][MONTHS[now.month - 1]]
        top = NIGHT_LIGHT + (DAY_LIGHT - NIGHT_LIGHT) * daylight(now.hour + now.minute / 60)
    top = min(top, MAX_LIGHT)
    return with_light(base, top), with_light(base, top * BOTTOM)


def css(setting, now, tokens):
    """The menu's background rule (the .backdrop under everything)."""
    if setting["mode"] == "image":
        url = "file://" + setting["image"].replace('"', "%22")
        # Darkened, so text reads on any picture.
        return (".backdrop { background-color: %s; background-image: linear-gradient(alpha(black, 0.45), "
                "alpha(black, 0.45)), url(\"%s\"); background-size: cover; background-position: center; }"
                % (tokens["color"]["bg"], url))
    top, bottom = colors(setting, now, tokens)
    return ".backdrop { background-color: %s; background-image: linear-gradient(to bottom, %s, %s); }" % (
        bottom, top, bottom)


def sway_bg(setting, now, tokens):
    """swaymsg's arguments for the screen behind the apps."""
    if setting["mode"] == "image":
        return ["output", "*", "bg", setting["image"], "fill"]
    top, bottom = colors(setting, now, tokens)
    mid = hex_of(*((a + b) / 2 for a, b in zip(rgb(top), rgb(bottom), strict=True)))
    return ["output", "*", "bg", mid, "solid_color"]


def choices(setting, tokens):
    """What the Settings page offers: (title, setting) pairs."""
    names = {"jan": "Silver", "feb": "Gold", "mar": "Olive", "apr": "Pink", "may": "Green", "jun": "Lavender",
             "jul": "Teal", "aug": "Blue", "sep": "Violet", "oct": "Amber", "nov": "Brown", "dec": "Red"}
    out = [("Month and time of day", {"mode": "month", "color": setting.get("color", ""), "image": setting.get("image", "")})]
    for m in MONTHS:
        c = with_light(tokens["month"][m], 0.2)
        out.append((names[m], {"mode": "color", "color": c, "image": setting.get("image", "")}))
    if setting.get("image"):
        out.append(("Image: " + os.path.basename(setting["image"]),
                    {"mode": "image", "color": setting.get("color", ""), "image": setting["image"]}))
    return out


def describe(setting):
    if setting["mode"] == "image":
        return os.path.basename(setting["image"])
    if setting["mode"] == "color":
        return setting["color"]
    return "Month and time of day"
