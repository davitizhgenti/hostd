# shellcheck shell=bash
# shellcheck disable=SC2034 # variables for the helpers
# Jupyter's hook (addon.toml has the rest): the Python environment, as the
# screen user, with JupyterLab and jupyter-collaboration from PyPI. Made
# once; upgraded when the add-on is named (hostd-setup update jupyter),
# never by the update timer. Then the app is started.
JUP_PKGS=("jupyterlab>=4.2,<5" "jupyter-collaboration>=3,<5")

jup_venv() { printf '%s' "$SCREEN_HOME/.local/share/hostd/jupyter/venv"; }

jup_named() {
	local n
	for n in "${ADDON_NAMES[@]}"; do [ "$n" = jupyter ] && return 0; done
	return 1
}

addon_install() {
	local venv
	venv=$(jup_venv)
	if [ ! -x "$venv/bin/jupyter" ]; then
		info "making JupyterLab's Python environment (a few minutes)"
		as_screen python3 -m venv "$venv" || die "python3 -m venv failed"
		as_screen "$venv/bin/pip" install -q --disable-pip-version-check "${JUP_PKGS[@]}" ||
			die "installing JupyterLab failed (see the lines above)"
		changed "installed JupyterLab and jupyter-collaboration in $venv"
	elif jup_named && [ "${HOSTD_AUTO:-}" != 1 ]; then
		as_screen "$venv/bin/pip" install -q --disable-pip-version-check --upgrade "${JUP_PKGS[@]}" ||
			warn "upgrading JupyterLab failed; the installed one stays"
		info "JupyterLab: $(as_screen "$venv/bin/jupyter" lab --version 2>/dev/null || echo unknown)"
	fi
	addon_rescan
	if [ -S "$ROOT/run/user/$SCREEN_UID/hostd.sock" ] && ! as_screen hostctl ps 2>/dev/null | grep -q '^jupyter '; then
		if as_screen hostctl start jupyter >/dev/null 2>&1; then
			changed "started JupyterLab"
		else
			warn "JupyterLab did not start (hostctl logs jupyter)"
		fi
	fi
	info "JupyterLab: https://$(hostname -s).local:8888 (the token: ssh screen@<machine> cat ~/.config/hostd/jupyter/token)"
	info "collaborators open the same address, with the token; their browsers need the machine's CA (hostctl tls)"
}

addon_remove() {
	if [ -S "$ROOT/run/user/$SCREEN_UID/hostd.sock" ]; then
		as_screen hostctl stop jupyter >/dev/null 2>&1 || true
	fi
}
