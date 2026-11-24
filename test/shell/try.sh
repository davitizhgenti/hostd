#!/bin/bash
# The new shell with the REAL hostd, in the shell's test container (make
# try-shell): hostd built from this repository, sample apps, a headless Sway,
# and hostd-shell connected to hostd's socket. It presses keys on the menu
# the way a keyboard does (wtype) and takes screenshots into
# test/shell/out/try/ for you to look at. Nothing is uploaded anywhere.
#
#   make try-shell            screenshots, then the container ends
#   make try-shell KEEP=1     stays up; in another terminal:
#       podman exec hostd-try key Down        press a key on the menu
#       podman exec hostd-try snap my-shot    screenshot -> out/try/my-shot.png
#       podman exec hostd-try hctl apps       hostctl against this hostd
#       podman exec hostd-try hostd-log       what hostd and the shell said
#
# No systemd runs here, so starting an app fails (the menu says so); what
# this shows is the menu, hostd's real answers and events.
set -u
out=/repo/test/shell/out/try
rm -rf "$out"
mkdir -p "$out"
export HOME=/tmp/home XDG_RUNTIME_DIR=/tmp/rt XDG_CONFIG_HOME=/tmp/home/.config XDG_STATE_HOME=/tmp/home/.state
mkdir -p "$HOME" "$XDG_RUNTIME_DIR" "$XDG_CONFIG_HOME/hostd/apps" "$XDG_STATE_HOME" && chmod 700 "$XDG_RUNTIME_DIR"
export WLR_BACKENDS=headless WLR_RENDERER=pixman WLR_LIBINPUT_NO_DEVICES=1
printf 'output HEADLESS-1 mode 1920x1080 bg #000000 solid_color\ndefault_border none\n' > /tmp/sway.conf

# What the menu lists: apps in folders, a service, a launcher with a game under it.
cfg=$XDG_CONFIG_HOME/hostd
app() { printf '%s\n' "$2" > "$cfg/apps/$1.toml"; }
app terminal 'name = "Terminal"
runner = { type = "exec", command = ["foot"] }'
app browser 'name = "Browser"
runner = { type = "exec", command = ["firefox"] }'
app jellyfin 'name = "Jellyfin"
folder = "Services"
surface = "background"
runner = { type = "exec", command = ["sleep", "3600"] }'
app steam 'name = "Steam"
folder = "Games/Steam"
launcher = true
runner = { type = "exec", command = ["steam"] }'
app portal 'name = "Portal 2"
folder = "Games/Steam"
under = "steam"
runner = { type = "exec", command = ["steam", "steam://run/620"] }'
app dolphin 'name = "Dolphin"
folder = "Games/Dolphin"
runner = { type = "exec", command = ["dolphin-emu"] }'
printf '[[tab]]\nfolder = "Games"\nicon = "gamepad-2"\nview = "tiles"\n\n[[tab]]\nfolder = "Services"\nicon = "server"\n' > "$cfg/menu.toml"
printf '[background]\nmode = "color"\ncolor = "#204060"\n' > "$cfg/ui.toml"

# Helpers for you (and for this script).
install -d /usr/local/bin
cat > /usr/local/bin/snap <<'EOS'
#!/bin/bash
export XDG_RUNTIME_DIR=/tmp/rt
export WAYLAND_DISPLAY=$(basename $(ls $XDG_RUNTIME_DIR/wayland-? | head -1))
mkdir -p /repo/test/shell/out/try
grim "/repo/test/shell/out/try/${1:-shot}.png" && echo "/repo/test/shell/out/try/${1:-shot}.png"
EOS
cat > /usr/local/bin/key <<'EOS'
#!/bin/bash
export XDG_RUNTIME_DIR=/tmp/rt
export WAYLAND_DISPLAY=$(basename $(ls $XDG_RUNTIME_DIR/wayland-? | head -1))
for k in "$@"; do wtype -s 150 -k "$k"; sleep 0.25; done
EOS
cat > /usr/local/bin/hctl <<'EOS'
#!/bin/bash
export HOSTD_URL=unix:///tmp/rt/hostd.sock HOSTD_TOKEN=$(cat /tmp/home/admin.token) HOME=/tmp/home XDG_CONFIG_HOME=/tmp/home/.config
exec /repo/bin/hostctl "$@"
EOS
cat > /usr/local/bin/hostd-log <<'EOS'
#!/bin/bash
tail -n 60 /repo/test/shell/out/try/hostd.log /repo/test/shell/out/try/shell.log
EOS
chmod +x /usr/local/bin/snap /usr/local/bin/key /usr/local/bin/hctl /usr/local/bin/hostd-log

