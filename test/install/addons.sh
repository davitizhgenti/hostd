#!/bin/bash
# Add-ons through hostd-setup (make test-install): list, add, re-apply,
# remove. Runs as the admin user inside the test container. Only the
# controllers add-on: the app add-ons download hundreds of MB from Flathub.
set -u
fail=0
t() {
	local out
	if out=$(eval "$2" 2>&1); then
		echo "  PASS $1"
	else
		echo "  FAIL $1"
		printf '%s\n' "$out" | tail -15 | sed 's/^/       /'
		fail=1
	fi
}

t "the add-ons are listed" 'sudo hostd-setup addons | grep -q "controllers" && sudo hostd-setup addons | grep -q "steam" && sudo hostd-setup addons | grep -q "dolphin" && sudo hostd-setup addons | grep -q "pterodactyl"'
t "an unknown add-on is refused" '! sudo hostd-setup add nope --source /opt/hostd-src && ! grep -qx nope /etc/hostd/addons'
# add is an update: from this checkout and build, not GitHub.
local_src="--source /opt/hostd-src --from /opt/hostd-dist --gpu other"
t "add nvidia is refused without an NVIDIA card, and not recorded" "! sudo hostd-setup add nvidia $local_src && ! grep -qx nvidia /etc/hostd/addons"
t "add controllers" "sudo hostd-setup add controllers $local_src"
t "  its rules are in place" 'grep -q "Managed by hostd" /etc/udev/rules.d/60-hostd-controllers.rules && grep -qx uinput /etc/modules-load.d/hostd-uinput.conf'
t "  it is recorded and listed as installed" 'grep -qx controllers /etc/hostd/addons && sudo hostd-setup addons | grep -q "^\* controllers"'
t "a later install keeps it, changing nothing" 'sudo hostd-setup install --gpu other --from /opt/hostd-dist > /tmp/again.log 2>&1; grep -q "controller rules up to date" /tmp/again.log || { tail -25 /tmp/again.log; false; }'
# An update prints what changed, and runs the add-ons again only when the
# installer or they changed (or when asked).
upd="sudo hostd-setup update $local_src"
t "a quiet update lists no step that had nothing to do" "$upd > /tmp/q.log 2>&1; grep -q 'No changes needed' /tmp/q.log && ! grep -q 'Checking the system' /tmp/q.log && grep -q 'already up to date' /tmp/q.log || { tail -20 /tmp/q.log; false; }"
t "  and leaves the add-on alone: nothing in it changed" "grep -q 'not run again' /tmp/q.log && ! grep -q 'Add-on: controllers' /tmp/q.log"
t "update --verbose prints every step" "$upd --verbose > /tmp/v.log 2>&1; grep -q '==> Checking the system' /tmp/v.log && grep -q 'not run again' /tmp/v.log"
t "update NAME runs that add-on now" "$upd --verbose controllers > /tmp/n.log 2>&1; grep -q '==> Add-on: controllers' /tmp/n.log && grep -q 'controller rules up to date' /tmp/n.log"
t "update --all runs every installed add-on" "$upd --verbose --all > /tmp/a.log 2>&1; grep -q '==> Add-on: controllers' /tmp/a.log"
t "update NAME for an add-on that is not installed is refused" "! $upd steam && $upd steam 2>&1 | grep -q 'not installed'"
t "a changed add-on is run again by a plain update" 'rm -rf /tmp/src2 && mkdir /tmp/src2 && cp -r /opt/hostd-src/deploy /tmp/src2/ && echo "# changed" >> /tmp/src2/deploy/addons/controllers/addon.sh && sudo hostd-setup update --verbose --source /tmp/src2 --from /opt/hostd-dist --gpu other > /tmp/c.log 2>&1; grep -q "==> Add-on: controllers" /tmp/c.log && ! grep -q "not run again" /tmp/c.log'
t "  and the next one leaves it alone again" 'sudo hostd-setup update --source /tmp/src2 --from /opt/hostd-dist --gpu other > /tmp/c2.log 2>&1; grep -q "not run again" /tmp/c2.log'
t "removing something not installed is refused" '! sudo hostd-setup remove steam'
t "remove controllers" 'sudo hostd-setup remove controllers'
t "  its rules are gone and it is not recorded" '! test -e /etc/udev/rules.d/60-hostd-controllers.rules && ! grep -qx controllers /etc/hostd/addons'
exit $fail
