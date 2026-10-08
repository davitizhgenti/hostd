#!/bin/bash
# hostd-setup update and uninstall, end to end, run as the admin user inside
# the install test container after install.sh (make test-install).
set -u
fail=0
ok() { echo "  PASS $1"; }
bad() { echo "  FAIL $1"; fail=1; }
t() { if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1"; fi; }
as_screen() { sudo runuser -u screen -- env HOME=/home/screen XDG_RUNTIME_DIR="/run/user/$(id -u screen)" "$@"; }
hostd_up() {
	for _ in $(seq 1 30); do as_screen hostctl version 2>/dev/null | grep -q "^hostd " && return 0; sleep 1; done
	return 1
}
src=/opt/hostd-src
dist=/opt/hostd-dist

echo "update"
t "hostd-setup is on the PATH" 'command -v hostd-setup'
t "sudo hostd-setup update" "sudo hostd-setup update --source $src --from $dist --gpu other"
t "hostd runs the updated build" 'hostd_up && [ "$(as_screen hostctl version | awk "/^hostd /{print \$2}" | tr -d ,)" = "$(sudo runuser -u screen -- /home/screen/.local/lib/hostd/current/hostd -version 2>&1 | awk "{print \$2}")" ]'
t "a second update changes nothing" "sudo hostd-setup update --source $src --from $dist --gpu other | grep -q 'No changes needed'"

echo "uninstall (keeps data)"
as_screen hostctl token create keepme --scopes read >/dev/null
t "sudo hostd-setup uninstall" 'sudo hostd-setup uninstall'
t "hostd service gone" '! as_screen systemctl --user cat hostd.service'
t "no hostd process" '! pgrep -u screen -x hostd'
t "binaries gone" '! test -e /home/screen/.local/lib/hostd && ! test -e /usr/local/bin/hostctl'
t "hostd-setup gone" '! test -e /usr/local/sbin/hostd-setup && ! test -e /usr/local/share/hostd-setup'
t "data kept" 'sudo test -f /home/screen/.local/state/hostd/state.db && sudo test -f /home/screen/.config/hostctl/config.toml'
t "base setup kept" 'grep -qx "vt = 7" /etc/greetd/config.toml && test -e /var/lib/systemd/linger/screen'

echo "reinstall"
t "install again" "sudo /opt/hostd-deploy/install.sh --gpu other --from $dist"
t "hostd back, with the same tokens" 'hostd_up && as_screen hostctl token list | grep -q keepme'

echo "uninstall --purge --all"
t "sudo hostd-setup uninstall --purge --all" 'sudo hostd-setup uninstall --purge --all'
t "data gone" '! sudo test -e /home/screen/.local/state/hostd && ! sudo test -e /home/screen/.config/hostd'
t "greetd config is the original again (no autologin)" '! grep -q "Managed by hostd" /etc/greetd/config.toml && ! grep -q initial_session /etc/greetd/config.toml'
t "lingering off" '! test -e /var/lib/systemd/linger/screen'
t "managed Sway config removed" '! test -e /home/screen/.config/sway/config'
t "screen user and packages kept" 'id screen && command -v sway'

echo "fresh install after a full uninstall"
t "install" "sudo /opt/hostd-deploy/install.sh --gpu other --from $dist"
t "hostd running with a new admin login" 'hostd_up && as_screen hostctl token list | grep -q admin && ! as_screen hostctl token list | grep -q keepme'
exit $fail
