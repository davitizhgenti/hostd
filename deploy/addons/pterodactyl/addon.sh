# shellcheck shell=bash
# Pterodactyl (https://pterodactyl.io): a panel for hosting game servers,
# and Wings, the daemon that runs them in Docker containers. Installed the
# way Pterodactyl's own Debian guide does, on one machine:
#
#   panel   nginx on port 80, PHP 8.3 (from deb.sury.org: Debian 13 ships
#           8.4, which the panel does not support), MariaDB, Redis, a queue
#           worker and a scheduler (systemd units), in /var/www/pterodactyl
#   Wings   Debian's docker.io, and the wings binary, as a service
#
# Everything downloaded is pinned here with its checksum, so an update of
# this add-on is how the versions change. The panel gets an admin user, a
# location and a node for this machine, and Wings its config, so game
# servers only need an allocation (a port) and an egg (a game).
# shellcheck disable=SC2034
ADDON_DESCRIPTION="Pterodactyl game server panel (http://<machine>/) and its Wings daemon (Docker)"
ADDON_REQUIRES="chromium" # the panel is an app of hostd: a page in its browser

PTERO_PANEL_VERSION=1.15.1
PTERO_PANEL_SHA256=62c88c035b3e0f3c3ddd06bc3ef12249d087af0e765f49878b6327a066ed860b
PTERO_WINGS_VERSION=1.13.3
PTERO_WINGS_SHA256_amd64=010d894a895fe4f914e3f1c1e75fb2fda4ebe50cc249e7e456887ea5b422c8fa
PTERO_WINGS_SHA256_arm64=8e3114cb0ee5ec617831d5ec35097b1096c6c972f05ef80c683a9f396d0fe9f5
PTERO_COMPOSER_VERSION=2.10.3
PTERO_COMPOSER_SHA256=7a2d379d5b8ffdaa028580ef26494c36d2feef4b178d3dd1473a4dbc5e17c8d6
# deb.sury.org's signing key (also the Signed-By of its Release files).
PTERO_SURY_FPR=15058500A0235D97F5D10063B188E2B695BD4743
PTERO_DIR=/var/www/pterodactyl

# ptero_fetch URL DEST SHA256: download, and refuse anything but the file
# this add-on was written for.
ptero_fetch() {
	curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o "$2" "$1" || die "cannot download $1"
	echo "$3  $2" | sha256sum --check --quiet - || die "$1 is not the file this add-on pins (sha256 $3)"
}

# ptero_artisan ARGS...: the panel's command line, as the web server's user.
ptero_artisan() {
	(cd "$PTERO_DIR" && runuser -u www-data -- php8.3 artisan "$@")
}

# ptero_sury: the PHP 8.3 repository, with its key checked against the
# fingerprint above.
ptero_sury() {
	local list=/etc/apt/sources.list.d/hostd-sury-php.sources keyring=/usr/share/keyrings/deb.sury.org-php.gpg
	local codename key
	if [ -f "$list" ] && [ -f "$keyring" ]; then
		info "the PHP 8.3 repository is set up"
		return 0
	fi
	codename=$(. /etc/os-release && echo "${VERSION_CODENAME:-}")
	case $codename in
	bookworm | trixie) ;;
	*) die "pterodactyl: PHP 8.3 comes from deb.sury.org, which has no packages for \"$codename\"" ;;
	esac
	ensure_packages gnupg ca-certificates curl
	key=$(mktemp)
	curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o "$key" https://packages.sury.org/php/apt.gpg ||
		die "cannot download deb.sury.org's key"
	gpg --show-keys --with-colons "$key" | awk -F: '$1 == "fpr" {print $10}' | grep -qx "$PTERO_SURY_FPR" ||
		die "the key from packages.sury.org is not the one this add-on pins ($PTERO_SURY_FPR)"
	install -D -m 644 "$key" "$keyring"
	rm -f "$key"
	cat >"$list" <<EOF
# Managed by hostd's pterodactyl add-on: PHP 8.3, which Pterodactyl needs.
Types: deb
URIs: https://packages.sury.org/php/
Suites: $codename
Components: main
Signed-By: $keyring
EOF
	apt-get update -q >/dev/null || die "apt-get update failed after adding the PHP repository"
	changed "added the deb.sury.org PHP repository"
}

