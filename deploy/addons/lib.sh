# shellcheck shell=bash
# The helpers an add-on hook has, run by hostd-setup (deploy/addons.go) as
#   bash lib.sh addon_install|addon_remove
# with the add-on's files in ADDON_HERE and its hook script ADDON_HOOK.
# Lines that start with \001 and a letter go to the installer's output:
# I information, C a change, W a warning, E an error, R a reboot needed.
set -euo pipefail

# ROOT is put in front of the machine's paths: empty on a machine, a
# directory in the hooks' tests (deploy/hooks_test.go).
ROOT=${HOSTD_ROOT:-}

info() { printf '\001I%s\n' "$*"; }
log() { printf '\001I%s\n' "$*"; }
changed() { printf '\001C%s\n' "$*"; }
warn() { printf '\001W%s\n' "$*"; }
die() {
	printf '\001E%s\n' "$*"
	exit 1
}

# hostd_owned FILE: whether hostd wrote FILE (it says so, or it is in
# /usr/local, where the system's packages never put files).
hostd_owned() {
	case ${1#"$ROOT"} in /usr/local/*) return 0 ;; esac
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

# as_screen runs a command as the screen user, inside its systemd session.
as_screen() {
	runuser -u "$SCREEN_USER" -- env HOME="$SCREEN_HOME" USER="$SCREEN_USER" \
		XDG_RUNTIME_DIR="/run/user/$SCREEN_UID" \
		DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$SCREEN_UID/bus" "$@"
}

# addon_rescan has hostd read the app catalog again.
addon_rescan() {
	[ -S "$ROOT/run/user/$SCREEN_UID/hostd.sock" ] && as_screen hostctl app rescan >/dev/null 2>&1 || true
}

# The add-ons asked for by name in this run (hostd-setup add, update NAME).
read -ra ADDON_NAMES <<<"${HOSTD_ADDING:-} ${HOSTD_FORCE_ADDONS:-}"
export ADDON_NAMES
NEED_REBOOT=0

# shellcheck disable=SC1090
. "$ADDON_HOOK"
"$1"
if [ "$NEED_REBOOT" = 1 ]; then printf '\001R\n'; fi
