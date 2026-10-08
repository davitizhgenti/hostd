#!/bin/bash
# The M1 gate from the design: start a window app, set the volume, start a
# container, and see all three in hostctl events. Run as the screen user on
# a hostd machine (the real one or the devbox):
#   ssh screen@<machine> 'bash -s' < deploy/m1-gate.sh
#   make m1-gate            (devbox)
# It cleans up after itself and puts the volume back.
set -u
XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
export XDG_RUNTIME_DIR
apps=${XDG_CONFIG_HOME:-$HOME/.config}/hostd/apps
app=m1-gate-web
fail=0
ok() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=1; }

cleanup() {
	hostctl stop foot >/dev/null 2>&1
	hostctl stop "$app" >/dev/null 2>&1
	rm -f "$apps/$app.toml"
	hostctl app rescan >/dev/null 2>&1
	[ -n "${volume:-}" ] && hostctl volume "$volume" >/dev/null 2>&1
}
trap cleanup EXIT

volume=$(hostctl volume 2>/dev/null | sed -n 's/^volume \([0-9]*\)%.*/\1/p')
mkdir -p "$apps"
printf 'runner = { type = "docker", image = "docker.io/library/nginx:alpine" }\n' > "$apps/$app.toml"
hostctl app rescan >/dev/null || { bad "rescan"; exit 1; }

echo "M1 gate"
hostctl start foot >/dev/null && ok "hostctl start foot" || bad "hostctl start foot"
hostctl volume 40 >/dev/null && ok "hostctl volume 40" || bad "hostctl volume 40"
hostctl start "$app" >/dev/null && ok "hostctl start $app (a container)" || bad "hostctl start $app"

# The events, replayed from hostd's recent-events buffer.
events=$(timeout 5 hostctl events --recent --json 2>/dev/null)
has() { echo "$events" | jq -e "select($1)" >/dev/null 2>&1; }
has '.type == "instance.started" and .data.id == "foot" and .action != null' &&
	ok "event: foot started (linked to its action)" || bad "no instance.started for foot"
has '.type == "audio.volume.changed" and .data.percent == 40 and .action != null' &&
	ok "event: volume changed to 40" || bad "no audio.volume.changed to 40"
has ".type == \"instance.started\" and .data.id == \"$app\" and .data.runner == \"docker\"" &&
	ok "event: container started" || bad "no instance.started for $app"
has '.type == "window.opened" and .data.instance == "foot"' &&
	ok "event: foot's window opened on its own workspace" || bad "no window.opened for foot"

echo
[ $fail -eq 0 ] && echo "M1 gate passed" || echo "M1 gate FAILED"
exit $fail
