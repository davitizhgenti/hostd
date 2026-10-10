# shellcheck shell=bash
# Steam (Flatpak) in Big Picture for the TV, with its installed games
# listed under it. Steam is an app like any other: hostd has no Steam code.
# Games are app files that hostd-steam-games writes from Steam's library
# (a handoff to Steam, followed by SteamAppId, under = "steam"), kept
# current by a user path unit when the library changes.
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Steam (Flatpak) in Big Picture, with its games listed under it"
ADDON_REQUIRES="flatpak controllers"

STEAM_ID=com.valvesoftware.Steam

# Earlier versions ran Steam in gamescope: their session script and
# Flatpak permissions go. (gamescope stays installed; it does no harm.)
steam_drop_gamescope() {
	if [ -e /usr/local/bin/hostd-steam-session ]; then
		rm -f /usr/local/bin/hostd-steam-session
		changed "removed the gamescope Steam session"
	fi
	if flatpak override --system --show "$STEAM_ID" 2>/dev/null | grep -q "xdg-run/gamescope"; then
		flatpak override --system --reset "$STEAM_ID"
		changed "reset Steam's gamescope permissions"
	fi
}

# steam_vram_cap tells Proton games (DXVK) the card has 768 MiB less video
# memory than it has: games size their textures and caches to what they
# are told, which leaves room for Steam, the compositor and the menu, so
# a game cannot fill a small card (4 GB) and break up.
steam_vram_cap() {
	local total=""
	if command -v nvidia-smi >/dev/null; then
		total=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | head -1 | tr -d " ")
	else
		local f
		for f in /sys/class/drm/card[0-9]*/device/mem_info_vram_total; do
			[ -r "$f" ] && total=$(($(cat "$f") >> 20)) && break
		done
	fi
	case $total in '' | *[!0-9]*) info "video memory size unknown; no limit for games"; return 0 ;; esac
	[ "$total" -ge 2048 ] || return 0
	local want="dxgi.maxDeviceMemory = $((total - 768))"
	if flatpak override --system --show "$STEAM_ID" 2>/dev/null | grep -qF "DXVK_CONFIG=$want"; then
		info "games see $((total - 768)) of $total MiB video memory"
		return 0
	fi
	flatpak override --system --env="DXVK_CONFIG=$want" "$STEAM_ID"
	changed "games see $((total - 768)) of $total MiB video memory (DXVK)"
}

addon_install() {
	steam_drop_gamescope
	if flatpak info --system "$STEAM_ID" >/dev/null 2>&1; then
		info "Steam is installed"
	else
		info "installing Steam from Flathub (a few hundred MB)"
		flatpak install --system -y --noninteractive flathub "$STEAM_ID" >/dev/null
		changed "installed Steam"
	fi
	steam_vram_cap
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
	steam_drop_gamescope
	flatpak override --system --reset "$STEAM_ID" 2>/dev/null || true # the games' video memory limit
	flatpak uninstall --system -y --noninteractive "$STEAM_ID" >/dev/null 2>&1 && changed "removed Steam" || true
	addon_rescan
	info "Steam's own data (logins, saves, games) stays in $SCREEN_HOME/.var/app/$STEAM_ID"
}
