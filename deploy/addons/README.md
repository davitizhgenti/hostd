# Add-ons

An add-on puts something optional on a hostd machine: an app (Steam, Dolphin), a tool (podman-compose), device access (controllers). Each is a directory here with an `addon.toml` that says what it installs. hostd-setup carries them (they are built into it), runs them on `sudo hostd-setup add NAME`, runs them again when a new build changes them (or on `update --all`, or `update NAME`), and undoes them on `remove NAME`.

```toml
description = "Dolphin (Flatpak): GameCube and Wii games"   # shown by hostd-setup addons
requires = ["flatpak", "controllers"]   # add-ons installed first, and kept while this one is

packages = ["chromium"]                 # Debian packages
purge = true                            # removed again with the add-on

flathub = true                          # set up the Flathub repository
flatpak_update = true                   # update every Flatpak app and runtime when it runs
flatpaks = ["org.DolphinEmu.dolphin-emu"]   # apps from Flathub, uninstalled with the add-on

kernel_modules = ["uinput"]             # loaded now and at boot

# Files from this directory. In dest, ~ is the screen user's home; owner
# "screen" is the screen user (default root); mode is octal (default 644).
files = [
  { src = "steam.toml", dest = "~/.config/hostd/apps/steam.toml", owner = "screen" },
  { src = "hostd-steam-games", dest = "/usr/local/lib/hostd/addons/hostd-steam-games", mode = "755" },
]
on_change = [["udevadm", "control", "--reload"]]   # run when one of the files changed

user_units = ["hostd-steam-games.path"] # the screen user's units: enabled and started
user_start = ["hostd-steam-games.service"]   # started on every run
rescan = true                           # hostd reads the app catalog again afterwards
keeps = "Dolphin's own data stays in ~/.var/app/org.DolphinEmu.dolphin-emu"   # said on remove

hook = "hook.sh"                        # optional: steps the fields above cannot say
```

Everything is checked first and changed only if it differs, so running an add-on again changes nothing that is already right.

## Hooks

When the steps are a chain of commands (NVIDIA's driver installer, the Pterodactyl panel's PHP and database setup), `hook` names a bash script that defines `addon_install` and `addon_remove`. It runs after the fields above (on remove, before files and apps go), as root, with these helpers from `lib.sh`:

| Helper | Does |
| --- | --- |
| `info`, `changed`, `warn` | A line in hostd-setup's output (`changed` marks the step as having changed something) |
| `die MESSAGE` | Stops, failing the add-on |
| `install_file SRC DEST MODE OWNER` | Copies a file unless it is the same; keeps a file hostd did not write as `.orig` |
| `ensure_packages PKG...` | Installs the Debian packages that are missing |
| `as_screen CMD...` | Runs a command as the screen user, in its session |
| `addon_rescan` | hostd reads the app catalog again |

and these variables: `SCREEN_USER`, `SCREEN_HOME`, `SCREEN_UID`, `ADDON_HERE` (this directory's files), `ADDON_NAMES` (the add-ons named in this run), `ADDONS_STATE` (the file listing installed add-ons) and `HOSTD_AUTO` (`1` when the update timer runs it: hold back changes to data). Setting `NEED_REBOOT=1` adds "Reboot to finish" to the summary.

`hostd-setup` has unit tests for the manifests (`deploy/addons_test.go`); the install test (`make test-install`) adds and removes the controllers add-on on a real system.
