# Shell setup for the screen user inside the devbox.
[ -f /etc/bash.bashrc ] && . /etc/bash.bashrc

export XDG_RUNTIME_DIR=/run/user/1000
export DBUS_SESSION_BUS_ADDRESS=unix:path=$XDG_RUNTIME_DIR/bus
export WAYLAND_DISPLAY=wayland-1
SWAYSOCK=$(ls "$XDG_RUNTIME_DIR"/sway-ipc.*.sock 2>/dev/null | head -1)
export SWAYSOCK
export PATH=/opt/hostd/bin:$PATH

PS1='screen@devbox:\w\$ '
