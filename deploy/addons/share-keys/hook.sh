# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers
# share-keys' hook (addon.toml has the rest): the pinned lan-mouse binary and
# the screen user's config, written once.
LANMOUSE_VERSION=0.11.0
LANMOUSE_URL="https://github.com/feschber/lan-mouse/releases/download/v${LANMOUSE_VERSION}/lan-mouse-linux-x86_64"
LANMOUSE_SHA256=c43d211ec1af3eaea0cd585ac5c415776000355c3fcb9742fa2513f32dab55ba
LANMOUSE_BIN=$ROOT/usr/local/lib/hostd/share-keys/lan-mouse

lanmouse_binary() {
	if [ -x "$LANMOUSE_BIN" ] && [ "$(cat "$LANMOUSE_BIN.version" 2>/dev/null)" = "$LANMOUSE_VERSION" ]; then
		info "lan-mouse $LANMOUSE_VERSION is installed"
		return 0
	fi
	[ "$(dpkg --print-architecture)" = amd64 ] || die "this add-on has the amd64 build of lan-mouse only"
	local tmp
	tmp=$(mktemp -d)
	curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o "$tmp/lan-mouse" "$LANMOUSE_URL" ||
		die "cannot download $LANMOUSE_URL"
	(cd "$tmp" && echo "$LANMOUSE_SHA256  lan-mouse" | sha256sum --check --quiet -) ||
		die "$LANMOUSE_URL is not the file this add-on pins (sha256 $LANMOUSE_SHA256)"
	install -D -m 755 -o root -g root "$tmp/lan-mouse" "$LANMOUSE_BIN"
	printf '%s\n' "$LANMOUSE_VERSION" >"$LANMOUSE_BIN.version"
	rm -rf "$tmp"
	changed "installed lan-mouse $LANMOUSE_VERSION"
}

# lanmouse_config writes a starting config once; a person's edits stay.
lanmouse_config() {
	local conf=$SCREEN_HOME/.config/lan-mouse/config.toml tmp
	[ -e "$conf" ] && return 0
	tmp=$(mktemp)
	cat >"$tmp" <<'CONF'
# Written by hostd's share-keys add-on once; your changes stay.
# Core receives: a PC's keyboard and mouse control it. Authorize the PC by its
# certificate fingerprint (shown by lan-mouse on the PC), then restart with
# hostctl stop share-keys && hostctl start share-keys.
port = 4242

[authorized_fingerprints]
# "aa:bb:cc:...:ff" = "my-pc"
CONF
	install -D -m 644 -o "$SCREEN_USER" -g "$SCREEN_USER" "$tmp" "$conf"
	rm -f "$tmp"
	changed "wrote $conf"
}

addon_install() {
	lanmouse_binary
	lanmouse_config
	[ -S "$ROOT/run/user/$SCREEN_UID/bus" ] || return 0
	as_screen "$SCREEN_HOME/.local/bin/hostctl" start share-keys >/dev/null 2>&1 || true
}

addon_remove() {
	as_screen "$SCREEN_HOME/.local/bin/hostctl" stop share-keys >/dev/null 2>&1 || true
	rm -f "$LANMOUSE_BIN" "$LANMOUSE_BIN.version"
	rmdir "$(dirname "$LANMOUSE_BIN")" 2>/dev/null || true
}
