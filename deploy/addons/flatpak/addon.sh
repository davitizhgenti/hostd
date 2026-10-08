# shellcheck shell=bash
# Flatpak with Flathub, system-wide: how the app add-ons install their
# apps. hostd finds Flatpak apps by itself (their exported desktop
# entries). Updating it updates every Flatpak app and runtime, which keeps
# the NVIDIA graphics runtime in step with the host's driver.
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Flatpak and Flathub, for apps that come as Flatpaks (updated with hostd)"
ADDON_REQUIRES=""

addon_install() {
	ensure_packages flatpak
	if flatpak remotes --system --columns=name | grep -qx flathub; then
		info "Flathub is set up"
	else
		flatpak remote-add --system --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo
		changed "added the Flathub repository"
	fi
	if [ -n "$(flatpak list --system --columns=application)" ]; then
		info "updating Flatpak apps and runtimes"
		flatpak update --system -y --noninteractive >/dev/null && info "Flatpak apps up to date"
	fi
}

addon_remove() {
	info "Flatpak itself stays: other apps may come as Flatpaks (apt remove flatpak to drop it)"
}
