# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers
# Sunshine's hook (addon.toml has the rest): the pinned package, the
# screen user's settings (capture straight from Sway; the web UI and
# pairing only from the home network; "Desktop" as the only app) and the
# user service that starts it with the session.
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
	if [ ! -e "$dir/sunshine.conf" ]; then
		local tmp
		tmp=$(mktemp)
		cat >"$tmp" <<CONF
# Written by hostd's sunshine add-on once; your changes stay.
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
	local newpkg=0
	sunshine_package && newpkg=1
	sunshine_config
	if [ ! -S "$ROOT/run/user/$SCREEN_UID/bus" ]; then
		warn "$SCREEN_USER's user session is not running: Sunshine starts at its next login"
		return 0
	fi
	as_screen systemctl --user enable "$SUNSHINE_UNIT" >/dev/null 2>&1 || warn "could not enable $SUNSHINE_UNIT"
	if [ "$newpkg" = 1 ] || ! as_screen systemctl --user is-active --quiet "$SUNSHINE_UNIT"; then
		as_screen systemctl --user restart "$SUNSHINE_UNIT" || warn "Sunshine did not start (journalctl --user -u $SUNSHINE_UNIT)"
	fi
}

addon_remove() {
	as_screen systemctl --user disable --now "$SUNSHINE_UNIT" >/dev/null 2>&1 || true
	DEBIAN_FRONTEND=noninteractive apt-get remove -y -q sunshine >/dev/null 2>&1 || true
}
