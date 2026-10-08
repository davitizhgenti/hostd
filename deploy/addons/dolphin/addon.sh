# shellcheck shell=bash
# Dolphin, the GameCube and Wii emulator (Flatpak). With the controllers
# add-on it can use real Wii Remotes through a Mayflash DolphinBar (in its
# mode 4) and the GameCube controller adapter. Set up controllers in
# Dolphin itself (Controllers > Wii Remote: "Real Wii Remote").
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Dolphin (Flatpak): GameCube and Wii games, real Wii Remotes through a DolphinBar"
ADDON_REQUIRES="flatpak controllers"

DOLPHIN_ID=org.DolphinEmu.dolphin-emu

addon_install() {
	if flatpak info --system "$DOLPHIN_ID" >/dev/null 2>&1; then
		info "Dolphin is installed"
	else
		info "installing Dolphin from Flathub"
		flatpak install --system -y --noninteractive flathub "$DOLPHIN_ID" >/dev/null
		changed "installed Dolphin"
	fi
	addon_rescan
}

addon_remove() {
	flatpak uninstall --system -y --noninteractive "$DOLPHIN_ID" >/dev/null 2>&1 && changed "removed Dolphin" || true
	addon_rescan
	info "Dolphin's own data (settings, saves) stays in $SCREEN_HOME/.var/app/$DOLPHIN_ID"
}
