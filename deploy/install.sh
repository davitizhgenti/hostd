#!/bin/bash
# hostd installer: turns a fresh Debian 13 (trixie) machine into a hostd
# machine. This script only fetches the installer itself, hostd-setup (a
# signed program of the same build; deploy/*.go), checks its signature and
# runs it with these arguments:
#
#   sudo ./deploy/install.sh [options]      first install (see hostd-setup --help)
#   sudo ./deploy/install.sh --from DIR     with a build in DIR (make dist)
#
# hostd-setup then stays on the machine for updates and an uninstall:
#   sudo hostd-setup update | uninstall | addons | add NAME | remove NAME
#
# The installer of earlier versions (a shell script) runs this file for its
# updates, with HOSTD_RELEASE_URL and the like in the environment: they pass
# through to hostd-setup.
set -euo pipefail

die() {
	printf '\033[31merror:\033[0m %s\n' "$*" >&2
	exit 1
}
[ "$(id -u)" -eq 0 ] || die "run as root: sudo $0 $*"

# The key builds are signed with (Ed25519; CI holds the private half, and
# checks this copy matches deploy/release-key.pem). HOSTD_RELEASE_KEY
# replaces it, for tests with a key of their own.
RELEASE_KEY="${HOSTD_RELEASE_KEY:-}"
[ -n "$RELEASE_KEY" ] || RELEASE_KEY="-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAc5GeAgxeWgwQ5JnhYERGeBTMgDWk45+Nfv+FfVr5WiQ=
-----END PUBLIC KEY-----"

FROM=""
prev=""
for a in "$@"; do
	[ "$prev" = --from ] && FROM=$a
	prev=$a
done

ARCH=$(dpkg --print-architecture)
NAME=hostd-setup-linux-$ARCH

# A build at hand (--from): its hostd-setup is the one to run.
if [ -n "$FROM" ] && [ -x "$FROM/$NAME" ]; then
	exec "$FROM/$NAME" "$@"
fi

# Otherwise the newest signed build of the configured channel.
CHANNEL=edge
if [ -f /etc/hostd/update.conf ]; then
	c=$(sed -n 's/^CHANNEL=\([a-z]*\).*/\1/p' /etc/hostd/update.conf | tail -1)
	CHANNEL=${c:-edge}
fi
case $CHANNEL in
stable) DEFAULT_URL=https://github.com/davitizhgenti/hostd/releases/latest/download ;;
*) DEFAULT_URL=https://github.com/davitizhgenti/hostd/releases/download/edge ;;
esac
RELEASE_URL=${HOSTD_RELEASE_URL:-$DEFAULT_URL}

# A minimal Debian may have neither a downloader nor openssl.
need=()
command -v curl >/dev/null || command -v wget >/dev/null || need+=(curl ca-certificates)
command -v openssl >/dev/null || need+=(openssl)
if [ ${#need[@]} -gt 0 ]; then
	echo "installing ${need[*]} (to fetch the installer)"
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q "${need[@]}" >/dev/null ||
		{ apt-get update -q >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -q "${need[@]}" >/dev/null; } ||
		die "could not install ${need[*]}"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch() { # FILE
	case $RELEASE_URL in
	file://*) cp "${RELEASE_URL#file://}/$1" "$tmp/$1" 2>/dev/null || die "no $1 in the build at $RELEASE_URL" ;;
	*)
		if command -v curl >/dev/null; then
			curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o "$tmp/$1" "$RELEASE_URL/$1"
		else
			wget -q --tries=7 --waitretry=5 -O "$tmp/$1" "$RELEASE_URL/$1"
		fi || die "cannot download $RELEASE_URL/$1 (CI publishes a build a few minutes after each push to main)"
		;;
	esac
}
fetch SHA256SUMS
fetch SHA256SUMS.sig
fetch "$NAME"
printf '%s\n' "$RELEASE_KEY" >"$tmp/key.pem"
openssl pkeyutl -verify -pubin -inkey "$tmp/key.pem" -rawin -in "$tmp/SHA256SUMS" -sigfile "$tmp/SHA256SUMS.sig" >/dev/null 2>&1 ||
	die "the build at $RELEASE_URL is not signed with hostd's key: not installing it"
grep "  $NAME\$" "$tmp/SHA256SUMS" >"$tmp/check.sha256" || die "the signed SHA256SUMS does not list $NAME"
(cd "$tmp" && sha256sum --check --quiet check.sha256) || die "$NAME does not match the signed SHA256SUMS"
chmod 755 "$tmp/$NAME"
# Not exec: the trap must remove the download afterwards.
HOSTD_RELEASE_URL=$RELEASE_URL "$tmp/$NAME" "$@"
