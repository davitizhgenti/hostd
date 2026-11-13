"""Renders the shell's surfaces offscreen, deterministically, for the
layout and golden tests: Qt's offscreen platform and software renderer,
no animation (Theme.animate = 0), a fixed time and the fixture data.

    with Screen((1920, 1080)) as s:
        s.verb("down")
        s.rect("itemTitle/1")   # where it is, in the window
        s.grab().save(...)
"""

import os
import sys
import tempfile

os.environ["QT_QPA_PLATFORM"] = "offscreen"
os.environ["QT_QUICK_BACKEND"] = "software"
os.environ.setdefault("QT_SCALE_FACTOR", "1")
os.environ.setdefault("TZ", "UTC")

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
SHELL = os.path.join(REPO, "deploy", "files", "shell")
sys.path.insert(0, SHELL)
sys.path.insert(0, HERE)

from PySide6.QtCore import QCoreApplication, QEvent, QRectF, QUrl  # noqa: E402
from PySide6.QtGui import QFont, QGuiApplication  # noqa: E402

import fixtures  # noqa: E402
from hostd_shell import app as shellapp  # noqa: E402
from hostd_shell.shell import Shell  # noqa: E402

APP = QGuiApplication.instance() or QGuiApplication([])
APP.setDesktopFileName("hostd-menu")
TOKENS = shellapp.tokens()
APP.setFont(QFont(TOKENS["font"]["text"]))
SIZES = {"1280x800": (1280, 800), "1920x1080": (1920, 1080), "3840x2160": (3840, 2160)}


class Screen:
    """One surface (Menu, NoticeLayer, OsdLayer, Background, Gallery) at a size."""

    def __init__(self, size, surface="Menu", reads=None, look=None):
        self.size = size
        self.dir = tempfile.TemporaryDirectory()
        look_path = os.path.join(self.dir.name, "ui.toml")
        if look is not None:
            from hostd_shell import background
            background.save(look, look_path)
        self.api = fixtures.FakeAPI(reads)
        self.shell = Shell(self.api, TOKENS, now=lambda: fixtures.NOW, look_path=look_path)
        self.shell.fullscreen = False
        self.warnings = []
        self.engine = shellapp.engine_for(APP, self.shell)
        self.engine.warnings.connect(lambda ws: self.warnings.extend(w.toString() for w in ws))
        theme = self.engine.singletonInstance("Hostd", "Theme")
        theme.setProperty("scale", shellapp.scale_for(size[1]))
        theme.setProperty("animate", 0)
        for c in range(4):
            self.shell.refresh(c)
        self.engine.load(QUrl.fromLocalFile(os.path.join(shellapp.QML, "Hostd", surface + ".qml")))
        roots = self.engine.rootObjects()
        if not roots:
            raise RuntimeError(f"{surface}.qml did not load: {self.warnings}")
        self.window = roots[-1]
        if surface in ("Menu", "Gallery"):  # the full-screen ones; layers size themselves
            self.window.setWidth(size[0])
            self.window.setHeight(size[1])
        if surface == "Menu":
            self.shell.showMenu()
        else:
            self.window.setVisible(True)
        self.settle()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()

    def close(self):
        # The windows go before the engine, and the shell after both: QML
        # bound to it must not see it vanish.
        self.window.setVisible(False)
        for root in self.engine.rootObjects():
            root.deleteLater()
        QCoreApplication.sendPostedEvents(None, QEvent.DeferredDelete)
        self.engine.deleteLater()
        QCoreApplication.sendPostedEvents(None, QEvent.DeferredDelete)
        self.dir.cleanup()
        self.engine = self.window = None

    def settle(self, rounds=4):
        """Runs events, then draws a frame: Qt lays views out (polish) only
        before drawing, and the tests measure what is drawn."""
        for _ in range(rounds):
            QCoreApplication.processEvents()
        if getattr(self, "window", None) is not None and self.window.isVisible():
            self.window.grabWindow()
            for _ in range(rounds):
                QCoreApplication.processEvents()

    # --- driving it ---

    def verb(self, *verbs):
        for v in verbs:
            self.shell.handle(v)
            self.settle()

    def key(self, name, text=""):
        took = self.shell.key(name, text, False)
        self.settle()
        return took

    # --- reading it ---

    def items(self):
        """Every item in the window with an objectName, by name."""
        out = {}

        def walk(item):
            name = item.objectName()
            if name:
                out.setdefault(name, item)
            for child in item.childItems():
                walk(child)
        walk(self.window.contentItem())
        return out

    def item(self, name):
        found = self.items().get(name)
        if found is None:
            raise KeyError(f"no item {name!r} on screen")
        return found

    def rect(self, name_or_item):
        it = self.item(name_or_item) if isinstance(name_or_item, str) else name_or_item
        r = it.mapRectToScene(QRectF(0, 0, it.width(), it.height()))
        return (r.x(), r.y(), r.width(), r.height())

    def shown(self, name_or_item):
        """Whether it is drawn: visible, not see-through, inside the window."""
        it = self.item(name_or_item) if isinstance(name_or_item, str) else name_or_item
        if not it.isVisible():
            return False
        p, opacity = it, 1.0
        while p is not None:
            opacity *= p.opacity()
            p = p.parentItem()
        return opacity > 0.01

    def grab(self):
        return self.window.grabWindow()
