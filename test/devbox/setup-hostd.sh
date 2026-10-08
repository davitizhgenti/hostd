#!/bin/bash
# Runs hostd in the devbox the way deploy/install.sh does on the real
# machine: a user service (from bin/, mounted at /opt/hostd/bin), hostctl
# logged in, the switcher with its local token, and a few apps to try.
# Run as the screen user inside the devbox (make devbox-hostd). Idempotent:
# run it again after `make build` to restart hostd on the new binary.
set -euo pipefail
export XDG_RUNTIME_DIR=/run/user/$(id -u)
export DBUS_SESSION_BUS_ADDRESS=unix:path=$XDG_RUNTIME_DIR/bus
export PATH=/opt/hostd/bin:$PATH
cfg=$HOME/.config

mkdir -p "$cfg/systemd/user" "$cfg/hostd/apps"
cat >"$cfg/systemd/user/hostd.service" <<UNIT
[Unit]
Description=hostd (devbox, from bin/)

[Service]
Type=notify
ExecStart=/opt/hostd/bin/hostd
Restart=on-failure
RestartSec=2
KillMode=mixed
# The switcher's scripts call hostctl.
Environment=PATH=/opt/hostd/bin:/usr/local/bin:/usr/bin:/bin
UNIT

# The switcher, straight from the repository: edits apply on its next start.
cat >"$cfg/hostd/apps/hostd-overlay.toml" <<'APP'
name = "Switcher"
hidden = true
runner = { type = "exec", command = ["/opt/hostd-deploy/files/overlay/hostd-overlay"] }
[instance]
policy = "single"
if_running = "focus"
APP

# Apps to try. foot's own entry is a terminal, hidden from the catalog.
cat >"$cfg/hostd/apps/terminal.toml" <<'APP'
name = "Terminal"
runner = { type = "exec", command = ["foot"] }
[instance]
policy = "multiple"
APP
cat >"$cfg/hostd/apps/monitor.toml" <<'APP'
name = "System monitor"
runner = { type = "exec", command = ["foot", "--title", "System monitor", "top"] }
APP
cat >"$cfg/hostd/apps/clock.toml" <<'APP'
name = "Clock"
runner = { type = "exec", command = ["foot", "--title", "Clock", "watch", "-t", "-n1", "date +%H:%M:%S"] }
[window]
fullscreen = false
APP

systemctl --user daemon-reload
systemctl --user restart hostd
echo "hostd: $(hostd -version 2>&1)"

# First start: log hostctl in with the admin token hostd wrote.
if [ ! -f "$cfg/hostctl/config.toml" ]; then
	for _ in $(seq 1 20); do [ -f "$XDG_RUNTIME_DIR/hostd-admin-token" ] && break; sleep 0.5; done
	hostctl login "unix://$XDG_RUNTIME_DIR/hostd.sock" --token-file "$XDG_RUNTIME_DIR/hostd-admin-token" >/dev/null
	echo "hostctl: logged in"
fi
# The switcher's token, kind local (what is chosen on the screen counts as
# someone at the screen).
if [ ! -s "$cfg/hostd/local.token" ]; then
	hostctl --json token create switcher --kind local --scopes read,apps | jq -r .secret >"$cfg/hostd/local.token"
	chmod 600 "$cfg/hostd/local.token"
	echo "switcher: token created"
fi
hostctl app rescan >/dev/null
hostctl apps
