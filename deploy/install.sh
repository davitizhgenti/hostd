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
# The first install also installs this script as `hostd-setup`, so later:
#   sudo hostd-setup update      fetch the latest installer and run it
#   sudo hostd-setup uninstall   remove hostd (keeps your data and the base
#                                system; see below)
#
# Add-ons (deploy/addons) install optional things on top: apps such as
# Steam or Dolphin, game-controller support. Installed ones are kept up to
# date by every install and update.
#   sudo hostd-setup addons             list them, and which are installed
#   sudo hostd-setup add NAME...        install (with what they need), as
#                                       part of an update
#   sudo hostd-setup remove NAME...     remove
#
# Usage, as root on the machine:
#   sudo ./deploy/install.sh [install] [options]
#   sudo hostd-setup update [options]
#   sudo hostd-setup uninstall [--purge] [--all]
#   sudo hostd-setup addons | add NAME... | remove NAME...
#
# Options:
#   --gpu auto|nvidia|other   GPU setup (default: auto, from the PCI devices)
#   --user NAME               screen user name (default: screen)
#   --from DIR                install hostd and hostctl from DIR instead of
#                             downloading them (e.g. a local build)
#   --source DIR              update, add: run DIR/deploy/install.sh (a
#                             checkout) instead of downloading the latest one
#   --purge                   uninstall: also delete hostd's data (tokens,
#                             audit trail, app files, hostd.toml)
#   --all                     uninstall: also undo the base setup (autologin
#                             to Sway, managed configs, lingering)
#   -h, --help                show this help
#
# Uninstall never removes system packages, the NVIDIA driver, or the screen
# user and its home: they may hold things that are not hostd's.
set -euo pipefail

SCREEN_USER=screen
GPU=auto
FROM=""
SOURCE=""
PURGE=0
ALL=0
CMD=install
SETUP_DIR=/usr/local/share/hostd-setup
ADDONS_STATE=/etc/hostd/addons # installed add-ons, one name per line
ADDON_NAMES=()
SOURCE_URL=${HOSTD_SOURCE_URL:-https://github.com/davitizhgenti/hostd/archive/refs/heads/main.tar.gz}
RELEASE_URL=${HOSTD_RELEASE_URL:-https://github.com/davitizhgenti/hostd/releases/download/edge}
# Resolve the hostd-setup symlink: the files live next to the real script.
HERE=$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)
FILES=$HERE/files

CHANGED=0       # set when anything on the system changed
NEED_REBOOT=0   # set when a change only takes effect after a reboot

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
changed() { info "$*"; CHANGED=1; }

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; }

case ${1:-} in
install | update | uninstall | addons | add | remove) CMD=$1; shift ;;
esac
PASS_ARGS=() # options handed to the latest installer by update
while [ $# -gt 0 ]; do
	case $1 in
	--gpu) GPU=${2:?--gpu needs a value}; PASS_ARGS+=("$1" "$2"); shift 2 ;;
	--user) SCREEN_USER=${2:?--user needs a value}; PASS_ARGS+=("$1" "$2"); shift 2 ;;
	--from) FROM=${2:?--from needs a directory}; PASS_ARGS+=("$1" "$2"); shift 2 ;;
	--source) SOURCE=${2:?--source needs a directory}; shift 2 ;;
	--purge) PURGE=1; shift ;;
	--all) ALL=1; shift ;;
	-h | --help) usage; exit 0 ;;
	-*) die "unknown option: $1 (see --help)" ;;
	*)
		case $CMD in add | remove) ADDON_NAMES+=("$1"); shift ;; *) die "unexpected argument: $1 (see --help)" ;; esac
		;;
	esac
done
# The add-ons an add asked for, handed over by the installer that ran this
# one (update runs the latest installer).
if [ "$CMD" = install ] && [ -n "${HOSTD_ADDING:-}" ]; then
	read -ra ADDON_NAMES <<<"$HOSTD_ADDING"
fi
if [ "$CMD" != uninstall ] && { [ $PURGE -eq 1 ] || [ $ALL -eq 1 ]; }; then
	die "--purge and --all go with uninstall"
fi
case $GPU in auto | nvidia | other) ;; *) die "--gpu must be auto, nvidia or other" ;; esac

