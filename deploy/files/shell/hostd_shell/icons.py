"""Icons for QML, as images: image://icons/hostd/NAME/RRGGBB and
image://icons/app/NAME.

hostd's own icons (icons/, from Lucide, ISC license) are strokes in
currentColor: they are drawn in the color asked for, with thinner strokes
than Lucide's default. An app's icon is its desktop entry's: a name in the
icon theme (Flatpak's exported icons included) or a file. Drawing happens
here, not in a shader, so every renderer shows the same.
"""

import os

from PySide6.QtCore import QBuffer, QByteArray, QSize, Qt
from PySide6.QtGui import QIcon, QImage, QImageReader
from PySide6.QtQuick import QQuickImageProvider

HERE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "icons")
STROKE = "1.5"
DEFAULT_SIZE = 64
FLATPAK_ICONS = (os.path.expanduser("~/.local/share/flatpak/exports/share/icons"),
                 "/var/lib/flatpak/exports/share/icons")


def svg(name, color):
    """hostd's icon name as SVG in color ("#rrggbb"); None if there is none."""
    if not name or "/" in name or name.startswith("."):
        return None
    try:
        with open(os.path.join(HERE, name + ".svg")) as f:
            src = f.read()
    except OSError:
        return None
    return src.replace("currentColor", color).replace('stroke-width="2"', f'stroke-width="{STROKE}"')


def render_svg(src, size):
    data = QByteArray(src.encode())
    buf = QBuffer(data)
    buf.open(QBuffer.ReadOnly)
    reader = QImageReader(buf, QByteArray(b"svg"))
    reader.setScaledSize(size)
    img = reader.read()
    return img


def setup_theme():
    paths = list(QIcon.themeSearchPaths())
    for p in FLATPAK_ICONS:
        if os.path.isdir(p) and p not in paths:
            paths.append(p)
    QIcon.setThemeSearchPaths(paths)
    if not QIcon.themeName():
        QIcon.setThemeName("hicolor")


def app_icon(name):
    """The app's QIcon, or None."""
    if not name:
        return None
    if os.path.isabs(name):
        return QIcon(name) if os.path.isfile(name) else None
    icon = QIcon.fromTheme(name)
    return None if icon.isNull() else icon


class IconProvider(QQuickImageProvider):
    def __init__(self):
        super().__init__(QQuickImageProvider.ImageType.Image)
        setup_theme()

    def requestImage(self, ident, size, requested):  # noqa: N802 - Qt's name
        want = requested if requested.isValid() and requested.width() > 0 else QSize(DEFAULT_SIZE, DEFAULT_SIZE)
        kind, _, rest = ident.partition("/")
        img = None
        if kind == "hostd":
            name, _, color = rest.partition("/")
            src = svg(name, "#" + (color or "ffffff"))
            if src is not None:
                img = render_svg(src, want)
        elif kind == "app":
            icon = app_icon(rest)
            if icon is not None:
                img = icon.pixmap(want).toImage()
        if img is None or img.isNull():
            img = QImage(want, QImage.Format_ARGB32_Premultiplied)
            img.fill(Qt.transparent)
        return img

