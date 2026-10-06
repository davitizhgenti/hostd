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
t "sway config installed"              'grep -q "import-environment WAYLAND_DISPLAY" /home/screen/.config/sway/config'
t "mako draws on the overlay layer"    'grep -qx "layer=overlay" /home/screen/.config/mako/config'
t "config files owned by screen"       '[ "$(stat -c %U /home/screen/.config/sway/config)" = screen ]'
t "Podman socket enabled for screen"   'test -L /home/screen/.config/systemd/user/sockets.target.wants/podman.socket'
t "greetd on VT 7, logs in screen"     'grep -qx "vt = 7" /etc/greetd/config.toml && grep -qx "user = \"screen\"" /etc/greetd/config.toml'
t "greetd starts plain sway (no GPU flags)" 'grep -qx "command = \"sway\"" /etc/greetd/config.toml'
t "greetd enabled"                     'systemctl is-enabled greetd'
t "original greetd config backed up"   'test -f /etc/greetd/config.toml.orig'
t "packages present"                   'for b in sway foot mako wayvnc grim wpctl podman greetd; do command -v $b || exit 1; done'
exit $fail