# hostd_owned FILE: whether hostd wrote FILE (it says so, or it is in
# /usr/local, where the system's packages never put files).
hostd_owned() {
	case $1 in /usr/local/*) return 0 ;; esac
	grep -qs "Managed by hostd" "$1"
}

# install_file SRC DEST MODE OWNER: copy SRC to DEST unless identical. A
# file hostd did not write is kept as DEST.orig the first time.
install_file() {
	local src=$1 dest=$2 mode=$3 owner=$4
	if [ -f "$dest" ] && cmp -s "$src" "$dest"; then
		return 1
	fi
	if [ -f "$dest" ] && [ ! -e "$dest.orig" ] && ! hostd_owned "$dest"; then
		cp -p "$dest" "$dest.orig"
	fi
	install -D -m "$mode" -o "$owner" -g "$owner" "$src" "$dest"
	changed "wrote $dest"
}

[ "$(id -u)" -eq 0 ] || die "run as root: sudo $0 $CMD"

# --- add-ons ----------------------------------------------------------------
# An add-on is deploy/addons/NAME/addon.sh, which sets ADDON_DESCRIPTION and
# ADDON_REQUIRES (other add-ons) and defines addon_install and addon_remove.
# Both run as root with this script's helpers, and must be safe to run
# again (install runs on every install and update).
ADDONS_DIR=$HERE/addons

addon_load() {
	local f=$ADDONS_DIR/$1/addon.sh
	[ -f "$f" ] || die "no add-on \"$1\"; see: hostd-setup addons"
	unset -f addon_install addon_remove
	# shellcheck disable=SC2034 # ADDON_HERE is for the add-on's own files
	ADDON_DESCRIPTION="" ADDON_REQUIRES="" ADDON_HERE=$ADDONS_DIR/$1
	# shellcheck disable=SC1090
	. "$f"
}

# addon_rescan has hostd read the app catalog again: a Flatpak app lands in
# a directory hostd may not have been watching (it did not exist yet).
addon_rescan() {
	[ -S "/run/user/$SCREEN_UID/hostd.sock" ] && as_screen hostctl app rescan >/dev/null 2>&1 || true
}

addons_installed() { [ -f "$ADDONS_STATE" ] && grep -v '^#' "$ADDONS_STATE" | sed '/^$/d' || true; }

# addon_order NAME...: sets ORDER to the names with what they require,
# requirements first. (Not in a $(...) subshell: an unknown name must stop
# the installer, and die in a subshell would only end the subshell.)
addon_order() {
	local seen=" " n
	ORDER=()
	visit() {
		case $seen in *" $1 "*) return ;; esac
		seen="$seen$1 "
		addon_load "$1"
		local req
		for req in $ADDON_REQUIRES; do visit "$req"; done
		ORDER+=("$1")
	}
	for n in "$@"; do visit "$n"; done
}

# ensure_packages PKG...: installs the Debian packages that are missing.
ensure_packages() {
	local missing=() p
	for p in "$@"; do
		dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q '^install ok installed$' || missing+=("$p")
	done
	[ ${#missing[@]} -eq 0 ] && return 0
	info "installing: ${missing[*]}"
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q "${missing[@]}" >/dev/null ||
		{ apt-get update -q >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -q "${missing[@]}" >/dev/null; } ||
		die "could not install ${missing[*]}"
	changed "installed ${missing[*]}"
}

if [ "$CMD" = addons ]; then
	installed=" $(addons_installed | tr '\n' ' ') "
	for d in "$ADDONS_DIR"/*/; do
		n=$(basename "$d")
		addon_load "$n"
		mark=" "
		case $installed in *" $n "*) mark="*" ;; esac
		printf '%s %-12s %s\n' "$mark" "$n" "$ADDON_DESCRIPTION"
		[ -n "$ADDON_REQUIRES" ] && printf '  %-12s (with: %s)\n' "" "$ADDON_REQUIRES"
	done
	echo "(* installed)  sudo hostd-setup add NAME | remove NAME"
	exit 0
fi

