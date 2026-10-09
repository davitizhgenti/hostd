# shellcheck shell=bash
# NVIDIA's own driver, 580 (a long-term branch, and the last one for
# Maxwell, Pascal and Volta cards), in place of Debian's 550. From 555 on
# the driver supports explicit sync; without it Sway, gamescope and
# Xwayland show frames before the GPU has finished them (flicker).
#
# NVIDIA's installer, checked against a pinned checksum, builds the kernel
# modules with DKMS, so kernel updates rebuild them. Debian's NVIDIA
# packages are removed and held back, so an update cannot pull them in
# again. The driver can only be replaced while nothing uses it: the screen
# session is stopped meanwhile and started again after. If anything fails,
# Debian's driver is put back. Removing the add-on puts it back too.
#
# A plain update never swaps drivers (it stops the screen): a newer
# version here is only reported, and installed by: hostd-setup add nvidia.
# shellcheck disable=SC2034
ADDON_DESCRIPTION="NVIDIA's 580 driver instead of Debian's 550 (fixes flicker; stops the screen while it installs)"
ADDON_REQUIRES=""

NV_VERSION=580.178.04
NV_SHA256=5975a86ee45bffcb626f51ae33d1169b108186a2ea47ad651e72f13fa4b6d6f9
NV_RUN=NVIDIA-Linux-x86_64-$NV_VERSION.run
NV_URL=https://download.nvidia.com/XFree86/Linux-x86_64/$NV_VERSION/$NV_RUN
NV_CACHE=/var/cache/hostd/nvidia
NV_PIN=/etc/apt/preferences.d/hostd-nvidia
# Debian's packages kept nouveau away; NVIDIA's installer only does when
# nouveau is loaded while it runs (it is not: Debian's driver is).
NV_NOUVEAU=/etc/modprobe.d/hostd-nvidia-nouveau.conf
# Steam's Flatpak brings its own GL; it needs the build for this driver.
NV_FLATPAK=("org.freedesktop.Platform.GL.nvidia-${NV_VERSION//./-}//1.4" "org.freedesktop.Platform.GL32.nvidia-${NV_VERSION//./-}//1.4")
# Debian's NVIDIA packages: removed, and held back while this is installed.
NV_DEBIAN='^(nvidia-|libnvidia-|libegl-nvidia|libgl1-nvidia|libgles-nvidia|libglx-nvidia|xserver-xorg-video-nvidia|glx-alternative-nvidia|firmware-nvidia-gsp|libcuda|libnvcuvid|libnvoptix)'

# nv_own: the version of NVIDIA's own driver installed, or nothing.
nv_own() {
	[ -x /usr/bin/nvidia-installer ] && [ -x /usr/bin/nvidia-uninstall ] || return 0
	/usr/bin/nvidia-installer --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1
}

