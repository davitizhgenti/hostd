#!/bin/sh
# Managed by hostd deploy/install.sh; changes here are overwritten.
#
# Run by hostd-rollback.service when hostd.service fails: if the run that
# failed was the current version, switches back to the version that ran
# before the last update, then starts hostd again. It never depends on the
# hostd binary, which is what just failed.
set -u
lib=${HOSTD_LIB:-$HOME/.local/lib/hostd}
prev=$(cat "$lib/previous" 2>/dev/null) || exit 0
cur=$(readlink "$lib/current" 2>/dev/null) || cur=""
if [ -z "$prev" ] || [ "$prev" = "$cur" ] || [ ! -x "$lib/$prev/hostd" ]; then
	echo "hostd-rollback: nothing to roll back to"
	exit 0
fi
# Only a failure of the current version counts. The run that failed may
# be the one before it, stopping (an update restarts hostd after switching
# versions): systemd names the failed run, whose log says its version.
inv=${MONITOR_INVOCATION_ID:-}
if [ -n "$inv" ]; then
	ran=$(journalctl --user _SYSTEMD_INVOCATION_ID="$inv" -o cat --no-pager 2>/dev/null |
		sed -n 's/.*msg="hostd started" version=\([^ ]*\).*/\1/p' | tail -1)
	if [ -n "$ran" ] && [ "versions/$ran" != "$cur" ]; then
		echo "hostd-rollback: the run that failed was $ran, not $cur; keeping $cur"
		systemctl --user reset-failed hostd.service
		systemctl --user start hostd.service
		exit 0
	fi
fi
ln -sfn "$prev" "$lib/.current.tmp" && mv -T "$lib/.current.tmp" "$lib/current"
echo "$cur" > "$lib/rolled-back-from"
echo "hostd-rollback: $cur failed to start; back to $prev"

# The data as it was before the update: the new version may have changed
# the database (or the config) in a way the old one cannot read. Only the
# backup made for this very update counts (an update through the installer
# makes none). What the failed version left is kept beside it.
if [ "$(cat "$lib/$prev/backup-for" 2>/dev/null)" = "$cur" ]; then
	state=${HOSTD_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/hostd}
	config=${HOSTD_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/hostd/hostd.toml}
	from=${cur#versions/}
	if [ -f "$lib/$prev/state.db.bak" ]; then
		mkdir -p "$state"
		for f in state.db state.db-wal state.db-shm; do
			[ -e "$state/$f" ] && mv -f "$state/$f" "$state/$f.from-$from"
		done
		cp "$lib/$prev/state.db.bak" "$state/state.db"
		echo "hostd-rollback: the state from before the update is back (the newer one is $state/state.db.from-$from)"
	fi
	if [ -f "$lib/$prev/hostd.toml.bak" ] && ! cmp -s "$lib/$prev/hostd.toml.bak" "$config"; then
		[ -e "$config" ] && cp "$config" "$config.from-$from"
		cp "$lib/$prev/hostd.toml.bak" "$config"
		echo "hostd-rollback: hostd.toml from before the update is back (the newer one is $config.from-$from)"
	fi
	rm -f "$lib/$prev/backup-for"
fi
systemctl --user reset-failed hostd.service
systemctl --user start hostd.service
