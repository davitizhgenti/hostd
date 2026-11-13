"""The shell on the session bus (Gio, on Qt's GLib main loop).

- org.freedesktop.Notifications: every app's notifications, and hostd's own
  (internal/notify), drawn by the shell.
- org.hostd.Shell: ShowMenu and HideMenu, and MenuClosed. hostd runs the
  menu as its app "hostd-menu", whose command is `hostd-shell menu`: that
  launcher asks the shell to show the menu and lives until it is closed, so
  hostd sees the menu as running exactly while it is on screen. If the
  launcher goes away without asking (killed), the menu is hidden too.
"""

import sys

from gi.repository import Gio, GLib

from hostd_shell import notices

NOTIFY_NAME = "org.freedesktop.Notifications"
NOTIFY_PATH = "/org/freedesktop/Notifications"
SHELL_NAME = "org.hostd.Shell"
SHELL_PATH = "/org/hostd/Shell"

NOTIFY_XML = """
<node>
  <interface name="org.freedesktop.Notifications">
    <method name="GetCapabilities"><arg direction="out" type="as"/></method>
    <method name="Notify">
      <arg direction="in" type="s"/><arg direction="in" type="u"/><arg direction="in" type="s"/>
      <arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="in" type="as"/>
      <arg direction="in" type="a{sv}"/><arg direction="in" type="i"/>
      <arg direction="out" type="u"/>
    </method>
    <method name="CloseNotification"><arg direction="in" type="u"/></method>
    <method name="GetServerInformation">
      <arg direction="out" type="s"/><arg direction="out" type="s"/>
      <arg direction="out" type="s"/><arg direction="out" type="s"/>
    </method>
    <signal name="NotificationClosed"><arg type="u"/><arg type="u"/></signal>
    <signal name="ActionInvoked"><arg type="u"/><arg type="s"/></signal>
  </interface>
</node>
"""

SHELL_XML = """
<node>
  <interface name="org.hostd.Shell">
    <method name="ShowMenu"/>
    <method name="HideMenu"/>
    <method name="Ping"/>
    <signal name="MenuClosed"/>
  </interface>
</node>
"""


class Bus:
    def __init__(self, shell, on_lost=None):
        self.shell = shell
        self.on_lost = on_lost
        self.conn = None
        self.launcher_watch = 0
        shell.noticeClosed.connect(self._closed)
        shell.menuClosed.connect(self._menu_closed)
        Gio.bus_own_name(Gio.BusType.SESSION, NOTIFY_NAME,
                         Gio.BusNameOwnerFlags.ALLOW_REPLACEMENT | Gio.BusNameOwnerFlags.REPLACE,
                         self._acquired_bus, None, self._lost)
        Gio.bus_own_name(Gio.BusType.SESSION, SHELL_NAME, Gio.BusNameOwnerFlags.REPLACE,
                         None, None, self._lost)

    def _acquired_bus(self, conn, _name):
        self.conn = conn
        conn.register_object(NOTIFY_PATH, Gio.DBusNodeInfo.new_for_xml(NOTIFY_XML).interfaces[0], self._notify_call, None, None)
        conn.register_object(SHELL_PATH, Gio.DBusNodeInfo.new_for_xml(SHELL_XML).interfaces[0], self._shell_call, None, None)

    def _lost(self, _conn, name):
        if self.on_lost:
            self.on_lost(name)

    # --- notifications ---

    def _notify_call(self, _conn, _sender, _path, _iface, method, params, invocation):
        if method == "GetCapabilities":
            invocation.return_value(GLib.Variant("(as)", (["body"],)))
        elif method == "GetServerInformation":
            invocation.return_value(GLib.Variant("(ssss)", ("hostd", "hostd", "2", "1.2")))
        elif method == "Notify":
            app, replaces, _icon, summary, body, _actions, hints, timeout = params.unpack()
            nid = self.shell.notify(app, summary, body, replaces, notices.urgency_of(hints), timeout)
            invocation.return_value(GLib.Variant("(u)", (nid,)))
        elif method == "CloseNotification":
            (nid,) = params.unpack()
            self.shell.close_notice(nid)
            invocation.return_value(None)

    def _closed(self, nid, reason):
        if self.conn is not None:
            self.conn.emit_signal(None, NOTIFY_PATH, NOTIFY_NAME, "NotificationClosed", GLib.Variant("(uu)", (nid, reason)))

    # --- the menu's launcher ---

    def _shell_call(self, conn, sender, _path, _iface, method, _params, invocation):
        if method == "ShowMenu":
            self._watch_launcher(conn, sender)
            self.shell.showMenu()
        elif method == "HideMenu":
            self.shell.hideMenu()
        # Ping: answering is the point (the launcher checks the shell is alive)
        invocation.return_value(None)

    def _watch_launcher(self, conn, sender):
        if self.launcher_watch:
            Gio.bus_unwatch_name(self.launcher_watch)
        self.launcher_watch = Gio.bus_watch_name_on_connection(
            conn, sender, Gio.BusNameWatcherFlags.NONE, None, lambda *_: self.shell.hideMenu())

    def _menu_closed(self):
        if self.launcher_watch:
            Gio.bus_unwatch_name(self.launcher_watch)
            self.launcher_watch = 0
        if self.conn is not None:
            self.conn.emit_signal(None, SHELL_PATH, SHELL_NAME, "MenuClosed", None)


