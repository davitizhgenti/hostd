# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers
# WireGuard's hook (addon.toml has the rest): the tunnel's settings, this
# machine's key, the interface wg-hostd, the firewall if there is one.
# Devices are added with hostd-wg. The tunnel's network is private, so
# hostd already accepts its addresses; nothing changes in hostd.
WG_IFACE=wg-hostd
WG_ENV=/etc/hostd/wireguard.env
WG_KEY=/etc/wireguard/$WG_IFACE.key

# wg_private_net NET: a private /24 such as 10.44.0.0/24.
wg_private_net() {
	[[ $1 =~ ^(10\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])|192\.168)\.[0-9]{1,3}\.0/24$ ]]
}

# wg_settings: the settings file, made the first time; HOSTD_WIREGUARD_*
# given later change the endpoint and the port (the network stays: the
# devices' addresses are in it).
wg_settings() {
	local net port endpoint tmp
	if [ -f "$ROOT$WG_ENV" ]; then
		# shellcheck disable=SC1090
		. "$ROOT$WG_ENV"
		net=$NET port=${HOSTD_WIREGUARD_PORT:-$PORT} endpoint=${HOSTD_WIREGUARD_ENDPOINT:-${ENDPOINT:-}}
	else
		net=${HOSTD_WIREGUARD_NET:-10.44.0.0/24} port=${HOSTD_WIREGUARD_PORT:-51820} endpoint=${HOSTD_WIREGUARD_ENDPOINT:-}
	fi
	wg_private_net "$net" || die "wireguard: the tunnel's network must be a private /24, such as 10.44.0.0/24 (not $net)"
	if ! [[ $port =~ ^[0-9]+$ ]] || [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
		die "wireguard: port $port"
	fi
	[[ $endpoint =~ ^[A-Za-z0-9.:-]*$ ]] || die "wireguard: endpoint $endpoint"
	tmp=$(mktemp)
	printf '%s\n' "# Managed by hostd's wireguard add-on (hostd-wg endpoint changes ENDPOINT)." \
		"NET=$net" "ADDRESS=${net%.0/24}.1" "PORT=$port" "ENDPOINT=$endpoint" >"$tmp"
	install_file "$tmp" "$ROOT$WG_ENV" 644 root || true
	rm -f "$tmp"
	WG_PORT=$port WG_ENDPOINT=$endpoint WG_ADDRESS=${net%.0/24}.1
}

wg_key() {
	[ -s "$ROOT$WG_KEY" ] && return 0
	mkdir -p "$(dirname "$ROOT$WG_KEY")"
	(umask 077 && wg genkey >"$ROOT$WG_KEY")
	changed "made this machine's WireGuard key ($WG_KEY, readable by root only)"
}

# wg_firewall opens the port in ufw, if ufw is on.
wg_firewall() {
	command -v ufw >/dev/null || return 0
	ufw status 2>/dev/null | grep -q '^Status: active' || return 0
	ufw status 2>/dev/null | grep -q "^$WG_PORT/udp .*ALLOW" && return 0
	ufw allow "$WG_PORT/udp" comment "hostd wireguard" >/dev/null
	changed "opened UDP $WG_PORT in the firewall"
}

addon_install() {
	local before
	wg_settings
	wg_key
	before=$(cat "$ROOT/etc/wireguard/$WG_IFACE.conf" 2>/dev/null || true)
	HOSTD_ROOT=$ROOT bash "$ADDON_HERE/hostd-wg" render
	if [ "$before" != "$(cat "$ROOT/etc/wireguard/$WG_IFACE.conf")" ]; then
		changed "wrote /etc/wireguard/$WG_IFACE.conf"
		if systemctl is-active --quiet "wg-quick@$WG_IFACE"; then
			systemctl restart "wg-quick@$WG_IFACE" || die "the tunnel did not come back (journalctl -u wg-quick@$WG_IFACE)"
		fi
	fi
	if ! systemctl is-active --quiet "wg-quick@$WG_IFACE"; then
		systemctl enable --now "wg-quick@$WG_IFACE" >/dev/null 2>&1 || die "the tunnel did not start (journalctl -u wg-quick@$WG_IFACE)"
		changed "started the tunnel: this machine is $WG_ADDRESS"
	fi
	wg_firewall
	info "forward UDP $WG_PORT on your router to this machine ($(hostname -I 2>/dev/null | awk '{print $1}'))"
	if [ -z "$WG_ENDPOINT" ]; then
		warn "set the address your devices reach this machine at: sudo hostd-wg endpoint home.example.org"
	fi
	info "add a device: sudo hostd-wg add phone (a config and a QR code to scan)"
}

addon_remove() {
	if systemctl is-active --quiet "wg-quick@$WG_IFACE" || systemctl is-enabled --quiet "wg-quick@$WG_IFACE" 2>/dev/null; then
		systemctl disable --now "wg-quick@$WG_IFACE" >/dev/null 2>&1 || true
		changed "stopped the tunnel"
	fi
	if [ -f "$ROOT$WG_ENV" ] && command -v ufw >/dev/null; then
		# shellcheck disable=SC1090
		. "$ROOT$WG_ENV"
		ufw delete allow "$PORT/udp" >/dev/null 2>&1 || true
	fi
}
