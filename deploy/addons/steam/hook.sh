# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers, and the platform not built here
# Steam's hook (addon.toml has the rest): the video memory games are told,
# and the gamescope session of earlier versions going away.
STEAM_ID=com.valvesoftware.Steam

# Earlier versions ran Steam in gamescope: their session script and
# Flatpak permissions go. (gamescope stays installed; it does no harm.)
steam_drop_gamescope() {
	if [ -e "$ROOT/usr/local/bin/hostd-steam-session" ]; then
		rm -f "$ROOT/usr/local/bin/hostd-steam-session"
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
		for f in "$ROOT"/sys/class/drm/card[0-9]*/device/mem_info_vram_total; do
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
	steam_vram_cap
}

addon_remove() {
	# The game apps it made (before the lister goes with the add-on's files).
	as_screen /usr/local/lib/hostd/addons/hostd-steam-games --remove || true
	steam_drop_gamescope
	flatpak override --system --reset "$STEAM_ID" 2>/dev/null || true # the games' video memory limit
}
