# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers
# Tailscale's hook (addon.toml has the rest): Tailscale's own repository,
# with its key pinned by fingerprint, then the login (an auth key in
# HOSTD_TAILSCALE_AUTHKEY, or a URL to open), then hostd's trust of the
# tailnet: a drop-in that adds its addresses to [api] allow.
TS_FPR=2596A99EAAB33821893C0A79458CA832957F5868
TS_KEYRING=/usr/share/keyrings/tailscale-archive-keyring.gpg
TS_LIST=/etc/apt/sources.list.d/hostd-tailscale.list
TS_NETS='["100.64.0.0/10", "fd7a:115c:a1e0::/48"]'

ts_dropin() { printf '%s' "$SCREEN_HOME/.config/hostd/hostd.d/tailscale.toml"; }

ts_repo() {
	local codename key
	if [ -f "$ROOT$TS_LIST" ] && [ -f "$ROOT$TS_KEYRING" ]; then
		return 0
	fi
	codename=$(. "$ROOT/etc/os-release" && echo "${VERSION_CODENAME:-}")
	case $codename in
	bookworm | trixie) ;;
	*) die "tailscale: no packages for \"$codename\"" ;;
	esac
	ensure_packages gnupg ca-certificates curl
	key=$(mktemp)
	curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o "$key" "https://pkgs.tailscale.com/stable/debian/$codename.noarmor.gpg" ||
		die "cannot download Tailscale's key"
	gpg --show-keys --with-colons "$key" | awk -F: '$1 == "fpr" {print $10}' | grep -qx "$TS_FPR" ||
		die "the key from pkgs.tailscale.com is not the one this add-on pins ($TS_FPR)"
	install -D -m 644 "$key" "$ROOT$TS_KEYRING"
	rm -f "$key"
	mkdir -p "$(dirname "$ROOT$TS_LIST")"
	printf '%s\n' "# Managed by hostd's tailscale add-on." \
		"deb [signed-by=$TS_KEYRING] https://pkgs.tailscale.com/stable/debian $codename main" >"$ROOT$TS_LIST"
	apt-get update -q >/dev/null || die "apt-get update failed after adding Tailscale's repository"
	changed "added Tailscale's repository"
}

# ts_state: the daemon's state (Running once logged in), from its status.
ts_state() { tailscale status --json 2>/dev/null | sed -n 's/^ *"BackendState": *"\([A-Za-z]*\)".*/\1/p' | head -1; }

ts_login() {
	local url
	if [ "$(ts_state)" = Running ]; then
		info "logged in to the tailnet: $(tailscale ip -4 2>/dev/null | head -1)"
		return 0
	fi
	if [ -n "${HOSTD_TAILSCALE_AUTHKEY:-}" ]; then
		tailscale up --authkey="$HOSTD_TAILSCALE_AUTHKEY" --hostname="$(hostname -s)" >/dev/null ||
			die "tailscale up refused the auth key"
		changed "joined the tailnet: $(tailscale ip -4 2>/dev/null | head -1)"
		return 0
	fi
	# Without a key, Tailscale gives a URL to approve this machine at.
	url=$(tailscale up --hostname="$(hostname -s)" --timeout=5s 2>&1 | grep -o 'https://login\.tailscale\.com/[^ ]*' | head -1 || true)
	if [ -n "$url" ]; then
		warn "to join the tailnet, open $url and log in; then: sudo hostd-setup update tailscale"
	else
		warn "tailscale is not logged in (tailscale status); then: sudo hostd-setup update tailscale"
	fi
}

# ts_trust: hostd accepts the tailnet's addresses as it does the home
# network's, through a drop-in (removed with the add-on). The tailnet's
# address of this machine is in its certificate once hostd sees it.
ts_trust() {
	local tmp dropin
	dropin=$(ts_dropin)
	tmp=$(mktemp)
	printf '%s\n' "# Managed by hostd's tailscale add-on; removed with it." "[api]" "allow = $TS_NETS" >"$tmp"
	install -d -o "$SCREEN_USER" -g "$SCREEN_USER" "$SCREEN_HOME/.config/hostd" "$SCREEN_HOME/.config/hostd/hostd.d"
	if install_file "$tmp" "$dropin" 644 "$SCREEN_USER"; then
		as_screen systemctl --user restart hostd || warn "restart hostd to use it: systemctl --user restart hostd"
	fi
	rm -f "$tmp"
}

addon_install() {
	ts_repo
	ensure_packages tailscale
	systemctl enable --now tailscaled >/dev/null 2>&1 || die "tailscaled did not start (journalctl -u tailscaled)"
	ts_login
	ts_trust
}

addon_remove() {
	local dropin
	dropin=$(ts_dropin)
	if [ -f "$dropin" ]; then
		rm -f "$dropin"
		as_screen systemctl --user restart hostd || true
		changed "hostd no longer accepts the tailnet's addresses"
	fi
	tailscale down >/dev/null 2>&1 || true
	info "Kept: the tailscale package and its login (sudo apt purge tailscale removes both)"
}
