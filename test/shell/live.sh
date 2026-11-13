#!/bin/bash
# The shell on a real Wayland session (headless Sway), in the shell's test
# container (make test-shell-live): what only a compositor shows.
#   - the shell owns the notification and shell bus names
#   - `hostd-shell menu` opens the menu: a window with app_id hostd-menu
#     (how hostd knows it); stopping the launcher closes it
#   - the background layer draws the ui.toml setting
# No hostd runs: the menu says it cannot reach it, which is fine here.
# Screenshots go to test/shell/out/live/.
set -u
out=/repo/test/shell/out/live
mkdir -p "$out"
export XDG_RUNTIME_DIR=/tmp/rt XDG_CONFIG_HOME=/tmp/cfg
mkdir -p "$XDG_RUNTIME_DIR" "$XDG_CONFIG_HOME/hostd" && chmod 700 "$XDG_RUNTIME_DIR"
export WLR_BACKENDS=headless WLR_RENDERER=pixman WLR_LIBINPUT_NO_DEVICES=1
printf 'output HEADLESS-1 mode 1280x800 bg #000000 solid_color\ndefault_border none\n' > /tmp/sway.conf
printf '[background]\nmode = "color"\ncolor = "#204060"\n' > "$XDG_CONFIG_HOME/hostd/ui.toml"
exec dbus-run-session -- bash -c '
  fail=0
  ok()  { echo "  PASS $1"; }
  bad() { echo "  FAIL $1"; fail=1; }
  sway -c /tmp/sway.conf >"'"$out"'/sway.log" 2>&1 &
  for i in $(seq 50); do ls $XDG_RUNTIME_DIR/wayland-* >/dev/null 2>&1 && break; sleep 0.2; done
  export WAYLAND_DISPLAY=$(basename $(ls $XDG_RUNTIME_DIR/wayland-? | head -1)) SWAYSOCK=$(ls $XDG_RUNTIME_DIR/sway-ipc.* | head -1)
  export QT_QPA_PLATFORM=wayland QT_QUICK_BACKEND=software HOSTD_TOKEN=none
  shell=/repo/deploy/files/shell/hostd-shell
  $shell >"'"$out"'/shell.log" 2>&1 &
  owner() { python3 -c "
from gi.repository import Gio, GLib
bus = Gio.bus_get_sync(Gio.BusType.SESSION)
print(bus.call_sync(\"org.freedesktop.Notifications\", \"/org/freedesktop/Notifications\", \"org.freedesktop.Notifications\",
                    \"GetServerInformation\", None, None, Gio.DBusCallFlags.NO_AUTO_START, 2000, None).unpack()[0])" 2>/dev/null; }
  for i in $(seq 50); do [ "$(owner)" = hostd ] && break; sleep 0.2; done
  [ "$(owner)" = hostd ] && ok "the shell is the notification server" || bad "the shell is not the notification server"
  sleep 1
  grim "'"$out"'/desktop.png"
  python3 -c "
from PySide6.QtGui import QImage
c = QImage(\"'"$out"'/desktop.png\").pixelColor(640, 400).name()
exit(0 if c != \"#000000\" else 1)" && ok "the background layer draws the setting" || bad "the background is still black"
  $shell menu >"'"$out"'/launcher.log" 2>&1 &
  launcher=$!
  sleep 2
  swaymsg -t get_tree | grep -q "\"app_id\": \"hostd-menu\"" && ok "the menu window is app_id hostd-menu" || bad "no hostd-menu window"
  notify-send -a Test "Hello" "from the live check" && ok "a notice was accepted" || bad "notify-send failed"
  sleep 0.5
  grim "'"$out"'/menu.png"
  kill $launcher; wait $launcher; code=$?
  [ $code -eq 0 ] && ok "the launcher ends cleanly when stopped" || bad "the launcher exited $code"
  sleep 1
  swaymsg -t get_tree | grep -q "\"app_id\": \"hostd-menu\"" && bad "the menu stayed after its launcher stopped" || ok "stopping the launcher closes the menu"
  grep -q "qml:" "'"$out"'/shell.log" && bad "QML warnings: $(grep qml: "'"$out"'/shell.log" | head -3)" || ok "no QML warnings"
  exit $fail
'
