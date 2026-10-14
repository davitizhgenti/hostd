#!/bin/bash
# The end-to-end test (make test-e2e): a real Debian 13 machine in a VM
# (QEMU with KVM, inside a container, so nothing is installed on the
# host), with what a container cannot give: its own kernel, a real boot,
# systemd as PID 1, lingering user services and the Sway session at boot.
#
#   1. cloud image boots; install.sh installs hostd; a second run changes nothing
#   2. reboot: hostd and the Sway session come up by themselves; deploy/check.sh passes
#   3. an update while an app runs: the app keeps running
#   4. a build that cannot start: rolled back, the app still runs
#   5. reboot: the installed version, tokens and app files are still there
#   6. the API and the docs from outside the VM
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
api_version() { curl -fsS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:7300/v1/version | grep -q e2e-next; }
api_without_token() { [ "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:7300/v1/version)" = 401 ]; }
docs_served() { curl -fsS http://127.0.0.1:7300/docs/ | grep -qi 'hostd'; }

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

# --- from outside the VM ---------------------------------------------------
echo "the network API"
t "the API answers with the token" api_version
t "the API refuses a request without a token" api_without_token
t "the docs are served without a token" docs_served

echo
[ $fail -eq 0 ] && echo "e2e test passed" || echo "e2e test FAILED (logs in test/e2e/.vm/out)"
exit $fail