# ptero_database: creates the panel's database and user (a first install
# only; later the password is in the panel's .env). Prints the password.
ptero_database() {
	local pw tables
	tables=$(mariadb -N -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'panel'")
	if [ "$tables" != 0 ] && [ ! -f "$PTERO_DIR/.hostd-owned" ]; then
		die "pterodactyl: a database named panel already exists and was not made by this add-on; not touching it"
	fi
	pw=$(openssl rand -hex 16)
	# A first install that failed halfway is started again from nothing.
	mariadb <<EOF
DROP DATABASE IF EXISTS panel;
CREATE DATABASE panel CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'pterodactyl'@'127.0.0.1' IDENTIFIED BY '$pw';
ALTER USER 'pterodactyl'@'127.0.0.1' IDENTIFIED BY '$pw';
GRANT ALL PRIVILEGES ON panel.* TO 'pterodactyl'@'127.0.0.1' WITH GRANT OPTION;
FLUSH PRIVILEGES;
EOF
	printf '%s' "$pw"
}

# ptero_unattended: whether this run is the update timer's. It never
# upgrades the panel or Wings (migrations change data): sudo hostd-setup
# update does.
ptero_unattended() { [ "${HOSTD_AUTO:-}" = 1 ]; }

# ptero_panel_files: the panel's files and dependencies, for the pinned
# version (a first install, or an upgrade).
ptero_panel_files() {
	local tmp upgrade=0
	[ "$(cat "$PTERO_DIR/.hostd-version" 2>/dev/null)" = "$PTERO_PANEL_VERSION" ] && return 1
	if [ -f "$PTERO_DIR/.hostd-bootstrapped" ] && ptero_unattended; then
		info "panel $PTERO_PANEL_VERSION is available: sudo hostd-setup update upgrades it (not done unattended)"
		return 1
	fi
	tmp=$(mktemp -d)
	ptero_fetch "https://github.com/pterodactyl/panel/releases/download/v$PTERO_PANEL_VERSION/panel.tar.gz" "$tmp/panel.tar.gz" "$PTERO_PANEL_SHA256"
	ptero_fetch "https://getcomposer.org/download/$PTERO_COMPOSER_VERSION/composer.phar" "$tmp/composer.phar" "$PTERO_COMPOSER_SHA256"
	if [ -f "$PTERO_DIR/artisan" ] && [ -f "$PTERO_DIR/.env" ]; then
		upgrade=1
		info "upgrading the panel to $PTERO_PANEL_VERSION"
		ptero_artisan down >/dev/null || true
	fi
	install -d "$PTERO_DIR"
	tar xzf "$tmp/panel.tar.gz" -C "$PTERO_DIR" || die "cannot unpack the panel"
	chmod -R 755 "$PTERO_DIR"/storage/* "$PTERO_DIR"/bootstrap/cache/
	install -d /var/cache/hostd-composer
	(cd "$PTERO_DIR" && COMPOSER_ALLOW_SUPERUSER=1 COMPOSER_HOME=/var/cache/hostd-composer \
		php8.3 "$tmp/composer.phar" install --no-dev --optimize-autoloader --no-interaction --quiet) ||
		die "composer could not install the panel's dependencies"
	rm -rf "$tmp"
	chown -R www-data:www-data "$PTERO_DIR"
	PTERO_UPGRADE=$upgrade
	return 0
}

# ptero_bootstrap: a first install's settings, admin, location and node.
# Sets PTERO_ADMIN_PASSWORD.
ptero_bootstrap() {
	local host dbpw tz mem disk reserve ip
	host=$(hostname -s)
	ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -1)
	[ -n "$ip" ] || ip=$(hostname -I | awk '{print $1}')
	[ -n "$ip" ] || die "pterodactyl: this machine has no network address for Wings' node"
	tz=$(timedatectl show -p Timezone --value 2>/dev/null || true)
	[ -n "$tz" ] || tz=UTC

	dbpw=$1
	cp "$PTERO_DIR/.env.example" "$PTERO_DIR/.env"
	chown www-data:www-data "$PTERO_DIR/.env"
	chmod 640 "$PTERO_DIR/.env"
	ptero_artisan key:generate --force >/dev/null
	ptero_artisan p:environment:setup --author="admin@$host.local" --url="http://$host.local" --timezone="$tz" \
		--cache=redis --session=redis --queue=redis --redis-host=127.0.0.1 --redis-pass= --redis-port=6379 \
		--settings-ui=true --telemetry=false >/dev/null
	ptero_artisan p:environment:database --host=127.0.0.1 --port=3306 --database=panel --username=pterodactyl \
		--password="$dbpw" >/dev/null
	ptero_artisan migrate --seed --force >/dev/null || die "the panel's database could not be set up"

	PTERO_ADMIN_PASSWORD=$(openssl rand -hex 12)
	ptero_artisan p:user:make --email="admin@$host.local" --username=admin --name-first=Admin --name-last=hostd \
		--password="$PTERO_ADMIN_PASSWORD" --admin=1 >/dev/null || die "cannot create the panel's admin user"
	ptero_artisan p:location:make --short=home --long="This machine" >/dev/null

	# What the node may hand out to game servers: the machine's memory and
	# free disk, less what the system itself needs.
	mem=$(free -m | awk '/^Mem:/ {print $2}')
	reserve=$((mem / 4))
	[ $reserve -ge 2048 ] || reserve=2048
	mem=$((mem - reserve))
	[ $mem -ge 512 ] || mem=512
	disk=$(df -BM --output=avail /var/lib | tail -1 | tr -dc '0-9')
	disk=$((disk * 8 / 10))
	ptero_artisan p:node:make --name="$host" --description="This machine" --locationId=1 --fqdn="$ip" --public=1 \
		--scheme=http --proxy=0 --maintenance=0 --maxMemory="$mem" --overallocateMemory=0 --maxDisk="$disk" \
		--overallocateDisk=0 --uploadSize=100 --daemonListeningPort=8080 --daemonSFTPPort=2022 \
		--daemonBase=/var/lib/pterodactyl/volumes >/dev/null || die "cannot create the panel's node"
	touch "$PTERO_DIR/.hostd-bootstrapped"
	PTERO_NODE_IP=$ip
}

ptero_nginx() {
	local changed_nginx=0
	install_file "$ADDON_HERE/nginx.conf" /etc/nginx/sites-available/pterodactyl.conf 644 root && changed_nginx=1
	if [ ! -L /etc/nginx/sites-enabled/pterodactyl.conf ]; then
		ln -s /etc/nginx/sites-available/pterodactyl.conf /etc/nginx/sites-enabled/pterodactyl.conf
		changed_nginx=1
	fi
	# Debian's welcome site also claims port 80 as the default.
	if [ -L /etc/nginx/sites-enabled/default ] && [ "$(readlink /etc/nginx/sites-enabled/default)" = /etc/nginx/sites-available/default ]; then
		rm -f /etc/nginx/sites-enabled/default
		changed_nginx=1
	fi
	if [ $changed_nginx -eq 1 ]; then
		nginx -t >/dev/null 2>&1 || die "the nginx configuration is not valid (nginx -t)"
		systemctl enable nginx >/dev/null 2>&1
		systemctl reload-or-restart nginx
		changed "nginx serves the panel on port 80"
	else
		info "nginx site up to date"
	fi
}

ptero_units() {
	local u units_changed=0
	for u in pteroq.service pterodactyl-schedule.service pterodactyl-schedule.timer wings.service; do
		install_file "$ADDON_HERE/$u" "/etc/systemd/system/$u" 644 root && units_changed=1
	done
	[ $units_changed -eq 1 ] && systemctl daemon-reload
	return 0
}

# ptero_wings: Docker, the wings binary and its config.
ptero_wings() {
	local arch want tmp sum
	ensure_packages docker.io
	systemctl enable --now docker >/dev/null 2>&1 || die "Docker did not start (journalctl -u docker)"
	arch=$(dpkg --print-architecture)
	case $arch in amd64 | arm64) ;; *) die "pterodactyl: Wings has no build for $arch" ;; esac
	want=PTERO_WINGS_SHA256_$arch
	want=${!want}
	sum=$(sha256sum /usr/local/bin/wings 2>/dev/null | cut -d' ' -f1 || true)
	if [ "$sum" != "$want" ] && [ -n "$sum" ] && ptero_unattended; then
		info "Wings $PTERO_WINGS_VERSION is available: sudo hostd-setup update upgrades it (not done unattended)"
	elif [ "$sum" != "$want" ]; then
		tmp=$(mktemp)
		ptero_fetch "https://github.com/pterodactyl/wings/releases/download/v$PTERO_WINGS_VERSION/wings_linux_$arch" "$tmp" "$want"
		install -m 755 "$tmp" /usr/local/bin/wings
		rm -f "$tmp"
		WINGS_NEW=1
		changed "installed Wings $PTERO_WINGS_VERSION"
	else
		info "Wings $PTERO_WINGS_VERSION is installed"
	fi
	install -d -m 755 /etc/pterodactyl /var/lib/pterodactyl /var/log/pterodactyl
}

# ptero_podman: Docker sets up its own firewall rules next to Podman's.
# Hostd's container apps are rootless Podman (networking through passt, not
# the host's forwarding), so they should not notice; this checks that.
ptero_podman() {
	[ -S "/run/user/$SCREEN_UID/podman/podman.sock" ] || return 0
	if as_screen curl -fsS --unix-socket "/run/user/$SCREEN_UID/podman/podman.sock" http://d/_ping 2>/dev/null | grep -q OK; then
		info "hostd's Podman still answers next to Docker"
	else
		info "WARNING: the screen user's Podman socket does not answer; hostd's container apps may be affected by Docker"
	fi
}

# ptero_node_address: the node is registered with this machine's address
# (browsers talk to Wings directly). If DHCP gave the machine another one,
# follow it, unless the node was given a name of the admin's own.
ptero_node_address() {
	local now cur
	now=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -1)
	[ -n "$now" ] || return 0
	cur=$(mariadb -N -e "SELECT fqdn FROM panel.nodes WHERE id = 1" 2>/dev/null || true)
	[[ $cur =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 0
	if [ "$cur" != "$now" ]; then
		mariadb -e "UPDATE panel.nodes SET fqdn = '$now' WHERE id = 1"
		changed "the machine's address changed ($cur -> $now): the panel's node follows. A fixed address (a DHCP reservation) avoids this"
	fi
}

# ptero_wings_config: Wings' config from the panel's node, once.
ptero_wings_config() {
	local conf=/etc/pterodactyl/config.yml out
	[ -f "$conf" ] && return 0
	out=$(ptero_artisan p:node:configuration 1) || die "the panel cannot give Wings' configuration"
	# Wings runs on the panel's machine: it reaches the panel directly
	# (mDNS names like core.local are not resolved by Wings).
	(
		umask 077
		printf '%s\n' "$out" | sed "s|^remote: .*|remote: 'http://127.0.0.1'|" >"$conf"
	)
	changed "wrote $conf (Wings' token for the panel: keep it private)"
}

addon_install() {
	local fresh=0 dbpw="" host
	host=$(hostname -s)
	[ -f "$PTERO_DIR/.hostd-bootstrapped" ] || fresh=1
	WINGS_NEW=0
	PTERO_UPGRADE=0

	if [ $fresh -eq 1 ] && [ -e "$PTERO_DIR" ] && [ ! -f "$PTERO_DIR/.hostd-owned" ] && [ -n "$(ls -A "$PTERO_DIR" 2>/dev/null)" ]; then
		die "pterodactyl: $PTERO_DIR exists and was not made by this add-on; not touching it"
	fi

	ptero_sury
	ensure_packages mariadb-server redis-server nginx curl tar unzip git openssl \
		php8.3-cli php8.3-fpm php8.3-gd php8.3-mysql php8.3-mbstring php8.3-bcmath php8.3-xml php8.3-curl php8.3-zip
	systemctl enable --now mariadb redis-server php8.3-fpm >/dev/null 2>&1 || die "the database, Redis or PHP-FPM did not start"

	if [ $fresh -eq 1 ]; then
		dbpw=$(ptero_database)
		install -d "$PTERO_DIR"
		touch "$PTERO_DIR/.hostd-owned"
	fi
	if ptero_panel_files; then
		if [ $fresh -eq 1 ]; then
			ptero_bootstrap "$dbpw"
		else
			ptero_artisan view:clear >/dev/null
			ptero_artisan config:clear >/dev/null
			ptero_artisan migrate --seed --force >/dev/null || die "the panel's database could not be upgraded"
		fi
		chown -R www-data:www-data "$PTERO_DIR"
		echo "$PTERO_PANEL_VERSION" >"$PTERO_DIR/.hostd-version"
		if [ $PTERO_UPGRADE -eq 1 ]; then
			ptero_artisan queue:restart >/dev/null || true
			ptero_artisan up >/dev/null || true
		fi
		changed "the panel is at $PTERO_PANEL_VERSION"
	else
		info "the panel is at $PTERO_PANEL_VERSION"
	fi

	ptero_nginx
	ptero_units
	ptero_wings
	ptero_wings_config
	ptero_podman
	ptero_node_address
	install -d -o "$SCREEN_USER" -g "$SCREEN_USER" "$SCREEN_HOME/.config/hostd" "$SCREEN_HOME/.config/hostd/apps"
	install_file "$ADDON_HERE/pterodactyl.toml" "$SCREEN_HOME/.config/hostd/apps/pterodactyl.toml" 644 "$SCREEN_USER" ||
		info "panel app file up to date"
	addon_rescan
	systemctl enable --now pteroq.service pterodactyl-schedule.timer >/dev/null 2>&1 || die "the panel's queue worker did not start"
	systemctl enable wings.service >/dev/null 2>&1
	if [ $WINGS_NEW -eq 1 ] || ! systemctl is-active --quiet wings; then
		systemctl restart wings || info "Wings did not start: journalctl -u wings"
	fi

	if curl -fsS -o /dev/null http://127.0.0.1/auth/login; then
		info "the panel answers on port 80"
	else
		die "the panel does not answer on port 80 (journalctl -u nginx -u php8.3-fpm; $PTERO_DIR/storage/logs)"
	fi
	if [ $fresh -eq 1 ]; then
		info "Pterodactyl panel: http://$host.local/"
		info "  admin login:   admin@$host.local"
		info "  admin password: $PTERO_ADMIN_PASSWORD   (shown only now; change it in the panel)"
		info "A node for this machine ($PTERO_NODE_IP:8080) is set up. For a game server, add allocations"
		info "(a port each) under Admin > Nodes > $host > Allocation, then create it under Admin > Servers."
	fi
}

addon_remove() {
	rm -f "$SCREEN_HOME/.config/hostd/apps/pterodactyl.toml"
	addon_rescan
	systemctl disable --now wings.service pteroq.service pterodactyl-schedule.timer >/dev/null 2>&1 || true
	rm -f /etc/systemd/system/wings.service /etc/systemd/system/pteroq.service \
		/etc/systemd/system/pterodactyl-schedule.service /etc/systemd/system/pterodactyl-schedule.timer \
		/usr/local/bin/wings /etc/nginx/sites-enabled/pterodactyl.conf /etc/nginx/sites-available/pterodactyl.conf
	if [ ! -e /etc/nginx/sites-enabled/default ] && [ -f /etc/nginx/sites-available/default ]; then
		ln -s /etc/nginx/sites-available/default /etc/nginx/sites-enabled/default
	fi
	systemctl daemon-reload
	systemctl reload nginx >/dev/null 2>&1 || true
	changed "stopped Pterodactyl and removed its services and site"
	info "Kept, so nothing is lost: the panel in $PTERO_DIR and its database, the game servers' files in"
	info "/var/lib/pterodactyl, Wings' config in /etc/pterodactyl, and the packages (nginx, PHP 8.3, MariaDB,"
	info "Redis, Docker). To delete the data as well:"
	info "  mariadb -e 'DROP DATABASE panel; DROP USER \"pterodactyl\"@\"127.0.0.1\"'"
	info "  rm -rf $PTERO_DIR /etc/pterodactyl /var/lib/pterodactyl /var/log/pterodactyl"
}
