#!/bin/bash
# Checks the state deploy/install.sh leaves on the test container
# (make test-install). Runs as root inside it.
set -u
fail=0
t() { if eval "$2" >/dev/null 2>&1; then echo "  PASS $1"; else echo "  FAIL $1"; fail=1; fi; }

t "screen user exists, no sudo"        'id screen && ! id -nG screen | grep -qw sudo'
t "screen in video, render, input"     'for g in video render input; do id -nG screen | grep -qw $g || exit 1; done'
t "subordinate IDs for Podman"         'grep -q "^screen:" /etc/subuid && grep -q "^screen:" /etc/subgid'
t "lingering on"                       'test -e /var/lib/systemd/linger/screen'
t "admin SSH key copied to screen"     'grep -q HostdInstallTestKey /home/screen/.ssh/authorized_keys'
t "screen .ssh owned and private"      '[ "$(stat -c "%U %a" /home/screen/.ssh)" = "screen 700" ] && [ "$(stat -c "%U %a" /home/screen/.ssh/authorized_keys)" = "screen 600" ]'
t "sway config installed"              'grep -q "dbus-update-activation-environment --systemd WAYLAND_DISPLAY" /home/screen/.config/sway/config'
t "mako draws on the overlay layer"    'grep -qx "layer=overlay" /home/screen/.config/mako/config'
t "config files owned by screen"       '[ "$(stat -c %U /home/screen/.config/sway/config)" = screen ]'
t "VNC runs as a supervised user service" 'grep -qx "Restart=always" /home/screen/.config/systemd/user/wayvnc.service && grep -q "restart wayvnc.service" /home/screen/.config/sway/config'
t "Podman socket enabled for screen"   'test -L /home/screen/.config/systemd/user/sockets.target.wants/podman.socket'
t "greetd on VT 7, logs in screen"     'grep -qx "vt = 7" /etc/greetd/config.toml && grep -qx "user = \"screen\"" /etc/greetd/config.toml'
t "greetd starts plain sway (no GPU flags)" 'grep -qx "command = \"sway\"" /etc/greetd/config.toml'
t "greetd enabled"                     'systemctl is-enabled greetd'
t "original greetd config backed up"   'test -f /etc/greetd/config.toml.orig'
t "hostd installed as current version" 'test -x /home/screen/.local/lib/hostd/current/hostd && readlink /home/screen/.local/lib/hostd/current | grep -q "^versions/"'
t "rollback script and service installed" 'test -x /home/screen/.local/lib/hostd/rollback.sh && grep -q "OnFailure=hostd-rollback.service" /home/screen/.config/systemd/user/hostd.service && test -f /home/screen/.config/systemd/user/hostd-rollback.service'
t "hostd starts at boot"               'test -L /home/screen/.config/systemd/user/default.target.wants/hostd.service'
t "hostd is running"                   'runuser -u screen -- env XDG_RUNTIME_DIR=/run/user/$(id -u screen) systemctl --user is-active --quiet hostd'
t "hostctl installed for everyone"     'test -x /usr/local/bin/hostctl'
t "screen user hostctl is logged in"   'runuser -u screen -- env HOME=/home/screen XDG_RUNTIME_DIR=/run/user/$(id -u screen) hostctl version | grep -q "^hostd "'
t "admin token file used up"           '! test -e /run/user/$(id -u screen)/hostd-admin-token'
t "screen user hostctl config private" '[ "$(stat -c "%U %a" /home/screen/.config/hostctl/config.toml)" = "screen 600" ]'
t "menu installed"                     'test -x /usr/local/lib/hostd/hostd-menu && ! test -e /usr/local/lib/hostd/hostd-overlay && ! grep -q "^bindsym" /home/screen/.config/sway/config'
t "menu in the catalog, hidden"        'hc() { runuser -u screen -- env HOME=/home/screen XDG_RUNTIME_DIR=/run/user/$(id -u screen) hostctl --json "$@"; }; hc apps --all | jq -e ".apps[] | select(.id == \"hostd-menu\")" && ! hc apps | jq -e ".apps[] | select(.id == \"hostd-menu\")"'
t "menu token: local, private"         '[ "$(stat -c "%U %a" /home/screen/.config/hostd/local.token)" = "screen 600" ] && runuser -u screen -- env HOME=/home/screen XDG_RUNTIME_DIR=/run/user/$(id -u screen) hostctl --json token list | jq -e ".[] | select(.name == \"menu\" and .kind == \"local\")"'
t "hostd config dir owned by screen"   '[ "$(stat -c %U /home/screen/.config/hostd)" = screen ] && [ "$(stat -c %U /home/screen/.config/hostd/apps)" = screen ]'
t "menu runs (GTK 4 bindings)"         'python3 -c "import gi; gi.require_version(\"Gtk\", \"4.0\")"'
t "packages present"                   'for b in sway foot mako wayvnc grim wpctl podman greetd /usr/sbin/nft; do command -v $b || exit 1; done'
exit $fail
