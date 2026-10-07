#!/bin/bash
# Checks that a hostd machine (the real one or the devbox) has everything
# hostd relies on: systemd, the screen user's session, Sway, VNC, notices,
# PipeWire, apps as transient user services, and rootless Podman.
#
# Run as the screen user, on the machine or from the laptop:
#   ssh screen@<machine> 'bash -s' < deploy/check.sh
#   make devbox-check
#
# It opens and closes a test window, and puts the volume back as it was.
set -u

export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
export DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS:-unix:path=$XDG_RUNTIME_DIR/bus}
if [ -z "${WAYLAND_DISPLAY:-}" ]; then
	WAYLAND_DISPLAY=$(basename "$(ls "$XDG_RUNTIME_DIR"/wayland-[0-9] 2>/dev/null | head -1)" 2>/dev/null)
	export WAYLAND_DISPLAY
fi
if [ -z "${SWAYSOCK:-}" ] || [ ! -S "$SWAYSOCK" ]; then
	SWAYSOCK=$(ls "$XDG_RUNTIME_DIR"/sway-ipc.*.sock 2>/dev/null | head -1)
	export SWAYSOCK
fi
UNIT=hostd-check
SHOT=/tmp/hostd-check.png

pass=0 fail=0
ok() { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() {
	printf '  \033[31mFAIL\033[0m %s\n' "$1"
	if [ -n "${2:-}" ]; then printf '       %s\n' "$2"; fi
	fail=$((fail + 1))
}
check() {
	local name=$1 out
	shift
	if out=$("$@" 2>&1); then ok "$name"; else bad "$name" "$(echo "$out" | tail -3)"; fi
}
wait_for() {
	local _
	for _ in $(seq 1 50); do
		"$@" >/dev/null 2>&1 && return 0
		sleep 0.2
	done
	return 1
}
foot_windows() { swaymsg -t get_tree -r | jq '[.. | objects | select(.app_id? == "foot")] | length'; }

echo "systemd"
check "system manager is up, no failed units" \
	bash -c 'systemctl is-system-running | grep -qx running || { systemctl --failed --no-legend; exit 1; }'
check "user manager is up" bash -c 'systemctl --user is-system-running | grep -Eq "running|degraded"'
check "lingering is on" bash -c 'loginctl show-user "$(id -un)" -p Linger | grep -q yes'

echo "display"
check "sway is running" wait_for swaymsg -t get_version
check "an output is active" bash -c 'swaymsg -t get_outputs -r | jq -e "[.[] | select(.active)] | length > 0"'
check "VNC listening on 5900" wait_for bash -c 'ss -ltn | grep -q ":5900 "'
check "notification daemon is running" bash -c 'busctl --user status org.freedesktop.Notifications >/dev/null'

echo "apps as transient services (how hostd starts them)"
# Clear what a previous run may have left. hostd's units use the same
# CollectMode, so a stopped or failed unit never blocks its name.
systemctl --user stop "$UNIT.service" >/dev/null 2>&1
systemctl --user reset-failed "$UNIT.service" >/dev/null 2>&1
before=$(foot_windows 2>/dev/null || echo 0)
check "start foot as $UNIT.service" systemd-run --user --unit="$UNIT" -p Type=exec -p CollectMode=inactive-or-failed foot
check "its window appears in sway" wait_for bash -c "[ \"\$(swaymsg -t get_tree -r | jq '[.. | objects | select(.app_id? == \"foot\")] | length')\" -gt $before ]"
check "window PID maps to the unit via cgroup" bash -c '
	for pid in $(swaymsg -t get_tree -r | jq -r "[.. | objects | select(.app_id? == \"foot\")][].pid"); do
		grep -q "'"$UNIT"'.service" /proc/$pid/cgroup && exit 0
	done; exit 1'
check "stopping the unit closes the window" bash -c "systemctl --user stop $UNIT.service &&
	for i in \$(seq 1 25); do [ \"\$(swaymsg -t get_tree -r | jq '[.. | objects | select(.app_id? == \"foot\")] | length')\" -le $before ] && exit 0; sleep 0.2; done; exit 1"

echo "audio"
check "a default audio sink exists" wait_for wpctl inspect @DEFAULT_AUDIO_SINK@
orig=$(wpctl get-volume @DEFAULT_AUDIO_SINK@ 2>/dev/null)
orig_vol=$(echo "$orig" | awk '{print $2}')
check "set volume to 40%" bash -c 'wpctl set-volume @DEFAULT_AUDIO_SINK@ 0.40 && wpctl get-volume @DEFAULT_AUDIO_SINK@ | grep -q "0.40"'
check "mute and unmute" bash -c 'wpctl set-mute @DEFAULT_AUDIO_SINK@ 1 && wpctl get-volume @DEFAULT_AUDIO_SINK@ | grep -q MUTED &&
	wpctl set-mute @DEFAULT_AUDIO_SINK@ 0 && ! wpctl get-volume @DEFAULT_AUDIO_SINK@ | grep -q MUTED'
if [ -n "$orig_vol" ]; then
	wpctl set-volume @DEFAULT_AUDIO_SINK@ "$orig_vol" >/dev/null 2>&1
	case $orig in *MUTED*) wpctl set-mute @DEFAULT_AUDIO_SINK@ 1 >/dev/null 2>&1 ;; esac
fi

echo "containers"
check "podman API socket answers" bash -c 'curl -fsS --unix-socket "$XDG_RUNTIME_DIR/podman/podman.sock" http://d/_ping | grep -q OK'
check "run a container" bash -c 'podman run --rm docker.io/library/alpine:latest echo hello | grep -q hello'

echo "hostd"
if systemctl --user cat hostd.service >/dev/null 2>&1; then
	check "hostd service is running" systemctl --user is-active --quiet hostd
	check "hostctl talks to hostd" bash -c 'hostctl version | grep -q "^hostd "'
else
	echo "  (hostd not installed as a service here; skipped)"
fi

echo "screenshot"
check "grim captures the screen to $SHOT" grim "$SHOT"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
