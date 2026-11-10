# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers, and the platform not built here
# Steam's hook (addon.toml has the rest): the video memory games are told.

STEAM_ID=com.valvesoftware.Steam

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
	if as_screen flatpak override --user --show "$STEAM_ID" 2>/dev/null | grep -qF "DXVK_CONFIG=$want"; then
		info "games see $((total - 768)) of $total MiB video memory"
		return 0
	fi
	as_screen flatpak override --user --env="DXVK_CONFIG=$want" "$STEAM_ID"
	changed "games see $((total - 768)) of $total MiB video memory (DXVK)"
}

addon_install() {
	steam_vram_cap
}

addon_remove() {
	# The game apps it made (before the lister goes with the add-on's files).
	as_screen "$SCREEN_HOME/.local/lib/hostd/addons/hostd-steam-games" --remove || true
	as_screen flatpak override --user --reset "$STEAM_ID" 2>/dev/null || true # the games' video memory limit
}