exec dbus-run-session -- bash -c '
  fail=0
  ok()  { echo "  PASS $1"; }
  bad() { echo "  FAIL $1"; fail=1; }
  sway -c /tmp/sway.conf >"'"$out"'/sway.log" 2>&1 &
  for i in $(seq 50); do ls $XDG_RUNTIME_DIR/wayland-* >/dev/null 2>&1 && break; sleep 0.2; done
  export WAYLAND_DISPLAY=$(basename $(ls $XDG_RUNTIME_DIR/wayland-? | head -1)) SWAYSOCK=$(ls $XDG_RUNTIME_DIR/sway-ipc.* | head -1)
  export QT_QPA_PLATFORM=wayland QT_QUICK_BACKEND=software
  /repo/bin/hostd -listen= -state /tmp/home/.state/hostd -runtime $XDG_RUNTIME_DIR >"'"$out"'/hostd.log" 2>&1 &
  for i in $(seq 50); do [ -S $XDG_RUNTIME_DIR/hostd.sock ] && [ -s $XDG_RUNTIME_DIR/hostd-admin-token ] && break; sleep 0.2; done
  [ -S $XDG_RUNTIME_DIR/hostd.sock ] && ok "hostd is listening" || { bad "hostd did not start"; cat "'"$out"'/hostd.log"; exit 1; }
  export HOSTD_TOKEN=$(cat $XDG_RUNTIME_DIR/hostd-admin-token)
  echo "$HOSTD_TOKEN" > /tmp/home/admin.token  # hostd deletes its copy at first use
  /repo/deploy/files/shell/hostd-shell --menu >"'"$out"'/shell.log" 2>&1 &
  shellpid=$!
  for i in $(seq 50); do swaymsg -t get_tree 2>/dev/null | grep -q "\"app_id\": \"hostd-menu\"" && break; sleep 0.2; done
  swaymsg -t get_tree | grep -q "\"app_id\": \"hostd-menu\"" && ok "the menu is on screen (app_id hostd-menu)" || bad "no menu window"
  sleep 1.5
  /usr/local/bin/snap 01-apps >/dev/null
  hctl apps >"'"$out"'/apps.txt" 2>&1
  grep -q "Portal 2" "'"$out"'/apps.txt" && ok "hostd lists the sample apps" || bad "hostd does not list the sample apps: $(head -3 "'"$out"'/apps.txt")"
  for step in "02-games Right" "03-services Right" "04-users Right" "05-settings Right" "06-status Right" "07-back-to-apps Right"; do
    set -- $step; /usr/local/bin/key $2; sleep 0.8; /usr/local/bin/snap $1 >/dev/null
  done
  /usr/local/bin/key Down Down; sleep 0.5; /usr/local/bin/snap 08-down >/dev/null
  same() { cmp -s "'"$out"'/$1.png" "'"$out"'/$2.png"; }
  for pair in "01-apps 02-games" "02-games 03-services" "03-services 04-users" "04-users 05-settings" "05-settings 06-status"; do
    set -- $pair; same $1 $2 && bad "the screen did not change between $1 and $2" || ok "the screen changed: $1 -> $2"
  done
  same 01-apps 07-back-to-apps && ok "seven moves to the right come back to Apps" || bad "07-back-to-apps is not the Apps screen again"
  grep -qi "traceback\|qml:" "'"$out"'/shell.log" && bad "the shell said: $(grep -i "traceback\|qml:" "'"$out"'/shell.log" | head -3)" || ok "the shell logged no errors"
  grep -qi "panic" "'"$out"'/hostd.log" && bad "hostd panicked" || ok "hostd logged no panic"
  kill -0 $shellpid 2>/dev/null && ok "the shell is still running" || bad "the shell died"
  ls "'"$out"'"/*.png | sed "s|^|  screenshot: |"
  if [ "${KEEP:-}" = 1 ]; then echo "  Staying up (KEEP=1): podman exec hostd-try key Down | snap name | hctl apps | hostd-log"; exec sleep infinity; fi
  exit $fail
' 2> >(grep -v '^dbus-daemon\[' >&2)
