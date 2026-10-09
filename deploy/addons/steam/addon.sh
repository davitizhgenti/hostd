# shellcheck shell=bash
# Steam (Flatpak), started in Big Picture inside gamescope for the TV (as
# on SteamOS, so Steam's overlay works over games), with an app for each
# installed game. Steam is an app like any other: hostd has no Steam code.
# Games are app files that hostd-steam-games writes from Steam's library
# (a handoff to Steam, followed by SteamAppId), kept current by a user
# path unit when the library changes.
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Steam (Flatpak) in Big Picture with gamescope, and an app for each installed game"
ADDON_REQUIRES="flatpak controllers"

STEAM_ID=com.valvesoftware.Steam

# ensure_gamescope installs gamescope, from the release's backports when
# the main archive has none (Debian 13), and lets it raise its own
# priority, as it asks for ("No CAP_SYS_NICE ... Performance will be
# affected"). Package upgrades drop the capability; every install sets it.
ensure_gamescope() {
	if ! dpkg-query -W -f='${Status}' gamescope 2>/dev/null | grep -q '^install ok installed$'; then
		if apt-cache policy gamescope 2>/dev/null | grep -q 'Candidate: [0-9]'; then
			ensure_packages gamescope
		else
			local codename
			codename=$(. /etc/os-release && echo "${VERSION_CODENAME:-}")
			[ -n "$codename" ] || die "gamescope: cannot tell the Debian release"
			if ! grep -rqs "^deb .* $codename-backports " /etc/apt/sources.list /etc/apt/sources.list.d/; then
				echo "deb http://deb.debian.org/debian $codename-backports main" >/etc/apt/sources.list.d/hostd-backports.list
				changed "enabled $codename-backports (only for gamescope)"
			fi
			info "installing gamescope from $codename-backports"
			apt-get update -q >/dev/null
			DEBIAN_FRONTEND=noninteractive apt-get install -y -q -t "$codename-backports" gamescope >/dev/null ||
				die "could not install gamescope"
			changed "installed gamescope"
		fi
	fi
	ensure_packages libcap2-bin
	local gs
	gs=$(command -v gamescope || echo /usr/games/gamescope)
	if ! getcap "$gs" | grep -q cap_sys_nice; then
		setcap cap_sys_nice=eip "$gs"
		changed "let gamescope raise its priority"
	fi
}

addon_install() {
	ensure_gamescope
	ensure_packages x11-utils # xwininfo: the session watches for Big Picture
	install_file "$ADDON_HERE/hostd-steam-session" /usr/local/bin/hostd-steam-session 755 root ||
		info "Steam session up to date"
	if flatpak info --system "$STEAM_ID" >/dev/null 2>&1; then
		info "Steam is installed"
	else
		info "installing Steam from Flathub (a few hundred MB)"
		flatpak install --system -y --noninteractive flathub "$STEAM_ID" >/dev/null
		changed "installed Steam"
	fi
	install -d -o "$SCREEN_USER" -g "$SCREEN_USER" "$SCREEN_HOME/.config/hostd" "$SCREEN_HOME/.config/hostd/apps"
	install_file "$ADDON_HERE/steam.toml" "$SCREEN_HOME/.config/hostd/apps/steam.toml" 644 "$SCREEN_USER" ||
		info "Steam app file up to date"
	install_file "$ADDON_HERE/hostd-steam-games" /usr/local/lib/hostd/addons/hostd-steam-games 755 root ||
		info "game list maker up to date"
	local units=$SCREEN_HOME/.config/systemd/user reload=0 u
	for u in hostd-steam-games.service hostd-steam-games.path hostd-steam-games.timer; do
		install_file "$ADDON_HERE/$u" "$units/$u" 644 "$SCREEN_USER" && reload=1
	done
	[ $reload -eq 1 ] && as_screen systemctl --user daemon-reload
	as_screen systemctl --user enable --now hostd-steam-games.path hostd-steam-games.timer >/dev/null 2>&1 || true
	as_screen systemctl --user start hostd-steam-games.service || info "warning: listing Steam's games failed (see journalctl --user -u hostd-steam-games)"
	addon_rescan
}

addon_remove() {
	as_screen systemctl --user disable --now hostd-steam-games.path hostd-steam-games.timer >/dev/null 2>&1 || true
	rm -f "$SCREEN_HOME"/.config/systemd/user/hostd-steam-games.{service,path,timer}
	as_screen systemctl --user daemon-reload || true
	# The game apps it made, and the Steam app file.
	as_screen /usr/local/lib/hostd/addons/hostd-steam-games --remove || true
	rm -f "$SCREEN_HOME/.config/hostd/apps/steam.toml" /usr/local/lib/hostd/addons/hostd-steam-games /usr/local/bin/hostd-steam-session
	flatpak uninstall --system -y --noninteractive "$STEAM_ID" >/dev/null 2>&1 && changed "removed Steam" || true
	addon_rescan
	info "Steam's own data (logins, saves, games) stays in $SCREEN_HOME/.var/app/$STEAM_ID"
	info "gamescope stays installed (apt remove gamescope removes it)"
}
