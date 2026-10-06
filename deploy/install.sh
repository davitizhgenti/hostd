#!/bin/bash
# hostd installer: turns a fresh Debian 13 (trixie) machine into a hostd
# machine.
#
#   - enables contrib/non-free and installs the NVIDIA driver when an NVIDIA
#     GPU is present, set up for Sway (KMS, --unsupported-gpu)
#   - installs Sway, PipeWire, Podman, mako, wayvnc and greetd
#   - creates the `screen` user (no sudo) with lingering, copies the admin
#     user's SSH keys to it, and enables its Podman API socket
#   - logs `screen` straight into Sway at boot (greetd on VT 7)
#
# Safe to run again: every step checks first and changes only what is
# missing or different. Files it manages are backed up once as *.orig.
#
# Usage, as root on the machine:
#   sudo ./deploy/install.sh [options]
#
# Options:
#   --gpu auto|nvidia|other   GPU setup (default: auto, from the PCI devices)
#   --user NAME               screen user name (default: screen)
#   -h, --help                show this help
set -euo pipefail

SCREEN_USER=screen
GPU=auto
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
FILES=$HERE/files

CHANGED=0       # set when anything on the system changed
NEED_REBOOT=0   # set when a change only takes effect after a reboot

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
changed() { info "$*"; CHANGED=1; }

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
	case $1 in
	--gpu) GPU=${2:?--gpu needs a value}; shift 2 ;;
	--user) SCREEN_USER=${2:?--user needs a value}; shift 2 ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option: $1 (see --help)" ;;
	esac
done
case $GPU in auto | nvidia | other) ;; *) die "--gpu must be auto, nvidia or other" ;; esac

# install_file SRC DEST MODE OWNER: copy SRC to DEST unless identical.
install_file() {
	local src=$1 dest=$2 mode=$3 owner=$4
	if [ -f "$dest" ] && cmp -s "$src" "$dest"; then
		return 1
	fi
	if [ -f "$dest" ] && [ ! -e "$dest.orig" ]; then
		cp -p "$dest" "$dest.orig"
	fi
	install -D -m "$mode" -o "$owner" -g "$owner" "$src" "$dest"
	changed "wrote $dest"
}

# ---------------------------------------------------------------------------
log "Checking the system"

[ "$(id -u)" -eq 0 ] || die "run as root: sudo $0"
# shellcheck disable=SC1091
. /etc/os-release
[ "${ID:-}" = debian ] && [ "${VERSION_CODENAME:-}" = trixie ] ||
	die "needs Debian 13 (trixie); this is ${PRETTY_NAME:-unknown}"
ARCH=$(dpkg --print-architecture)
info "Debian 13 ($ARCH)"

