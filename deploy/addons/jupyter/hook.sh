# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers
# Jupyter's hook (addon.toml has the rest): the Python environment, as the
# screen user, with JupyterLab, jupyter-collaboration and jupyterlab-git
# from PyPI: what is missing is installed on every run, upgrades happen
# when the add-on is named (hostd-setup update jupyter), never by the
# update timer. Then the screen user's key for GitHub, and the app.
JUP_PKGS=("jupyterlab>=4.2,<5" "jupyter-collaboration>=3,<5" "jupyterlab-git>=0.50,<1")
# GitHub's host key, as GitHub publishes it (SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU).
JUP_GITHUB_HOST="github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

jup_venv() { printf '%s' "$SCREEN_HOME/.local/share/hostd/jupyter/venv"; }

jup_named() {
	local n
	for n in "${ADDON_NAMES[@]}"; do [ "$n" = jupyter ] && return 0; done
	return 1
}

# jup_packages: the environment, with the packages above. Returns 0 if it
# changed something JupyterLab has to restart for.
jup_packages() {
	local venv=$1 want
	want=$(printf '%s\n' "${JUP_PKGS[@]}")
	if [ ! -x "$venv/bin/jupyter" ]; then
		info "making JupyterLab's Python environment (a few minutes)"
		as_screen python3 -m venv "$venv" || die "python3 -m venv failed"
	fi
	if [ "$(cat "$venv/.hostd-packages" 2>/dev/null)" != "$want" ]; then
		as_screen "$venv/bin/pip" install -q --disable-pip-version-check "${JUP_PKGS[@]}" ||
			die "installing JupyterLab failed (see the lines above)"
		printf '%s\n' "$want" >"$venv/.hostd-packages"
		chown "$SCREEN_USER:" "$venv/.hostd-packages"
		changed "installed ${JUP_PKGS[*]} in $venv"
		return 0
	fi
	if jup_named && [ "${HOSTD_AUTO:-}" != 1 ]; then
		as_screen "$venv/bin/pip" install -q --disable-pip-version-check --upgrade "${JUP_PKGS[@]}" ||
			warn "upgrading JupyterLab failed; the installed one stays"
		info "JupyterLab: $(as_screen "$venv/bin/jupyter" lab --version 2>/dev/null || echo unknown)"
		return 0
	fi
	return 1
}

# jup_github: the screen user's own key for GitHub (made once), used only
# for github.com, whose host key is pinned. Its public half goes on your
# GitHub account; then git clone/pull/push work from JupyterLab.
jup_github() {
	local ssh=$SCREEN_HOME/.ssh key tmp
	key=$ssh/github_ed25519
	install -d -m 700 -o "$SCREEN_USER" -g "$SCREEN_USER" "$ssh"
	if [ ! -s "$key" ]; then
		as_screen ssh-keygen -q -t ed25519 -N "" -C "$SCREEN_USER@$(hostname -s) (hostd)" -f "$key" ||
			die "ssh-keygen failed"
		changed "made the key for GitHub ($key)"
	fi
	if ! grep -qsxF "$JUP_GITHUB_HOST" "$ssh/known_hosts"; then
		printf '%s\n' "$JUP_GITHUB_HOST" >>"$ssh/known_hosts"
		chown "$SCREEN_USER:" "$ssh/known_hosts"
		changed "pinned GitHub's host key"
	fi
	if ! grep -qs "^# Managed by hostd's jupyter add-on" "$ssh/config"; then
		tmp=$(mktemp)
		cat "$ssh/config" 2>/dev/null >"$tmp" || true
		printf '%s\n' "" "# Managed by hostd's jupyter add-on: this machine's key for GitHub." \
			"Host github.com" "	IdentityFile ~/.ssh/github_ed25519" "	IdentitiesOnly yes" >>"$tmp"
		install -m 600 -o "$SCREEN_USER" -g "$SCREEN_USER" "$tmp" "$ssh/config"
		rm -f "$tmp"
		changed "GitHub uses that key ($ssh/config)"
	fi
	local who
	who=$(as_screen ssh -T -o BatchMode=yes -o ConnectTimeout=10 git@github.com 2>&1 | sed -n 's/^Hi \([^!]*\)!.*/\1/p' || true)
	if [ -n "$who" ]; then
		info "GitHub: this machine's key works, as $who"
	else
		warn "GitHub does not know this machine's key yet: add it at https://github.com/settings/ssh/new (Authentication key):"
		warn "    $(cat "$key.pub")"
	fi
	if [ -z "$(as_screen git config --global user.email 2>/dev/null || true)" ]; then
		warn "commits need a name: ssh screen@<machine> \"git config --global user.name 'Your Name' && git config --global user.email you@example.com\""
	fi
}

addon_install() {
	local venv restart=0
	venv=$(jup_venv)
	jup_packages "$venv" && restart=1
	jup_github
	addon_rescan
	if [ -S "$ROOT/run/user/$SCREEN_UID/hostd.sock" ]; then
		if ! as_screen hostctl ps 2>/dev/null | grep -q '^jupyter '; then
			if as_screen hostctl start jupyter >/dev/null 2>&1; then
				changed "started JupyterLab"
			else
				warn "JupyterLab did not start (hostctl logs jupyter)"
			fi
		elif [ "$restart" = 1 ]; then
			as_screen hostctl stop jupyter >/dev/null 2>&1 || true
			as_screen hostctl start jupyter >/dev/null 2>&1 || warn "JupyterLab did not start (hostctl logs jupyter)"
			changed "restarted JupyterLab with the new packages"
		fi
	fi
	info "JupyterLab: https://$(hostname -s).local:8888, on ~/projects (the token: ssh screen@<machine> \"cat ~/.config/hostd/jupyter/token\")"
	info "collaborators open the same address, with the token; their browsers need the machine's CA (hostctl tls)"
}

addon_remove() {
	if [ -S "$ROOT/run/user/$SCREEN_UID/hostd.sock" ]; then
		as_screen hostctl stop jupyter >/dev/null 2>&1 || true
	fi
}
