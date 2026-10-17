#!/bin/bash
# The end-to-end test (make test-e2e): a real Debian 13 machine in a VM
# (QEMU with KVM, inside a container, so nothing is installed on the
# host), with what a container cannot give: its own kernel, a real boot,
# systemd as PID 1, lingering user services and the Sway session at boot.
#
#   1. cloud image boots; install.sh installs hostd; a second run changes nothing
#   2. reboot: hostd and the Sway session come up by themselves; deploy/check.sh passes
#   3. commands run in the background end, with their status; an update
#      while an app runs: the app keeps running
#   4. a build that cannot start: rolled back, the app still runs
#   5. reboot: the installed version, tokens and app files are still there
#   6. an update through the installer (hostd-setup update, what the timer
#      runs): the app keeps running, and the old version's data is kept for
#      a rollback
#   7. the wireguard add-on: the tunnel, a device, hostd on its address;
#      the jupyter add-on: HTTPS, its token, collaboration, autostart
#   8. the API and the docs from outside the VM
#
# Runs inside the hostd-e2e container, with these mounted:
#   /repo (the checkout), /dist (make dist), /bins (e2e-next, e2e-bad),
#   /vm (cache and logs, test/e2e/.vm)
set -u

IMAGE_URL=${E2E_IMAGE_URL:-https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64}
VM=/vm
OUT=$VM/out
SSH_PORT=2222
fail=0
mkdir -p "$VM/cache" "$OUT"
rm -f "$OUT"/*.log

ok() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad() {
	printf '  \033[31mFAIL\033[0m %s\n' "$1"
	[ -n "${2:-}" ] && printf '%s\n' "$2" | tail -15 | sed 's/^/       /'
	fail=1
}
# t NAME COMMAND...: a check, run on the host side of the VM.
t() {
	local name=$1 out
	shift
	if out=$("$@" 2>&1); then ok "$name"; else bad "$name" "$out"; fi
}
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 2; }

KEY=$VM/id_ed25519
SSHOPTS=(-i "$KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR
	-o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=6 -o IdentitiesOnly=yes)
ga() { ssh "${SSHOPTS[@]}" admin@127.0.0.1 "$@"; }  # as the admin user
gs() { ssh "${SSHOPTS[@]}" screen@127.0.0.1 "$@"; } # as the screen user

wait_for() { # SECONDS COMMAND...
	local n=$1 i
	shift
	for ((i = 0; i < n; i++)); do
		"$@" >/dev/null 2>&1 && return 0
		sleep 1
	done
	return 1
}

boot_id() { ga cat /proc/sys/kernel/random/boot_id; }
reboot_vm() {
	local before
	before=$(boot_id) || return 1
	ga 'sudo systemctl reboot' >/dev/null 2>&1 || true
	local i
	for ((i = 0; i < 240; i++)); do
		sleep 2
		[ "$(boot_id 2>/dev/null)" ] && [ "$(boot_id 2>/dev/null)" != "$before" ] && return 0
	done
	return 1
}
hostd_up() { gs 'hostctl version' 2>/dev/null | grep -q '^hostd '; }
running_version() { gs 'hostctl version' 2>/dev/null | awk '/^hostd /{print $2}' | tr -d ,; }
version_is() { [ "$(running_version)" = "$1" ]; }
second_install() { ga 'sudo ~/deploy/install.sh --gpu other --from ~/dist' | grep -q 'No changes needed'; }
app_runs() { gs "kill -0 $PID && hostctl ps | grep -q '^e2e-sleep .*running'"; }
# The API is HTTPS with the machine's own CA, which the checks trust (its
# leaf certificate names 127.0.0.1 among the machine's addresses).
fetch_ca() { gs 'cat ~/.local/state/hostd/tls/ca.pem' >"$OUT/ca.pem" && grep -q 'BEGIN CERTIFICATE' "$OUT/ca.pem"; }
api_version() { curl -fsS --cacert "$OUT/ca.pem" -H "Authorization: Bearer $TOKEN" https://127.0.0.1:7300/v1/version | grep -q '"version"'; }
api_without_token() { [ "$(curl -s --cacert "$OUT/ca.pem" -o /dev/null -w '%{http_code}' https://127.0.0.1:7300/v1/version)" = 401 ]; }
api_not_plain() { [ "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" http://127.0.0.1:7300/v1/version)" = 400 ]; }
docs_redirect() { [ "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' http://127.0.0.1:7300/docs/)" = "301 https://127.0.0.1:7300/docs/" ]; }
docs_served() { curl -fsS --cacert "$OUT/ca.pem" https://127.0.0.1:7300/docs/ | grep -qi 'hostd'; }

cleanup() {
	if [ -f "$VM/qemu.pid" ]; then
		kill "$(cat "$VM/qemu.pid")" 2>/dev/null
		rm -f "$VM/qemu.pid"
	fi
}
trap cleanup EXIT

# --- the VM --------------------------------------------------------------
echo "the machine"
base=$VM/cache/debian-13-generic-amd64.qcow2
if [ ! -f "$base" ]; then
	echo "  downloading the Debian 13 cloud image (once, about 400 MB)"
	curl -fsSL -o "$base.part" "$IMAGE_URL.qcow2" || die "cannot download $IMAGE_URL.qcow2"
	curl -fsSL -o "$VM/cache/SHA512SUMS" "$(dirname "$IMAGE_URL")/SHA512SUMS" || die "cannot download SHA512SUMS"
	want=$(awk -v f="$(basename "$IMAGE_URL").qcow2" '$2 == f {print $1}' "$VM/cache/SHA512SUMS")
	[ -n "$want" ] || die "SHA512SUMS has no entry for the image"
	[ "$(sha512sum "$base.part" | cut -d' ' -f1)" = "$want" ] || die "the downloaded image does not match SHA512SUMS"
	mv "$base.part" "$base"
fi
[ -f "$KEY" ] || ssh-keygen -q -t ed25519 -N '' -f "$KEY"
rm -f "$VM/disk.qcow2" "$VM/seed.img"
qemu-img create -q -f qcow2 -b "$base" -F qcow2 "$VM/disk.qcow2" 20G
cat >"$VM/user-data" <<EOF
#cloud-config
users:
  - name: admin
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $(cat "$KEY.pub")
ssh_pwauth: false
EOF
printf 'instance-id: hostd-e2e\nlocal-hostname: e2e\n' >"$VM/meta-data"
cloud-localds "$VM/seed.img" "$VM/user-data" "$VM/meta-data" || die "cannot make the cloud-init seed"

[ -w /dev/kvm ] || die "no access to /dev/kvm: run with --device /dev/kvm"
qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp 4 -m 4096 \
	-drive file="$VM/disk.qcow2",if=virtio,format=qcow2 -drive file="$VM/seed.img",if=virtio,format=raw \
	-netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22,hostfwd=tcp:127.0.0.1:7300-:7300" -device virtio-net-pci,netdev=n0 \
	-device virtio-vga -audiodev none,id=snd0 -device ich9-intel-hda -device hda-output,audiodev=snd0 -display none -serial "file:$OUT/console.log" -pidfile "$VM/qemu.pid" -daemonize ||
	die "qemu did not start"
t "the VM boots and takes the ssh key" wait_for 240 ga true

# --- install ---------------------------------------------------------------
echo "install"
ga 'rm -rf ~/deploy ~/dist && mkdir -p ~/dist'
tar -C /repo -c deploy | ga 'tar -x -C ~'
tar -C /dist -c . | ga 'tar -x -C ~/dist'
# The builds for the update tests, where the screen user can read them.
ga 'sudo install -d -m 755 /opt/e2e'
tar -C /bins -c . | ga 'sudo tar -x -C /opt/e2e && sudo chmod 755 /opt/e2e/*'
if ga 'sudo ~/deploy/install.sh --gpu other --from ~/dist' >"$OUT/install.log" 2>&1; then
	ok "install.sh installs hostd, the Sway session and the rest"
else
	bad "install.sh" "$(tail -20 "$OUT/install.log")"
	echo "the install failed; the log is $OUT/install.log"
	exit 1
fi
t "a second run changes nothing" second_install
t "SSH accepts keys only (the admin has one)" ga 'sudo sshd -T | grep -qx "passwordauthentication no"'
t "hostd runs as the screen user's service" wait_for 60 hostd_up

# --- the first boot into the session --------------------------------------
# The VM has a virtual GPU (virtio-vga) and no hardware rendering: let Sway
# use software rendering. (The "generic" Debian image has the graphics
# drivers the "genericcloud" one leaves out.)
ga 'sudo install -d /etc/systemd/system/greetd.service.d && printf "[Service]\nEnvironment=WLR_RENDERER_ALLOW_SOFTWARE=1\n" | sudo tee /etc/systemd/system/greetd.service.d/e2e.conf >/dev/null && sudo systemctl daemon-reload'
echo "reboot (the first boot into the Sway session)"
t "the VM reboots" reboot_vm
t "ssh is back" wait_for 120 ga true
t "hostd is up by itself, with nobody logged in" wait_for 120 hostd_up
t "greetd started the Sway session" wait_for 120 ga 'pgrep -u screen -x sway'
if gs 'bash -s' </repo/deploy/check.sh >"$OUT/check.log" 2>&1; then
	ok "deploy/check.sh: everything hostd relies on works ($(grep -c PASS "$OUT/check.log") checks)"
else
	bad "deploy/check.sh" "$(grep -E 'FAIL|^ +' "$OUT/check.log" | head -15)"
fi

# --- an update while an app runs -----------------------------------------
echo "update"
gs 'mkdir -p ~/.config/hostd/apps && printf "name = \"E2E sleeper\"\nrunner = { type = \"process\", command = [\"sleep\", \"3600\"] }\nrestart = \"never\"\n" > ~/.config/hostd/apps/e2e-sleep.toml && hostctl app rescan >/dev/null && hostctl start e2e-sleep >/dev/null'
t "a background app runs" gs 'hostctl ps | grep -q "^e2e-sleep .*running"'
# A command that ends at once: systemd announces its unit already ended.
t "run --wait returns for a command that ends at once" gs 'timeout 30 hostctl run -b --wait -- echo e2e-ok | grep -q e2e-ok'
t "and exits with its status" gs 'timeout 30 hostctl run -b --wait -- false; test $? = 1'
PID=$(gs 'systemctl --user show -p MainPID --value hostd-e2e-sleep.service')
t "it has a process" test "${PID:-0}" -gt 1
gs 'hostctl token create e2e-keep --scopes read' >"$OUT/token.txt" 2>&1
TOKEN=$(grep -o 'hostd_[A-Za-z0-9_-]*' "$OUT/token.txt" | head -1)
t "a token is created" test -n "$TOKEN"
t "an update to a good build" gs 'hostctl update push /opt/e2e/hostd-next'
t "hostd runs the new version" wait_for 60 version_is e2e-next
t "the app kept running through the update" app_runs

echo "a broken update"
if gs 'hostctl update push /opt/e2e/hostd-bad' >"$OUT/bad-update.log" 2>&1; then
	bad "a build that cannot start was reported as running"
else
	ok "a build that cannot start is refused"
fi
t "rolled back to the version before" wait_for 120 version_is e2e-next
t "the app still runs after the rollback" app_runs

# --- the second boot -------------------------------------------------------
echo "reboot (after updates)"
t "the VM reboots" reboot_vm
t "ssh is back" wait_for 120 ga true
t "hostd is up again" wait_for 120 hostd_up
t "it is the updated version (the current link survived)" version_is e2e-next
t "the token and the app file survived" gs 'hostctl token list | grep -q e2e-keep && test -f ~/.config/hostd/apps/e2e-sleep.toml'
t "the app did not come back by itself (its unit was transient)" gs '! systemctl --user is-active --quiet hostd-e2e-sleep.service'
t "the update timer is scheduled" ga 'systemctl is-active --quiet hostd-update.timer'
t "no unit failed" ga '! systemctl --failed --no-legend | grep -q .'

# --- an update through the installer --------------------------------------
# What the update timer runs: hostd-setup update, here from the checkout
# and the release build in the VM. hostd switches itself (hostd -install),
# keeping the state of the version it replaces.
echo "update through the installer"
gs 'hostctl start e2e-sleep >/dev/null'
PID=$(gs 'systemctl --user show -p MainPID --value hostd-e2e-sleep.service')
# shellcheck disable=SC2088 # ~ is the VM's home: it expands there
RELEASE=$(ga '~/dist/hostd-linux-amd64 -version 2>&1' | awk '{print $2}')
t "sudo hostd-setup update" ga 'sudo hostd-setup update --source ~/dist --from ~/dist --gpu other'
t "hostd runs the release build" wait_for 60 version_is "$RELEASE"
t "the app kept running through it" app_runs
t "the old version's state is kept for a rollback" gs 'L=~/.local/lib/hostd; [ "$(cat $L/versions/e2e-next/backup-for)" = "versions/'"$RELEASE"'" ] && test -s $L/versions/e2e-next/state.db.bak'
t "no more than 3 versions are kept" gs '[ "$(ls ~/.local/lib/hostd/versions | wc -l)" -le 3 ]'
t "no unit failed after it" ga '! systemctl --failed --no-legend | grep -q .'

# --- the wireguard add-on ---------------------------------------------------
# A tunnel of the machine's own, on the VM's real kernel: up, a device
# added and removed, hostd reached on the tunnel's address, then removed.
echo "wireguard"
t "sudo hostd-setup add wireguard" ga 'sudo HOSTD_WIREGUARD_ENDPOINT=vpn.example.org hostd-setup add wireguard --source ~/dist --from ~/dist --gpu other'
t "the tunnel is up at 10.44.0.1" ga 'ip -4 addr show wg-hostd | grep -q "inet 10.44.0.1/24"'
t "it listens on UDP 51820" ga '[ "$(sudo wg show wg-hostd listen-port)" = 51820 ]'
t "nothing is forwarded from or to it" ga 'sudo nft list table inet hostd_wg | grep -q "iifname \"wg-hostd\" drop"'
t "a device is added" ga 'sudo hostd-wg add phone | grep -q "Endpoint = vpn.example.org:51820"'
t "it is a peer of the tunnel" ga 'sudo wg show wg-hostd allowed-ips | grep -q "10.44.0.2/32"'
t "hostd answers on the tunnel's address" ga '[ "$(curl -sk -o /dev/null -w "%{http_code}" https://10.44.0.1:7300/v1/version)" = 401 ]'
t "a removed device is no longer a peer" ga 'sudo hostd-wg remove phone && ! sudo wg show wg-hostd allowed-ips | grep -q "10.44.0.2/32"'
t "remove wireguard stops the tunnel" ga 'sudo hostd-setup remove wireguard && ! ip link show wg-hostd 2>/dev/null'
t "and its forwarding rules go with it" ga '! sudo nft list table inet hostd_wg >/dev/null 2>&1'

# --- the jupyter add-on -----------------------------------------------------
# JupyterLab from PyPI in its own environment, a hostd app that starts with
# hostd, over HTTPS with the machine's certificate, behind its token.
echo "jupyter"
JCA='--cacert ~/.local/state/hostd/tls/ca.pem'
t "sudo hostd-setup add jupyter" ga 'sudo hostd-setup add jupyter --source ~/dist --from ~/dist --gpu other'
t "JupyterLab runs as a hostd app" wait_for 120 gs 'hostctl ps | grep -q "^jupyter .*running"'
t "it answers over HTTPS, with the machine's certificate and the token" wait_for 60 gs "curl -fsS $JCA \"https://127.0.0.1:8888/api/status?token=\$(cat ~/.config/hostd/jupyter/token)\" | grep -q started"
t "and not without the token" gs "case \$(curl -s $JCA -o /dev/null -w '%{http_code}' https://127.0.0.1:8888/api/status) in 401|403) ;; *) false ;; esac"
# shellcheck disable=SC2088 # ~ is the VM's home: it expands there
t "real-time collaboration is on" gs '~/.local/share/hostd/jupyter/venv/bin/jupyter server extension list 2>&1 | grep -Eiq "(ydoc|collaboration).*enabled"'
t "stopped, it is back when hostd starts (autostart)" gs 'hostctl stop jupyter >/dev/null && systemctl --user restart hostd'
t "  running again" wait_for 90 gs 'hostctl ps 2>/dev/null | grep -q "^jupyter .*running"'

# --- from outside the VM ---------------------------------------------------
echo "the network API"
t "the machine's CA is in its state folder" fetch_ca
t "the API answers over HTTPS with the token" api_version
t "plain HTTP does not reach the API" api_not_plain
t "the API refuses a request without a token" api_without_token
t "the docs are served without a token" docs_served
t "plain HTTP is sent to HTTPS" docs_redirect

echo
[ $fail -eq 0 ] && echo "e2e test passed" || echo "e2e test FAILED (logs in test/e2e/.vm/out)"
exit $fail