if [ "$CMD" = add ]; then
	[ ${#ADDON_NAMES[@]} -gt 0 ] || die "add what? see: hostd-setup addons"
	mkdir -p "$(dirname "$ADDONS_STATE")"
	addon_order "${ADDON_NAMES[@]}"
	for n in "${ORDER[@]}"; do
		addons_installed | grep -qx "$n" || echo "$n" >>"$ADDONS_STATE"
	done
	# The add-ons are applied at the end of an install run. Like update, it
	# is the latest installer's (or --source's): everything else comes up
	# to date with it, and one version of the installer does it all.
	CMD=update
fi

# as_screen runs a command as the screen user, inside its systemd session.
as_screen() {
	runuser -u "$SCREEN_USER" -- env HOME="$SCREEN_HOME" USER="$SCREEN_USER" \
		XDG_RUNTIME_DIR="/run/user/$SCREEN_UID" \
		DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$SCREEN_UID/bus" "$@"
}

# ---------------------------------------------------------------------------
# update: run the latest installer. It updates hostd, hostctl and whatever
# else changed (unit files, configs), and changes nothing that is current.
if [ "$CMD" = update ]; then
	if [ -n "$SOURCE" ]; then
		next="$SOURCE/deploy/install.sh"
		[ -x "$next" ] || die "$next not found"
	else
		tmp=$(mktemp -d)
		trap 'rm -rf "$tmp"' EXIT
		log "Fetching the latest installer"
		curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors "$SOURCE_URL" | tar xz -C "$tmp" || die "cannot download $SOURCE_URL"
		next=$(find "$tmp" -maxdepth 3 -path '*/deploy/install.sh' | head -1)
		[ -n "$next" ] || die "the download has no deploy/install.sh"
	fi
	# Not exec: the trap must remove the download afterwards. The add-ons
	# just asked for (add) go along: some act only when asked for by name.
	HOSTD_ADDING="${ADDON_NAMES[*]}" bash "$next" install "${PASS_ARGS[@]}"
	exit $?
fi

# ---------------------------------------------------------------------------
# remove: undo add-ons
if [ "$CMD" = remove ]; then
	[ ${#ADDON_NAMES[@]} -gt 0 ] || die "remove what? see: hostd-setup addons"
	SCREEN_HOME=$(getent passwd "$SCREEN_USER" | cut -d: -f6)
	SCREEN_UID=$(id -u "$SCREEN_USER")
	CHANGED=0
	for n in "${ADDON_NAMES[@]}"; do
		addons_installed | grep -qx "$n" || die "add-on \"$n\" is not installed"
		for other in $(addons_installed); do
			[ "$other" = "$n" ] && continue
			addon_load "$other"
			case " $ADDON_REQUIRES " in *" $n "*) die "$other needs $n: remove $other first" ;; esac
		done
		log "Removing add-on $n"
		addon_load "$n"
		addon_remove
		sed -i "/^$n\$/d" "$ADDONS_STATE"
	done
	log "Done"
	exit 0
fi

# ---------------------------------------------------------------------------
# uninstall
if [ "$CMD" = uninstall ]; then
	log "Removing hostd"
	if id "$SCREEN_USER" >/dev/null 2>&1; then
		SCREEN_HOME=$(getent passwd "$SCREEN_USER" | cut -d: -f6)
		SCREEN_UID=$(id -u "$SCREEN_USER")
		units=$SCREEN_HOME/.config/systemd/user
		if [ -S "/run/user/$SCREEN_UID/bus" ]; then
			as_screen systemctl --user disable --now hostd.service >/dev/null 2>&1 || true
			# Apps hostd started run in their own units and containers.
			for u in $(as_screen systemctl --user list-units --plain --no-legend 'hostd-*.service' 2>/dev/null | awk '{print $1}'); do
				as_screen systemctl --user stop "$u" >/dev/null 2>&1 || true
			done
			for c in $(as_screen podman ps -aq --filter label=hostd.instance 2>/dev/null); do
				as_screen podman rm -f "$c" >/dev/null 2>&1 || true
			done
			info "stopped hostd and the apps it started"
		fi
		rm -f "$units/hostd.service" "$units/hostd-rollback.service" "$units/default.target.wants/hostd.service"
		rm -rf "$SCREEN_HOME/.local/lib/hostd"
		[ -S "/run/user/$SCREEN_UID/bus" ] && as_screen systemctl --user daemon-reload || true
		rm -f "/run/user/$SCREEN_UID/hostd.sock" "/run/user/$SCREEN_UID/hostd-admin-token"
		info "removed the hostd service and its versions"
		if [ $PURGE -eq 1 ]; then
			rm -rf "$SCREEN_HOME/.local/state/hostd" "$SCREEN_HOME/.config/hostd" "$SCREEN_HOME/.config/hostctl"
			info "deleted hostd's data: tokens, audit trail, app files, hostd.toml, hostctl login"
		else
			# Kept together: the tokens, and the login that uses one, so a
			# reinstall continues where this left off.
			info "kept hostd's data (tokens, audit trail, app files, hostctl login); --purge deletes it"
		fi
		if [ $ALL -eq 1 ]; then
			log "Undoing the base setup"
			if [ -f /etc/greetd/config.toml.orig ]; then
				mv /etc/greetd/config.toml.orig /etc/greetd/config.toml
				info "restored the original greetd config (no more autologin to Sway)"
			fi
			for f in sway/config mako/config; do
				if [ -f "$SCREEN_HOME/.config/$f.orig" ]; then
					mv "$SCREEN_HOME/.config/$f.orig" "$SCREEN_HOME/.config/$f"
				else
					rm -f "$SCREEN_HOME/.config/$f"
				fi
			done
			rm -f "$units/wayvnc.service" "$units/sockets.target.wants/podman.socket"
			loginctl disable-linger "$SCREEN_USER" 2>/dev/null || true
			info "removed the managed Sway, mako and VNC configs, the Podman socket, and lingering"
		fi
	fi
	rm -f /usr/local/bin/hostctl /usr/local/sbin/hostd-setup /usr/local/bin/hostd-switch
	rm -rf "$SETUP_DIR" /usr/local/lib/hostd
	rm -f "$SCREEN_HOME/.config/hostd/apps/hostd-menu.toml" "$SCREEN_HOME/.config/hostd/apps/hostd-overlay.toml"
	info "removed hostctl, the menu and hostd-setup"
	log "Done"
	info "Not removed: system packages, the NVIDIA driver, and the $SCREEN_USER user."
	info "To remove the user and everything in its home as well: sudo userdel -r $SCREEN_USER"
	[ $ALL -eq 1 ] && info "Reboot to leave the Sway session: sudo reboot"
	exit 0
fi

# ---------------------------------------------------------------------------
log "Checking the system"
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
	python3-gi gir1.2-gtk-4.0
)
if [ "$GPU" = nvidia ]; then
	PACKAGES+=(linux-headers-amd64 firmware-misc-nonfree)
	# NVIDIA's own driver (the nvidia add-on) replaces Debian's: keep it.
	[ -x /usr/bin/nvidia-uninstall ] || PACKAGES+=(nvidia-driver)
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
	# start-sway picks the renderer for the driver (see the file).
	if install_file "$FILES/start-sway" /usr/local/lib/hostd/start-sway 755 root; then
		NEED_REBOOT=1
	else
		info "Sway start script up to date"
	fi
	SWAY_COMMAND="/usr/local/lib/hostd/start-sway"
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
# A running session picks up changes at once (exec lines do not run again).
if install_file "$FILES/sway/config" "$SCREEN_HOME/.config/sway/config" 644 "$SCREEN_USER"; then
	for sock in /run/user/"$SCREEN_UID"/sway-ipc.*.sock; do
		[ -S "$sock" ] && as_screen env SWAYSOCK="$sock" swaymsg reload >/dev/null 2>&1 && info "reloaded the running Sway" || true
	done
else
	info "sway config up to date"
fi
if install_file "$FILES/mako/config" "$SCREEN_HOME/.config/mako/config" 644 "$SCREEN_USER"; then
	as_screen makoctl reload >/dev/null 2>&1 && info "reloaded mako" || true
else
	info "mako config up to date"
fi
vnc_changed=0
if install_file "$FILES/systemd/wayvnc.service" "$SCREEN_HOME/.config/systemd/user/wayvnc.service" 644 "$SCREEN_USER"; then
	vnc_changed=1
else
	info "VNC service up to date"
fi

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
		if ! curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o "$work/$f" "$RELEASE_URL/$f" 2>"$work/curl.err"; then
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
old=$(readlink "$lib/current" 2>/dev/null || true)
if [ "$old" != "versions/$VERSION" ]; then
	if [ -n "$old" ]; then # what a rollback returns to
		echo "$old" >"$lib/previous"
		chown "$SCREEN_USER:$SCREEN_USER" "$lib/previous"
	fi
	ln -sfn "versions/$VERSION" "$lib/current"
	chown -h "$SCREEN_USER:$SCREEN_USER" "$lib/current"
	rm -f "$lib/rolled-back-from"
	changed "hostd $VERSION is now the current version"
	hostd_changed=1
fi
install_file "$FILES/rollback.sh" "$lib/rollback.sh" 755 "$SCREEN_USER" || info "rollback script up to date"
install_file "$FILES/systemd/hostd-rollback.service" "$SCREEN_HOME/.config/systemd/user/hostd-rollback.service" 644 "$SCREEN_USER" ||
	info "rollback service up to date"
install_file "$work/hostctl" /usr/local/bin/hostctl 755 root || info "hostctl up to date"
# hostd's on-screen menu: a hidden app in the catalog, opened by hostd's
# display.menu action (Super, or a controller's Guide button). The files
# of its old name (the "switcher", hostd-overlay) go.
rm -f /usr/local/lib/hostd/hostd-overlay "$SCREEN_HOME/.config/hostd/apps/hostd-overlay.toml"
menu_changed=0
if install_file "$FILES/menu/hostd-menu" /usr/local/lib/hostd/hostd-menu 755 root; then
	menu_changed=1
else
	info "menu up to date"
fi
rm -f /usr/local/bin/hostd-switch # replaced by hostd's own key bindings
# Backups that older installers made of hostd's own files.
for f in /usr/local/bin/hostctl.orig /usr/local/bin/hostd-steam-session.orig /usr/local/lib/hostd/*.orig \
	/usr/local/lib/hostd/addons/*.orig "$SCREEN_HOME"/.config/hostd/apps/*.orig; do
	if [ -f "$f" ] && hostd_owned "$f"; then
		rm -f "$f"
		changed "removed $f (a backup of hostd's own file)"
	fi
done
install -d -o "$SCREEN_USER" -g "$SCREEN_USER" "$SCREEN_HOME/.config/hostd" "$SCREEN_HOME/.config/hostd/apps"
if install_file "$FILES/menu/hostd-menu.toml" "$SCREEN_HOME/.config/hostd/apps/hostd-menu.toml" 644 "$SCREEN_USER"; then
	menu_changed=1
else
	info "menu app file up to date"
fi
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
# A running VNC server picks up a changed unit only when restarted.
if [ $vnc_changed -eq 1 ] && as_screen systemctl --user is-active --quiet wayvnc.service; then
	as_screen systemctl --user restart wayvnc.service && changed "restarted the VNC server"
fi
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
# The menu's token: kind "local", so what is chosen on the screen
# counts as someone at the screen (it comes to the front, it outranks
# phones and scripts).
local_token=$SCREEN_HOME/.config/hostd/local.token
if [ ! -s "$local_token" ] && [ -f "$SCREEN_HOME/.config/hostctl/config.toml" ]; then
	secret=$(as_screen hostctl --json token create menu --kind local --scopes read,apps | jq -r .secret)
	case $secret in
	hostd_*)
		install -D -m 600 -o "$SCREEN_USER" -g "$SCREEN_USER" /dev/null "$local_token"
		printf '%s\n' "$secret" >"$local_token"
		changed "created the menu's token"
		;;
	*) info "warning: could not create the menu's token; the menu will not work" ;;
	esac
fi

# A running menu keeps its old code: restart it. If it was on the screen,
# it comes back there (as if opened from the screen); otherwise it waits
# for the next Super or Guide press.
if [ $menu_changed -eq 1 ] && [ -s "$local_token" ] &&
	as_screen hostctl ps 2>/dev/null | awk 'NR > 1 {print $1}' | grep -qx hostd-menu; then
	in_front=$(as_screen hostctl windows 2>/dev/null | awk '$1 == "hostd-menu" {print $4}')
	as_screen hostctl stop hostd-menu >/dev/null 2>&1 || true
	if [ "$in_front" = true ]; then
		as_screen env HOSTD_TOKEN="$(cat "$local_token")" hostctl start hostd-menu >/dev/null 2>&1 || true
	fi
	changed "restarted the menu on its new version"
fi

# ---------------------------------------------------------------------------
log "Installing hostd-setup (update and uninstall)"
# A copy of this installer, so `sudo hostd-setup update` and `uninstall`
# work without a checkout.
if [ "$HERE" != "$SETUP_DIR" ]; then
	if [ -d "$SETUP_DIR" ] && diff -rq "$HERE" "$SETUP_DIR" >/dev/null 2>&1; then
		info "hostd-setup up to date"
	else
		rm -rf "$SETUP_DIR"
		mkdir -p "$SETUP_DIR"
		cp -r "$HERE/." "$SETUP_DIR/"
		changed "installed hostd-setup in $SETUP_DIR"
	fi
fi
if [ "$(readlink /usr/local/sbin/hostd-setup 2>/dev/null)" != "$SETUP_DIR/install.sh" ]; then
	ln -sfn "$SETUP_DIR/install.sh" /usr/local/sbin/hostd-setup
	changed "hostd-setup is on the PATH"
fi

# ---------------------------------------------------------------------------
# Add-ons: each installed one again, which also updates it.
if [ -n "$(addons_installed)" ]; then
	mapfile -t recorded < <(addons_installed)
	addon_order "${recorded[@]}"
	for n in "${ORDER[@]}"; do
		log "Add-on: $n"
		addon_load "$n"
		addon_install
	done
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
info "Later: sudo hostd-setup update (latest version), sudo hostd-setup uninstall"
