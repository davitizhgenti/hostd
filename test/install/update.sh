#!/bin/bash
# The update path end to end, run as the screen user inside the install
# test container after deploy/install.sh (make test-install): real systemd,
# real hostd.service, real rollback unit.
set -u
XDG_RUNTIME_DIR=/run/user/$(id -u)
export XDG_RUNTIME_DIR
bins=/opt/hostd-test-bins
fail=0
ok() { echo "  PASS $1"; }
bad() { echo "  FAIL $1"; fail=1; }
running_version() { hostctl version 2>/dev/null | awk '/^hostd /{print $2}' | tr -d ','; }

before=$(running_version)
echo "running: $before"

# 1. A good build: installed, restarted into, answers.
if hostctl update push "$bins/hostd-next"; then
	[ "$(running_version)" = "e2e-next" ] && ok "update to a good build" || bad "update to a good build: running $(running_version)"
else
	bad "update to a good build failed"
fi
systemctl --user is-active --quiet hostd && ok "hostd.service active after the update" || bad "hostd.service not active"

# Something in the state database, to find again after the rollback.
hostctl token create before-bad --scopes read >/dev/null || bad "token create"

# 2. A build that passes the version check but cannot start: systemd
#    gives up after three tries, the rollback unit switches back, and
#    hostctl reports the rollback (with a non-zero exit).
if hostctl update push "$bins/hostd-bad"; then
	bad "a broken build was reported as running"
else
	now=$(running_version)
	[ "$now" = "e2e-next" ] && ok "rolled back to the previous version" || bad "after the broken build: running $now"
	hostctl version --json | grep -q '"rolled_back_from": "e2e-bad"' && ok "the rollback is reported" || bad "no rolled_back_from"
fi
[ "$(readlink ~/.local/lib/hostd/current)" = "versions/e2e-next" ] && ok "current points at the good version" ||
	bad "current = $(readlink ~/.local/lib/hostd/current)"
# The state backup made for this update came back, and was used once.
[ -e ~/.local/state/hostd/state.db.from-e2e-bad ] && ok "the state from before the update was restored" ||
	bad "no state.db.from-e2e-bad"
hostctl token list | grep -q before-bad && ok "the restored state has what came before" || bad "token before-bad lost"
[ ! -e ~/.local/lib/hostd/versions/e2e-next/backup-for ] && ok "the backup is used once" || bad "backup-for still there"

# 3. Apps keep running across updates (they are not hostd's children).
exit $fail
