#!/bin/bash
# End-to-end test of hostd's menu on the devbox: real keys through VNC,
# checked against hostd's audit log (what the menu did, and as whom).
# Run from the repository on the host after make devbox-hostd:
#   make devbox-menu-test
# It takes over the devbox screen for a minute; do not use VNC meanwhile.
set -u
here=$(dirname "$0")
box=${DEVBOX:-hostd-devbox}
fail=0

hd() { podman exec -u screen "$box" bash -lc "$*"; }
keys() { python3 "$here/vnc.py" "$@"; }
menu_keys() { menu && keys "$@"; }
local_token='HOSTD_TOKEN=$(cat ~/.config/hostd/local.token)'

# expect DESCRIPTION PATTERN STEP...: runs the steps, then waits up to 5 s
# for a new log entry matching PATTERN.
expect() {
	local what=$1 pattern=$2
	shift 2
	local before
	before=$(hd 'hostctl log | tail -1')
	"$@"
	for _ in $(seq 1 25); do
		if hd 'hostctl log | tail -20' | awk -v b="$before" 'f; $0 == b {f=1}' | grep -Eq "$pattern"; then
			echo "  PASS $what"
			return
		fi
		sleep 0.2
	done
	echo "  FAIL $what (no log entry like: $pattern)"
	hd 'hostctl log | tail -5' | sed 's/^/       /'
	fail=1
}

front() { hd 'hostctl windows' | awk '$4 == "true" {print $1}'; }

# menu brings the menu forward through hostd (not F1, which toggles) and
# stops the test unless it is in front: keys must never land in another
# app (a shell, say).
menu() {
	hd "$local_token hostctl start hostd-menu >/dev/null"
	for _ in $(seq 1 20); do
		[ "$(front)" = hostd-menu ] && return
		sleep 0.2
	done
	echo "  FAIL the menu did not come to the front (in front: $(front)); stopping"
	exit 1
}

echo "== a clean start: only the menu"
for i in $(hd 'hostctl ps' | awk 'NR > 1 {print $1}'); do
	hd "hostctl stop '$i' >/dev/null 2>&1" || true
done
if [ -n "$(hd 'hostctl ps' | awk 'NR > 1')" ]; then
	echo "  FAIL could not stop what runs:"; hd 'hostctl ps'; exit 1
fi
hd "$local_token hostctl start hostd-menu >/dev/null"
sleep 2
# An app in front (started as from the screen), so that F1 opens the menu
# rather than going back from it.
hd "$local_token hostctl start clock >/dev/null"
sleep 1.5

expect "F1 brings the menu forward (display.menu, from the keyboard)" \
	'display\.menu .*local \(keyboard\)' keys F1 sleep:1
[ "$(front)" = hostd-menu ] && echo "  PASS the menu is in front" || { echo "  FAIL the menu is not in front: $(front)"; fail=1; }

menu
expect "typing filters, Enter starts the app (as the person at the screen)" \
	'app\.start terminal .*local \(' keys type:term Return sleep:1.5
[ "$(front)" = terminal ] && echo "  PASS the terminal is in front" || { echo "  FAIL in front: $(front)"; fail=1; }

expect "F1 again: back to the menu" 'display\.menu' keys F1 sleep:1
menu

# The menu follows events: with the menu in front, an app started from
# elsewhere opens in the background (keys were just pressed, so someone is
# at the screen) and shows up in the open menu. Filtering for it puts its
# running row first, so Enter focuses it; without the event stream there
# would be no running row, and Enter would start the app instead.
hd 'hostctl start monitor >/dev/null'
sleep 1
[ "$(front)" = hostd-menu ] && echo "  PASS the menu stayed in front" || { echo "  FAIL in front: $(front)"; fail=1; }
expect "the open menu followed an app started elsewhere" \
	'window\.focus monitor .*local \(' keys type:monitor Return sleep:1

menu
expect "Delete closes a running app from the menu" \
	'window\.close monitor .*local \(' keys type:monitor Delete sleep:2.5
hd 'hostctl ps' | grep -q '^monitor ' && { echo "  FAIL monitor still runs"; fail=1; } || echo "  PASS monitor ended"

# A running entry's actions: focus, close, then the app's own (Large text
# first). Shift+F10 opens them; Down Down Enter runs Large text.
expect "Shift+F10 opens an entry's actions; Down, Enter runs one" \
	'app\.start large terminal .*local \(' menu_keys type:term Shift+F10 sleep:0.6 Down Down Return sleep:1.5

expect "Escape goes back (window.back, from the menu)" 'window\.back .*local \(' menu_keys Escape sleep:1

echo "== cleanup"
for i in $(hd 'hostctl ps' | awk 'NR > 1 && $1 != "hostd-menu" {print $1}'); do
	hd "hostctl stop '$i' >/dev/null 2>&1" || true
done
[ $fail -eq 0 ] && echo "menu test passed" || echo "menu test FAILED"
exit $fail
