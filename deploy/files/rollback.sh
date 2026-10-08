#!/bin/sh
# Managed by hostd deploy/install.sh; changes here are overwritten.
#
# Run by hostd-rollback.service when hostd.service fails: switches back to
# the version that ran before the last update, then starts hostd again. It
# never depends on the hostd binary, which is what just failed.
set -u
lib=${HOSTD_LIB:-$HOME/.local/lib/hostd}
prev=$(cat "$lib/previous" 2>/dev/null) || exit 0
cur=$(readlink "$lib/current" 2>/dev/null) || cur=""
if [ -z "$prev" ] || [ "$prev" = "$cur" ] || [ ! -x "$lib/$prev/hostd" ]; then
	echo "hostd-rollback: nothing to roll back to"
	exit 0
fi
ln -sfn "$prev" "$lib/.current.tmp" && mv -T "$lib/.current.tmp" "$lib/current"
echo "$cur" > "$lib/rolled-back-from"
echo "hostd-rollback: $cur failed to start; back to $prev"
systemctl --user reset-failed hostd.service
systemctl --user start hostd.service
