# shellcheck shell=bash
# Steam (Flatpak), started in Big Picture for the TV, with an app for each
# installed game. Steam is an app like any other: hostd has no Steam code.
# Games are app files that hostd-steam-games writes from Steam's library
# (a handoff to Steam, followed by SteamAppId), kept current by a user
# path unit when the library changes.
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Steam (Flatpak) in Big Picture, and an app for each installed game"
ADDON_REQUIRES="flatpak controllers"

STEAM_ID=com.valvesoftware.Steam

addon_install() {
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
	rm -f "$SCREEN_HOME/.config/hostd/apps/steam.toml" /usr/local/lib/hostd/addons/hostd-steam-games
	flatpak uninstall --system -y --noninteractive "$STEAM_ID" >/dev/null 2>&1 && changed "removed Steam" || true
	addon_rescan
	info "Steam's own data (logins, saves, games) stays in $SCREEN_HOME/.var/app/$STEAM_ID"
}
