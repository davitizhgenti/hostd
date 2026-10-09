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

t "the add-ons are listed" 'sudo hostd-setup addons | grep -q "controllers" && sudo hostd-setup addons | grep -q "steam" && sudo hostd-setup addons | grep -q "dolphin"'
t "an unknown add-on is refused" '! sudo hostd-setup add nope --source /opt/hostd-src && ! grep -qx nope /etc/hostd/addons'
# add is an update: from this checkout and build, not GitHub.
local_src="--source /opt/hostd-src --from /opt/hostd-dist --gpu other"
t "add nvidia is refused without an NVIDIA card, and not recorded" "! sudo hostd-setup add nvidia $local_src && ! grep -qx nvidia /etc/hostd/addons"
t "add controllers" "sudo hostd-setup add controllers $local_src"
t "  its rules are in place" 'grep -q "Managed by hostd" /etc/udev/rules.d/60-hostd-controllers.rules && grep -qx uinput /etc/modules-load.d/hostd-uinput.conf'
t "  it is recorded and listed as installed" 'grep -qx controllers /etc/hostd/addons && sudo hostd-setup addons | grep -q "^\* controllers"'
t "a later install keeps it, changing nothing" 'sudo hostd-setup install --gpu other --from /opt/hostd-dist > /tmp/again.log 2>&1; grep -q "controller rules up to date" /tmp/again.log || { tail -25 /tmp/again.log; false; }'
t "removing something not installed is refused" '! sudo hostd-setup remove steam'
t "remove controllers" 'sudo hostd-setup remove controllers'
t "  its rules are gone and it is not recorded" '! test -e /etc/udev/rules.d/60-hostd-controllers.rules && ! grep -qx controllers /etc/hostd/addons'
exit $fail
