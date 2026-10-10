# shellcheck shell=bash
# Chromium, the web browser (Debian's package): an app (its app file adds
# an incognito-window action), and the browser hostd's web-page apps
# (runner url) open in.
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Chromium web browser (also opens hostd's web-page apps)"
ADDON_REQUIRES=""

addon_install() {
	if dpkg-query -W -f='${Status}' chromium 2>/dev/null | grep -q '^install ok installed$'; then
		info "Chromium is installed"
	else
		ensure_packages chromium
	fi
	install -d -o "$SCREEN_USER" -g "$SCREEN_USER" "$SCREEN_HOME/.config/hostd" "$SCREEN_HOME/.config/hostd/apps"
	install_file "$ADDON_HERE/chromium.toml" "$SCREEN_HOME/.config/hostd/apps/chromium.toml" 644 "$SCREEN_USER" ||
		info "Chromium app file up to date"
	addon_rescan
}

addon_remove() {
	rm -f "$SCREEN_HOME/.config/hostd/apps/chromium.toml"
	DEBIAN_FRONTEND=noninteractive apt-get purge -y -q chromium >/dev/null 2>&1 && changed "removed Chromium" || true
	addon_rescan
	info "Chromium's own data (profile, history) stays in $SCREEN_HOME/.config/chromium"
}
