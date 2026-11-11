# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers
# share-remote's hook (addon.toml has the rest): the pinned package and the
# screen user's settings (capture straight from Sway; the web UI and pairing
# only from the home network; "Desktop" as the only app). Sunshine itself runs
# as the share-remote app, not as the package's own user service, so that
# hostd starts, watches and stops it like everything else.
SUNSHINE_VERSION=2026.914.233613
SUNSHINE_DEB="sunshine_${SUNSHINE_VERSION}-1+debiantrixie_amd64.deb"
SUNSHINE_URL="https://github.com/SunshineStream/Sunshine/releases/download/v${SUNSHINE_VERSION}/${SUNSHINE_DEB}"
SUNSHINE_SHA256=a86f858cbae57f60ecebd445402ee0295c1c057b857bd45d44c2ea2d255cbbfb
SUNSHINE_UNIT=app-dev.lizardbyte.app.Sunshine.service

sunshine_package() {
	local have
	have=$(dpkg-query -W -f='${Version}' sunshine 2>/dev/null || true)
	if [ "$have" = "${SUNSHINE_VERSION}-1+debiantrixie" ]; then
		info "Sunshine $SUNSHINE_VERSION is installed"
		return 1
	fi
	[ "$(dpkg --print-architecture)" = amd64 ] || die "this add-on has the amd64 package of Sunshine only"
	local tmp
	tmp=$(mktemp -d)
	curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o "$tmp/$SUNSHINE_DEB" "$SUNSHINE_URL" ||
		die "cannot download $SUNSHINE_URL"
	(cd "$tmp" && echo "$SUNSHINE_SHA256  $SUNSHINE_DEB" | sha256sum --check --quiet -) ||
		die "$SUNSHINE_URL is not the file this add-on pins (sha256 $SUNSHINE_SHA256)"
	info "installing Sunshine $SUNSHINE_VERSION"
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q "$tmp/$SUNSHINE_DEB" >/dev/null ||
		die "could not install Sunshine"
	rm -rf "$tmp"
	changed "installed Sunshine $SUNSHINE_VERSION"
	return 0
}

# sunshine_config writes the screen user's settings once; a person's edits stay.
sunshine_config() {
	local dir=$SCREEN_HOME/.config/sunshine host ip
	host=$(hostname -s)
	ip=$(hostname -I | awk '{print $1}')
	as_screen mkdir -p "$dir" # the user's, not root's (install -D would make it root's)
	if [ ! -e "$dir/sunshine.conf" ]; then
		local tmp
		tmp=$(mktemp)
		cat >"$tmp" <<CONF
# Written by hostd's share-remote add-on once; your changes stay.
sunshine_name = $host
# Capture Sway's screen directly (wlroots), no extra permission.
capture = wlr
# The web UI and the PIN page only from the home network.
origin_web_ui_allowed = lan
origin_pin_allowed = lan
# The addresses the web UI is opened at (its CSRF check refuses others).
csrf_allowed_origins = https://$host.local:47990,https://$ip:47990
CONF
		install -D -m 644 -o "$SCREEN_USER" -g "$SCREEN_USER" "$tmp" "$dir/sunshine.conf"
		rm -f "$tmp"
		changed "wrote $dir/sunshine.conf"
	fi
	if [ ! -e "$dir/apps.json" ]; then
		local apps
		apps=$(mktemp)
		cat >"$apps" <<'APPS'
{
  "env": {
    "PATH": "$(PATH):$(HOME)/.local/bin"
  },
  "apps": [
    {
      "name": "Desktop",
      "image-path": "desktop.png"
    }
  ]
}
APPS
		install -D -m 644 -o "$SCREEN_USER" -g "$SCREEN_USER" "$apps" "$dir/apps.json"
		rm -f "$apps"
		changed "wrote $dir/apps.json (the Desktop app only)"
	fi
}

addon_install() {
	sunshine_package || true
	sunshine_config
	if [ ! -S "$ROOT/run/user/$SCREEN_UID/bus" ]; then
		warn "$SCREEN_USER's user session is not running: share-remote starts with hostd"
		return 0
	fi
	# One way to run it: the package's user unit goes, the app takes over.
	as_screen systemctl --user disable --now "$SUNSHINE_UNIT" >/dev/null 2>&1 || true
	# A new file is followed by hostd; start the app now (autostart is for hostd's own start).
	as_screen "$SCREEN_HOME/.local/bin/hostctl" start share-remote >/dev/null 2>&1 || true
}

addon_remove() {
	as_screen "$SCREEN_HOME/.local/bin/hostctl" stop share-remote >/dev/null 2>&1 || true
	as_screen systemctl --user disable --now "$SUNSHINE_UNIT" >/dev/null 2>&1 || true
	DEBIAN_FRONTEND=noninteractive apt-get remove -y -q sunshine >/dev/null 2>&1 || true
}
