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
#   - installs hostd (from the "edge" release, checked against SHA256SUMS)
#     as a user service that starts at boot, and hostctl in /usr/local/bin;
#     the screen user's hostctl is logged in with the first admin token
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
#   --from DIR                install hostd and hostctl from DIR instead of
#                             downloading them (e.g. a local build)
#   -h, --help                show this help
set -euo pipefail

SCREEN_USER=screen
GPU=auto
FROM=""
RELEASE_URL=${HOSTD_RELEASE_URL:-https://github.com/davitizhgenti/hostd/releases/download/edge}
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
	--from) FROM=${2:?--from needs a directory}; shift 2 ;;
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
	podman uidmap fuse-overlayfs passt catatonit nftables
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

# SSH keys: copy the keys of the admin who ran sudo, so whoever
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
log "Installing hostd"

# as_screen runs a command as the screen user, inside its systemd session.
as_screen() {
	runuser -u "$SCREEN_USER" -- env HOME="$SCREEN_HOME" USER="$SCREEN_USER" \
		XDG_RUNTIME_DIR="/run/user/$SCREEN_UID" \
		DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$SCREEN_UID/bus" "$@"
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
if [ -n "$FROM" ]; then
	for cmd in hostd hostctl; do
		if [ -f "$FROM/$cmd-linux-$ARCH" ]; then
			cp "$FROM/$cmd-linux-$ARCH" "$work/$cmd"
		elif [ -f "$FROM/$cmd" ]; then
			cp "$FROM/$cmd" "$work/$cmd"
		else
			die "$FROM has no $cmd or $cmd-linux-$ARCH"
		fi
	done
	info "using the binaries in $FROM"
else
	for f in "hostd-linux-$ARCH" "hostctl-linux-$ARCH" SHA256SUMS; do
		if ! curl -fsSL --retry 3 -o "$work/$f" "$RELEASE_URL/$f" 2>"$work/curl.err"; then
			if grep -q "404" "$work/curl.err"; then
				die "no published build at $RELEASE_URL yet. CI publishes one a few minutes after each push to main; try again shortly"
			fi
			die "cannot download $RELEASE_URL/$f: $(cat "$work/curl.err")"
		fi
	done
	(cd "$work" && sha256sum --check --ignore-missing --quiet SHA256SUMS) || die "downloaded binaries do not match SHA256SUMS"
	mv "$work/hostd-linux-$ARCH" "$work/hostd"
	mv "$work/hostctl-linux-$ARCH" "$work/hostctl"
	info "downloaded from $RELEASE_URL (checksums match)"
fi
chmod 755 "$work/hostd" "$work/hostctl"
VERSION=$("$work/hostd" -version 2>&1 | awk '{print $2}')
[ -n "$VERSION" ] || die "the hostd binary does not run on this machine"

# Versions sit side by side; "current" points at the running one, which is
# what updates and rollbacks switch.
lib=$SCREEN_HOME/.local/lib/hostd
hostd_changed=0
if [ -f "$lib/versions/$VERSION/hostd" ] && cmp -s "$work/hostd" "$lib/versions/$VERSION/hostd"; then
	info "hostd $VERSION already installed"
else
	install -d -o "$SCREEN_USER" -g "$SCREEN_USER" "$SCREEN_HOME/.local" "$SCREEN_HOME/.local/lib" \
		"$lib" "$lib/versions" "$lib/versions/$VERSION"
	install -m 755 -o "$SCREEN_USER" -g "$SCREEN_USER" "$work/hostd" "$lib/versions/$VERSION/hostd"
	changed "installed hostd $VERSION"
	hostd_changed=1
fi
if [ "$(readlink "$lib/current" 2>/dev/null)" != "versions/$VERSION" ]; then
	ln -sfn "versions/$VERSION" "$lib/current"
	chown -h "$SCREEN_USER:$SCREEN_USER" "$lib/current"
	changed "hostd $VERSION is now the current version"
	hostd_changed=1
fi
install_file "$work/hostctl" /usr/local/bin/hostctl 755 root || info "hostctl up to date"
if install_file "$FILES/systemd/hostd.service" "$SCREEN_HOME/.config/systemd/user/hostd.service" 644 "$SCREEN_USER"; then
	hostd_changed=1
else
	info "hostd service up to date"
fi
wants=$SCREEN_HOME/.config/systemd/user/default.target.wants
if [ ! -L "$wants/hostd.service" ]; then
	install -d -o "$SCREEN_USER" -g "$SCREEN_USER" "$wants"
	ln -s ../hostd.service "$wants/hostd.service"
	chown -h "$SCREEN_USER:$SCREEN_USER" "$wants/hostd.service"
	changed "hostd starts at boot"
fi

# Lingering starts the user's systemd; wait for it before talking to it.
for _ in $(seq 1 60); do
	[ -S "/run/user/$SCREEN_UID/bus" ] && break
	sleep 0.5
done
[ -S "/run/user/$SCREEN_UID/bus" ] || die "the $SCREEN_USER user's systemd did not start"
as_screen systemctl --user daemon-reload
if [ $hostd_changed -eq 1 ] || ! as_screen systemctl --user is-active --quiet hostd; then
	# Type=notify: this returns once hostd is serving, or fails.
	if ! as_screen systemctl --user restart hostd; then
		as_screen journalctl --user -u hostd -n 20 --no-pager >&2 || true
		die "hostd did not start"
	fi
	changed "hostd is running"
else
	info "hostd is running"
fi

# On its first start hostd writes an admin token for the first login. Give
# it to the screen user's own hostctl, so hostctl works on this machine and
# can create tokens for other devices.
token_file=/run/user/$SCREEN_UID/hostd-admin-token
if [ -f "$token_file" ] && [ ! -f "$SCREEN_HOME/.config/hostctl/config.toml" ]; then
	as_screen hostctl login "unix:///run/user/$SCREEN_UID/hostd.sock" --token-file "$token_file" >/dev/null
	changed "logged the $SCREEN_USER user's hostctl in"
fi

# ---------------------------------------------------------------------------
log "Done"
if [ $CHANGED -eq 0 ]; then
	info "No changes needed; the system was already set up."
fi
if [ $NEED_REBOOT -eq 1 ]; then
	info "Reboot to finish: sudo reboot"
	info "After the reboot the screen shows a plain dark background, with no login prompt."
fi
ip=$(hostname -I 2>/dev/null | awk '{print $1}')
info "hostd $VERSION is running. hostctl works on this machine as $SCREEN_USER:"
info "  ssh $SCREEN_USER@${ip:-<this-machine>} hostctl apps --all"
info "Other devices (a phone, a script) use the API at http://${ip:-<this-machine>}:7300"
info "with their own token, made with: hostctl token create <name> --scopes read,apps"
