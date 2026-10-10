# shellcheck shell=bash
# Chromium, the web browser (Debian's package). Its desktop entry makes it
# an app with its own actions (new window, incognito window), and hostd's
# web-page apps (runner url) open in it.
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Chromium web browser (also opens hostd's web-page apps)"
ADDON_REQUIRES=""

addon_install() {
	if dpkg-query -W -f='${Status}' chromium 2>/dev/null | grep -q '^install ok installed$'; then
		info "Chromium is installed"
	else
		ensure_packages chromium
	fi
	addon_rescan
}

addon_remove() {
	DEBIAN_FRONTEND=noninteractive apt-get purge -y -q chromium >/dev/null 2>&1 && changed "removed Chromium" || true
	addon_rescan
	info "Chromium's own data (profile, history) stays in $SCREEN_HOME/.config/chromium"
}