PING_EVERY, PING_TIMEOUT, PING_FAILS = 10, 3, 2  # seconds, seconds, misses before giving up


def launch_menu():
    """`hostd-shell menu`: show the menu, and live until it is closed. Stopped
    (SIGTERM: hostd closing the instance), it hides the menu. If the shell
    goes away or stops answering, the menu is gone with it, so the launcher
    ends too (hostd then sees the menu app closed, never stuck "running").
    Returns the exit code."""
    import signal
    loop = GLib.MainLoop()
    try:
        conn = Gio.bus_get_sync(Gio.BusType.SESSION, None)
        conn.signal_subscribe(SHELL_NAME, SHELL_NAME, "MenuClosed", SHELL_PATH, None,
                              Gio.DBusSignalFlags.NONE, lambda *_: loop.quit())
        conn.call_sync(SHELL_NAME, SHELL_PATH, SHELL_NAME, "ShowMenu", None, None,
                       Gio.DBusCallFlags.NO_AUTO_START, 5000, None)
    except GLib.Error as e:
        print(f"hostd-shell menu: the shell is not running ({e.message}); systemctl --user start hostd-shell")
        return 1

    result = {"code": 0, "misses": 0}

    def call(method, timeout_ms):
        conn.call_sync(SHELL_NAME, SHELL_PATH, SHELL_NAME, method, None, None,
                       Gio.DBusCallFlags.NO_AUTO_START, timeout_ms, None)

    def stop():
        try:
            call("HideMenu", 2000)
        except GLib.Error:
            pass
        loop.quit()
        return False

    def shell_gone(*_):
        print("hostd-shell menu: the shell went away", file=sys.stderr)
        result["code"] = 1
        loop.quit()

    def ping():
        try:
            call("Ping", PING_TIMEOUT * 1000)
            result["misses"] = 0
        except GLib.Error:
            result["misses"] += 1
            if result["misses"] >= PING_FAILS:
                print("hostd-shell menu: the shell does not answer", file=sys.stderr)
                result["code"] = 1
                loop.quit()
                return False
        return True

    Gio.bus_watch_name_on_connection(conn, SHELL_NAME, Gio.BusNameWatcherFlags.NONE, None, shell_gone)
    GLib.timeout_add_seconds(PING_EVERY, ping)
    GLib.unix_signal_add(GLib.PRIORITY_DEFAULT, signal.SIGTERM, stop)
    GLib.unix_signal_add(GLib.PRIORITY_DEFAULT, signal.SIGINT, stop)
    loop.run()
    return result["code"]
