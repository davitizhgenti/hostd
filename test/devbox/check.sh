#!/bin/bash
# Smoke test for the devbox, run inside it as the screen user:
#   make devbox-check
# Checks every part hostd will rely on: systemd, the user session, Sway,
# VNC, PipeWire, transient user services and rootless Podman.
set -u
. ~/.bashrc

pass=0 fail=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '       %s\n' "$2"; fail=$((fail+1)); }
check() { local name=$1; shift; local out; if out=$("$@" 2>&1); then ok "$name"; else bad "$name" "$(echo "$out" | tail -3)"; fi; }
wait_for() { local i; for i in $(seq 1 50); do "$@" >/dev/null 2>&1 && return 0; sleep 0.2; done; return 1; }

echo "systemd"
check "system manager is up, no failed units" bash -c 'systemctl is-system-running | grep -qx running || { systemctl --failed --no-legend; exit 1; }'
check "user manager is up"     bash -c 'systemctl --user is-system-running | grep -Eq "running|degraded"'
check "lingering is on"        bash -c 'loginctl show-user screen -p Linger | grep -q yes'

echo "display"
check "sway is running"        wait_for swaymsg -t get_version
check "virtual output 1920x1080" bash -c 'swaymsg -t get_outputs -r | jq -e ".[] | select(.name==\"HEADLESS-1\" and .current_mode.width==1920)"'
check "VNC listening on 5900"  bash -c 'ss -ltn | grep -q ":5900 "'

echo "apps as transient services (how hostd will start them)"
systemctl --user stop hostd-devbox-check.service >/dev/null 2>&1
check "start foot as hostd-devbox-check.service" systemd-run --user --unit=hostd-devbox-check -p Type=exec foot
check "its window appears in sway" wait_for bash -c 'swaymsg -t get_tree -r | jq -e "[.. | objects | select(.app_id? == \"foot\")] | length > 0"'
check "window PID maps to the unit via cgroup" bash -c '
  pid=$(swaymsg -t get_tree -r | jq -r "[.. | objects | select(.app_id? == \"foot\")][0].pid")
  grep -q "hostd-devbox-check.service" /proc/$pid/cgroup'
check "stop the unit closes the window" bash -c 'systemctl --user stop hostd-devbox-check.service &&
  for i in $(seq 1 25); do swaymsg -t get_tree -r | jq -e "[.. | objects | select(.app_id? == \"foot\")] | length == 0" >/dev/null && exit 0; sleep 0.2; done; exit 1'

echo "audio"
check "fake HDMI sink exists"  wait_for bash -c 'wpctl status | grep -q "Fake HDMI"'
check "set volume to 40%"      bash -c 'wpctl set-mute @DEFAULT_AUDIO_SINK@ 0 && wpctl set-volume @DEFAULT_AUDIO_SINK@ 0.40 && wpctl get-volume @DEFAULT_AUDIO_SINK@ | grep -q "0.40"'
check "mute and unmute"        bash -c 'wpctl set-mute @DEFAULT_AUDIO_SINK@ 1 && wpctl get-volume @DEFAULT_AUDIO_SINK@ | grep -q MUTED &&
                                         wpctl set-mute @DEFAULT_AUDIO_SINK@ 0 && ! wpctl get-volume @DEFAULT_AUDIO_SINK@ | grep -q MUTED'

echo "containers"
check "podman API socket answers" bash -c 'curl -fsS --unix-socket $XDG_RUNTIME_DIR/podman/podman.sock http://d/_ping | grep -q OK'
check "run a container"        bash -c 'podman run --rm docker.io/library/alpine:latest echo hello | grep -q hello'

echo "screenshot"
check "grim captures the screen to /tmp/devbox-screen.png" grim /tmp/devbox-screen.png

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