if [ "$GPU" = auto ]; then
	GPU=other
	for dev in /sys/bus/pci/devices/*; do
		[ -r "$dev/class" ] || continue
		case $(cat "$dev/class") in 0x03*) ;; *) continue ;; esac
		if [ "$(cat "$dev/vendor")" = 0x10de ]; then GPU=nvidia; fi
	done
fi
if [ "$GPU" = nvidia ] && [ "$ARCH" != amd64 ]; then die "the NVIDIA setup needs amd64"; fi
info "GPU setup: $GPU"

# ---------------------------------------------------------------------------
if [ "$GPU" = nvidia ]; then
	log "Enabling contrib and non-free (NVIDIA driver)"
	if [ -f /etc/apt/sources.list.d/debian.sources ]; then
		src=/etc/apt/sources.list.d/debian.sources
		tmp=$(mktemp)
		awk '/^Components:/ {
			if ($0 !~ / contrib( |$)/) $0 = $0 " contrib"
			if ($0 !~ / non-free( |$)/) $0 = $0 " non-free"
		} { print }' "$src" >"$tmp"
	else
		src=/etc/apt/sources.list
		tmp=$(mktemp)
		awk '/^deb(-src)? / && / main( |$)/ {
			if ($0 !~ / contrib( |$)/) $0 = $0 " contrib"
			if ($0 !~ / non-free( |$)/) $0 = $0 " non-free"
		} { print }' "$src" >"$tmp"
	fi
	install_file "$tmp" "$src" 644 root || info "already enabled"
	rm -f "$tmp"
fi

# ---------------------------------------------------------------------------
log "Installing packages"

PACKAGES=(
	sway swaybg xwayland foot greetd mako-notifier libnotify-bin wayvnc grim
	pipewire pipewire-pulse wireplumber rtkit dbus-user-session
	podman uidmap fuse-overlayfs passt catatonit
	jq curl ca-certificates
)
if [ "$GPU" = nvidia ]; then
	PACKAGES+=(linux-headers-amd64 nvidia-driver firmware-misc-nonfree)
fi

missing=()
for p in "${PACKAGES[@]}"; do
	dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q '^install ok installed$' ||
		missing+=("$p")
done
if [ ${#missing[@]} -eq 0 ]; then
	info "all ${#PACKAGES[@]} packages already installed"
else
	info "installing: ${missing[*]}"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -q
	apt-get install -y -q "${missing[@]}"
	CHANGED=1
	for p in "${missing[@]}"; do
		case $p in nvidia-driver | linux-headers-amd64) NEED_REBOOT=1 ;; esac
	done
fi

# ---------------------------------------------------------------------------
if [ "$GPU" = nvidia ]; then
	log "Setting up the NVIDIA driver for Sway"
	tmp=$(mktemp)
	echo "options nvidia-drm modeset=1 fbdev=1" >"$tmp"
	if install_file "$tmp" /etc/modprobe.d/nvidia-drm-kms.conf 644 root; then
		update-initramfs -u
		NEED_REBOOT=1
	else
		info "kernel mode setting already on"
	fi
	rm -f "$tmp"
	SWAY_COMMAND="env WLR_NO_HARDWARE_CURSORS=1 sway --unsupported-gpu"
else
	SWAY_COMMAND="sway"
fi

# ---------------------------------------------------------------------------
log "Setting up the $SCREEN_USER user"

if id "$SCREEN_USER" >/dev/null 2>&1; then
	info "user exists"
else
	useradd -m -s /bin/bash "$SCREEN_USER"
	changed "created user $SCREEN_USER"
fi
for g in video render input; do
	getent group "$g" >/dev/null || groupadd -r "$g"
	if ! id -nG "$SCREEN_USER" | tr ' ' '\n' | grep -qx "$g"; then
		usermod -aG "$g" "$SCREEN_USER"
		changed "added $SCREEN_USER to $g"
	fi
done
if ! grep -q "^$SCREEN_USER:" /etc/subuid || ! grep -q "^$SCREEN_USER:" /etc/subgid; then
	usermod --add-subuids 200000-265535 --add-subgids 200000-265535 "$SCREEN_USER"
	changed "added subordinate IDs for rootless Podman"
fi
if [ -e "/var/lib/systemd/linger/$SCREEN_USER" ]; then
	info "lingering on"
else
	loginctl enable-linger "$SCREEN_USER"
	changed "enabled lingering (user services start at boot)"
fi

SCREEN_HOME=$(getent passwd "$SCREEN_USER" | cut -d: -f6)
SCREEN_UID=$(id -u "$SCREEN_USER")

# SSH keys: copy the keys of the admin who ran sudo, so the laptop that
# reaches the admin account can reach the screen user too.
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
	admin_keys=$(getent passwd "$SUDO_USER" | cut -d: -f6)/.ssh/authorized_keys
	if [ -s "$admin_keys" ]; then
		install -d -m 700 -o "$SCREEN_USER" -g "$SCREEN_USER" "$SCREEN_HOME/.ssh"
		keys=$SCREEN_HOME/.ssh/authorized_keys
		touch "$keys"
		added=0
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			grep -qxF "$line" "$keys" || { echo "$line" >>"$keys"; added=$((added + 1)); }
		done <"$admin_keys"
		chown "$SCREEN_USER:$SCREEN_USER" "$keys"
		chmod 600 "$keys"
		if [ $added -gt 0 ]; then changed "copied $added SSH key(s) from $SUDO_USER"; else info "SSH keys in place"; fi
	else
		info "no SSH keys for $SUDO_USER to copy (add yours with ssh-copy-id first)"
	fi
fi

# Session config.
install_file "$FILES/sway/config" "$SCREEN_HOME/.config/sway/config" 644 "$SCREEN_USER" || info "sway config up to date"
install_file "$FILES/mako/config" "$SCREEN_HOME/.config/mako/config" 644 "$SCREEN_USER" || info "mako config up to date"
install_file "$FILES/systemd/wayvnc.service" "$SCREEN_HOME/.config/systemd/user/wayvnc.service" 644 "$SCREEN_USER" ||
	info "VNC service up to date"

# Rootless Podman API socket, used by hostd's docker runner.
wants=$SCREEN_HOME/.config/systemd/user/sockets.target.wants
if [ -L "$wants/podman.socket" ]; then
	info "Podman socket enabled"
else
	install -d -o "$SCREEN_USER" -g "$SCREEN_USER" "$SCREEN_HOME/.config" "$SCREEN_HOME/.config/systemd" \
		"$SCREEN_HOME/.config/systemd/user" "$wants"
	ln -s /usr/lib/systemd/user/podman.socket "$wants/podman.socket"
	chown -h "$SCREEN_USER:$SCREEN_USER" "$wants/podman.socket"
	changed "enabled the Podman API socket"
	# Start it now if the user manager is already running.
	if [ -d "/run/user/$SCREEN_UID/systemd" ]; then
		systemctl --user --machine="$SCREEN_USER@" start podman.socket 2>/dev/null || true
	fi
fi

# ---------------------------------------------------------------------------
log "Setting up automatic login to Sway"

tmp=$(mktemp)
sed -e "s|@SWAY_COMMAND@|$SWAY_COMMAND|" -e "s|@SCREEN_USER@|$SCREEN_USER|" \
	"$FILES/greetd/config.toml" >"$tmp"
if install_file "$tmp" /etc/greetd/config.toml 644 root; then
	NEED_REBOOT=1
else
	info "greetd config up to date"
fi
rm -f "$tmp"
if systemctl is-enabled --quiet greetd 2>/dev/null; then
	info "greetd enabled"
else
	systemctl enable greetd
	changed "enabled greetd"
	NEED_REBOOT=1
fi

# ---------------------------------------------------------------------------
log "hostd"
info "not installed: hostd has no release yet (milestone M1). Run this again"
info "once it does; the base system above is everything hostd needs."

# ---------------------------------------------------------------------------
log "Done"
if [ $CHANGED -eq 0 ]; then
	info "No changes needed; the system was already set up."
fi
if [ $NEED_REBOOT -eq 1 ]; then
	info "Reboot to finish: sudo reboot"
	info "After the reboot the screen shows a plain dark background, with no login prompt."
fi
info "Check it from the machine as $SCREEN_USER, or from the laptop:"
info "  ssh $SCREEN_USER@<this-machine> 'bash -s' < deploy/check.sh"
