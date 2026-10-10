# shellcheck shell=bash
# Compose: podman-compose, which runs hostd's compose apps (runner
# compose) through "podman compose".
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Compose tool for multi-container apps (podman-compose)"
ADDON_REQUIRES=""

addon_install() {
	if dpkg-query -W -f='${Status}' podman-compose 2>/dev/null | grep -q '^install ok installed$'; then
		info "podman-compose is installed"
	else
		ensure_packages podman-compose
	fi
}

addon_remove() {
	DEBIAN_FRONTEND=noninteractive apt-get purge -y -q podman-compose >/dev/null 2>&1 && changed "removed podman-compose" || true
	info "compose apps' containers and volumes stay; hostd stops them when their app stops"
}