nv_gpu() {
	local dev
	for dev in /sys/bus/pci/devices/*; do
		case $(cat "$dev/class" 2>/dev/null) in 0x03*) ;; *) continue ;; esac
		[ "$(cat "$dev/vendor")" = 0x10de ] && return 0
	done
	return 1
}

nv_asked() { case " ${ADDON_NAMES[*]} " in *" nvidia "*) return 0 ;; esac; return 1; }

nv_pin() {
	local tmp
	tmp=$(mktemp)
	cat >"$tmp" <<-EOF
		# Managed by hostd's nvidia add-on: NVIDIA's own driver is installed,
		# so Debian's NVIDIA packages must not be.
		Package: nvidia-* libnvidia-* libegl-nvidia* libgl1-nvidia* libgles-nvidia* libglx-nvidia* xserver-xorg-video-nvidia glx-alternative-nvidia firmware-nvidia-gsp libcuda* libnvcuvid* libnvoptix*
		Pin: release *
		Pin-Priority: -1
	EOF
	install_file "$tmp" "$NV_PIN" 644 root || true
	rm -f "$tmp"
}

# nv_no_nouveau keeps the kernel's nouveau driver off the card, so
# NVIDIA's can take it at boot. Returns 0 if the file changed.
nv_no_nouveau() {
	local tmp rc=1
	tmp=$(mktemp)
	printf '%s\n' "# Managed by hostd's nvidia add-on: NVIDIA's driver, not nouveau." \
		"blacklist nouveau" "options nouveau modeset=0" >"$tmp"
	if install_file "$tmp" "$NV_NOUVEAU" 644 root; then
		update-initramfs -u >/dev/null 2>&1 || true
		NEED_REBOOT=1
		rc=0
	fi
	rm -f "$tmp"
	return $rc
}

nv_flatpak() {
	command -v flatpak >/dev/null || return 0
	flatpak info --system "${NV_FLATPAK[0]%//*}" >/dev/null 2>&1 && return 0
	info "installing the matching NVIDIA GL for Flatpak apps (Steam)"
	flatpak install --system -y --noninteractive flathub "${NV_FLATPAK[@]}" >/dev/null ||
		info "warning: could not install ${NV_FLATPAK[*]}; Flatpak apps may not start until: flatpak update"
}

# nv_stop_screen stops everything that uses the GPU (the screen session,
# the screen user's apps) and unloads the driver. It returns non-zero,
# with the screen started again, if the driver stays loaded.
nv_stop_screen() {
	info "stopping the screen session (the screen goes dark until the driver is in place)"
	systemctl stop greetd 2>/dev/null || true
	systemctl stop "user@$SCREEN_UID.service" 2>/dev/null || true
	systemctl stop nvidia-persistenced 2>/dev/null || true
	sleep 2
	modprobe -r nvidia_drm nvidia_modeset nvidia_uvm nvidia 2>/dev/null || true
	if [ -d /sys/module/nvidia ]; then
		nv_start_screen
		return 1
	fi
}

nv_start_screen() {
	systemctl reset-failed greetd 2>/dev/null || true
	systemctl start greetd 2>/dev/null || true
	sleep 3
	if ! systemctl is-active --quiet greetd || ! pgrep -u "$SCREEN_USER" -x sway >/dev/null; then
		info "the screen did not come back yet; it does after the reboot"
		NEED_REBOOT=1
	fi
}

# nv_debian_back puts Debian's driver back (after a failure, or on remove).
nv_debian_back() {
	rm -f "$NV_PIN" "$NV_NOUVEAU"
	info "installing Debian's NVIDIA driver"
	apt-get update -q >/dev/null || true
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q linux-headers-amd64 nvidia-driver firmware-misc-nonfree >/dev/null ||
		info "warning: installing Debian's driver failed: sudo apt-get install nvidia-driver"
	update-initramfs -u >/dev/null 2>&1 || true
	modprobe nvidia_drm 2>/dev/null || true
}

addon_install() {
	nv_gpu || {
		sed -i '/^nvidia$/d' "$ADDONS_STATE"
		die "no NVIDIA graphics card here"
	}
	local own
	own=$(nv_own)
	if [ "$own" = "$NV_VERSION" ]; then
		info "NVIDIA driver $NV_VERSION is installed"
		nv_no_nouveau && info "nouveau is kept off the card now; reboot to finish"
		nv_pin
		nv_flatpak
		return 0
	fi
	if ! nv_asked; then
		# A plain update: never swap drivers behind someone's back.
		local now="Debian's"
		[ -n "$own" ] && now=$own
		info "NVIDIA driver $NV_VERSION is available (installed: $now); to install it: sudo hostd-setup add nvidia"
		return 0
	fi

	ensure_packages linux-headers-amd64 dkms gcc make
	mkdir -p "$NV_CACHE"
	if ! echo "$NV_SHA256  $NV_CACHE/$NV_RUN" | sha256sum -c --status 2>/dev/null; then
		info "downloading NVIDIA's driver $NV_VERSION (about 380 MB)"
		curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o "$NV_CACHE/$NV_RUN.part" "$NV_URL" || die "could not download $NV_URL"
		mv "$NV_CACHE/$NV_RUN.part" "$NV_CACHE/$NV_RUN"
		echo "$NV_SHA256  $NV_CACHE/$NV_RUN" | sha256sum -c --status ||
			{ rm -f "$NV_CACHE/$NV_RUN"; die "$NV_RUN does not match its checksum; not installed"; }
	fi

	if ! nv_stop_screen; then
		sed -i '/^nvidia$/d' "$ADDONS_STATE"
		die "the NVIDIA driver is still in use; nothing changed. Reboot, then: sudo hostd-setup add nvidia"
	fi

	local debian
	debian=$(dpkg-query -W -f='${Package} ${Status}\n' 2>/dev/null | awk -v re="$NV_DEBIAN" '/ install ok installed$/ && $1 ~ re {print $1}')
	if [ -n "$debian" ]; then
		info "removing Debian's NVIDIA packages"
		# shellcheck disable=SC2086 # package names
		DEBIAN_FRONTEND=noninteractive apt-get purge -y -q $debian >/dev/null ||
			{ nv_start_screen; sed -i '/^nvidia$/d' "$ADDONS_STATE"; die "could not remove Debian's NVIDIA packages"; }
	fi
	nv_pin
	nv_no_nouveau || true

	info "installing NVIDIA's driver $NV_VERSION (builds the kernel modules; a few minutes)"
	# Proprietary modules: the open ones do not support cards before Turing.
	if ! sh "$NV_CACHE/$NV_RUN" --silent --dkms --kernel-module-type=proprietary --disable-nouveau \
		--no-install-libglvnd --no-x-check --rebuild-initramfs; then
		info "NVIDIA's installer failed (see /var/log/nvidia-installer.log); putting Debian's driver back"
		[ -x /usr/bin/nvidia-uninstall ] && /usr/bin/nvidia-uninstall --silent >/dev/null 2>&1
		nv_debian_back
		nv_start_screen
		sed -i '/^nvidia$/d' "$ADDONS_STATE"
		die "NVIDIA's driver $NV_VERSION was not installed; Debian's driver is back"
	fi
	modprobe nvidia_drm 2>/dev/null || true
	changed "installed NVIDIA's driver $NV_VERSION"
	nv_flatpak
	nv_start_screen
	NEED_REBOOT=1
}

addon_remove() {
	if [ -z "$(nv_own)" ]; then
		rm -f "$NV_PIN"
		info "NVIDIA's own driver is not installed"
		return 0
	fi
	nv_stop_screen || die "the NVIDIA driver is still in use; reboot, then: sudo hostd-setup remove nvidia"
	info "removing NVIDIA's driver"
	/usr/bin/nvidia-uninstall --silent >/dev/null 2>&1 || info "warning: nvidia-uninstall reported a problem"
	nv_debian_back
	nv_start_screen
	changed "Debian's NVIDIA driver is back"
	info "Reboot to finish: sudo reboot"
}
