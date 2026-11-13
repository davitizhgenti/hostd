"""The shell's process: one Qt application drawing every surface (the
menu, the background, notices, the OSD) from one Shell."""

import json
import os
import sys

from PySide6.QtCore import QUrl
from PySide6.QtGui import QFont, QGuiApplication
from PySide6.QtQml import QQmlApplicationEngine

from hostd_shell import api as hapi
from hostd_shell.icons import IconProvider, app_icon
from hostd_shell.shell import Shell

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
QML = os.path.join(HERE, "qml")
SURFACES = ("Background", "NoticeLayer", "OsdLayer", "Menu")
BASE_HEIGHT, MIN_SCALE = 1080, 0.85


EX_CONFIG = 78  # the unit does not restart on this: retrying cannot help


def layer_shell_present():
    """Whether the layer-shell QML module is installed (notices, OSD and the
    background need it)."""
    from PySide6.QtCore import QLibraryInfo
    paths = [QLibraryInfo.path(QLibraryInfo.QmlImportsPath), *os.environ.get("QML_IMPORT_PATH", "").split(":")]
    return any(os.path.isdir(os.path.join(p, "org", "kde", "layershell")) for p in paths if p)


def tokens():
    with open(os.path.join(HERE, "tokens.json")) as f:
        return json.load(f)


def scale_for(height):
    return max(MIN_SCALE, (height or BASE_HEIGHT) / BASE_HEIGHT)


def engine_for(app, shell):
    """A QML engine with the Hostd module and the icons. Every surface is
    given the shell as its `shell` property (no context properties: the
    QML stays typed and lintable)."""
    engine = QQmlApplicationEngine()
    engine.addImportPath(QML)
    engine.addImageProvider("icons", IconProvider())
    engine.setInitialProperties({"shell": shell})
    engine.warnings.connect(lambda ws: [print("hostd-shell: qml:", w.toString(), file=sys.stderr) for w in ws])
    theme = engine.singletonInstance("Hostd", "Theme")
    screen = app.primaryScreen()
    theme.setProperty("scale", scale_for(screen.size().height() if screen else BASE_HEIGHT))
    theme.setProperty("screenWidth", screen.size().width() if screen else 1920)
    from hostd_shell.glide import Glide
    shell.glide = Glide.from_tokens(shell.tokens, scale_for(screen.size().height() if screen else BASE_HEIGHT))
    return engine


def load(engine, name):
    engine.load(QUrl.fromLocalFile(os.path.join(QML, "Hostd", name + ".qml")))
    return bool(engine.rootObjects())


def run(argv):
    app = QGuiApplication(argv)
    app.setApplicationName("hostd-shell")
    app.setDesktopFileName("hostd-menu")  # the menu window's app_id: how hostd knows it
    t = tokens()
    app.setFont(QFont(t["font"]["text"]))
    shell = Shell(hapi.API(), t, has_icon=lambda name: app_icon(name) is not None)
    if not shell.api.token:
        print("hostd-shell: no token: set HOSTD_TOKEN or create ~/.config/hostd/local.token", file=sys.stderr)

    from hostd_shell.bus import SHELL_NAME, Bus  # the session bus: not needed by the tests

    def lost(name):
        print(f"hostd-shell: another program has {name}", file=sys.stderr)
        if name == SHELL_NAME:
            app.quit()

    engine = engine_for(app, shell)
    if "--gallery" in argv:
        surfaces = ("Gallery",)
    else:
        app.bus = Bus(shell, on_lost=lost)
        surfaces = SURFACES
    if "--gallery" not in argv and not layer_shell_present():
        print("hostd-shell: the layer-shell QML module is missing (qml6-module-org-kde-layershell): "
              "sudo hostd-admin add shell, or sudo hostd-admin prepare", file=sys.stderr)
        return EX_CONFIG
    for name in surfaces:
        if not load(engine, name):
            print(f"hostd-shell: cannot load {name}.qml", file=sys.stderr)
            return 1
    shell.start()
    if "--menu" in argv:
        shell.showMenu()  # for trying it out: the menu without hostd's launcher
    return app.exec()
