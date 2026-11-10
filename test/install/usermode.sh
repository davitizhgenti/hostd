#!/bin/bash
# The no-root path, end to end: an admin prepares the machine once
# (hostd-admin), then a user with NO sudo installs, updates and removes hostd.
# Run as root inside a fresh test container (make test-install-user).
set -u
fail=0
t() { if eval "$2" >/dev/null 2>&1; then echo "  PASS $1"; else echo "  FAIL $1"; fail=1; fi; }
dist=/opt/hostd-dist
U=hostduser
as_u() { runuser -u $U -- env HOME=/home/$U XDG_RUNTIME_DIR="/run/user/$(id -u $U)" PATH="/home/$U/.local/bin:/usr/local/bin:/usr/bin:/bin" "$@"; }
hostd_up() { for _ in $(seq 1 30); do as_u hostctl version 2>/dev/null | grep -q "^hostd " && return 0; sleep 1; done; return 1; }

echo "the admin prepares the machine (root, once)"
t "hostd-admin prepare" "SUDO_USER=admin $dist/hostd-admin-linux-amd64 prepare --user $U --gpu other"
t "the user exists, has no sudo" "id $U && ! id -nG $U | grep -qw sudo && ! sudo -l -U $U 2>/dev/null | grep -q 'may run'"
t "lingering, groups, subuid" "test -e /var/lib/systemd/linger/$U && id -nG $U | grep -qw render && grep -q '^$U:' /etc/subuid"
t "hostd-admin stays for later" 'test -x /usr/local/sbin/hostd-admin'
t "nothing of hostd installed yet" "! test -e /home/$U/.local/lib/hostd/current && ! test -e /usr/local/bin/hostctl"
for _ in $(seq 1 30); do test -S "/run/user/$(id -u "$U")/bus" && break; sleep 1; done

echo "the user installs (no root)"
mark=$(mktemp)
t "hostd-setup install, as the user" "as_u $dist/hostd-setup-linux-amd64 install --from $dist --gpu other"
t "it is all in the home" "test -x /home/$U/.local/bin/hostctl && test -x /home/$U/.local/bin/hostd-setup && test -e /home/$U/.local/lib/hostd/current/hostd && test -f /home/$U/.config/hostd/update.conf"
t "nothing outside the home, /tmp and /run was written" "! find / -xdev \( -path /proc -o -path /sys -o -path /dev -o -path /run -o -path /home/$U -o -path /tmp -o -path /opt -o -path /var/lib/systemd -o -path /var/log \) -prune -o -newer $mark -type f -user $U -print 2>/dev/null | grep -q ."
t "hostd runs as the user" "hostd_up && pgrep -u $U -x hostd"
t "hostctl works for the user" 'as_u hostctl apps --all'
t "the update timer is the user's" "as_u systemctl --user is-enabled --quiet hostd-update.timer && ! test -e /etc/systemd/system/hostd-update.timer"
t "a second install changes nothing" "as_u $dist/hostd-setup-linux-amd64 install --from $dist --gpu other | grep -q 'No changes needed'"
t "a root add-on is refused, with the admin command" "as_u hostd-setup add compose 2>&1 | grep -q 'sudo hostd-admin addons add compose'"
t "the admin adds it" "hostd-admin add compose --user $U"
t "now the user's list shows it as installed" "as_u hostd-setup addons | grep -q '^\\* compose'"

echo "update and uninstall (no root)"
t "hostd-setup update" "as_u hostd-setup update --source $dist --from $dist --gpu other"
t "hostd still runs" 'hostd_up'
t "hostd-setup uninstall" "as_u hostd-setup uninstall"
t "service and binaries gone" "! test -e /home/$U/.local/lib/hostd && ! test -e /home/$U/.local/bin/hostctl && ! as_u systemctl --user cat hostd.service"
t "data kept" "test -f /home/$U/.config/hostctl/config.toml"
t "the admin's setup is untouched" 'grep -q "user = \"hostduser\"" /etc/greetd/config.toml && test -x /usr/local/sbin/hostd-admin'
rm -f "$mark"
exit $fail
