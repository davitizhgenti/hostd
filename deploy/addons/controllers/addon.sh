# shellcheck shell=bash
# Game controllers: the device permissions games and emulators need, for
# the person logged in at the screen. The kernel already drives Xbox
# (xpad), PlayStation (hid-playstation), Switch (hid-nintendo) and Wii
# (hid-wiimote) pads as input devices; what games also want is raw HID
# (Steam Input, Dolphin's real Wii Remotes, rumble, gyro) and uinput (the
# virtual pads Steam Input makes), which are root-only by default.
# hostd's own controller profiles are separate (~/.config/hostd/controllers).
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Game controllers: raw HID and uinput access for Xbox, PlayStation, Nintendo (DolphinBar, GameCube adapter), 8BitDo, Valve pads"
ADDON_REQUIRES=""

addon_install() {
	local reload=0
	install_file "$ADDON_HERE/60-hostd-controllers.rules" /etc/udev/rules.d/60-hostd-controllers.rules 644 root && reload=1
	if [ "$(cat /etc/modules-load.d/hostd-uinput.conf 2>/dev/null)" != uinput ]; then
		echo uinput >/etc/modules-load.d/hostd-uinput.conf
		changed "uinput loads at boot"
	fi
	modprobe uinput 2>/dev/null || true
	if [ $reload -eq 1 ]; then
		udev_apply
		changed "applied the controller rules to connected devices"
	else
		info "controller rules up to date"
	fi
}

addon_remove() {
	rm -f /etc/udev/rules.d/60-hostd-controllers.rules /etc/modules-load.d/hostd-uinput.conf
	udev_apply
	changed "removed the controller rules"
}

# udev_apply reloads the rules and applies them to connected devices (a
# container may have no udev: then they apply at the next boot).
udev_apply() {
	udevadm control --reload 2>/dev/null || return 0
	udevadm trigger --subsystem-match=hidraw --subsystem-match=misc --subsystem-match=usb --action=change 2>/dev/null || true
}
