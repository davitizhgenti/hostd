# Configuration

hostd reads `~/.config/hostd/hostd.toml` when it starts. Every key is optional. An unknown key, module or binding stops hostd with a message saying which one, so a typo never goes unnoticed.

```toml
# Built-in modules to load. A headless machine leaves out display and audio.
modules = ["apps", "display", "audio", "deploy", "automation"]

# The API on the home network ("" turns it off). The unix socket is always on.
listen = ":7300"

# How long a person's change holds a resource against automation, overall
# and per resource key prefix.
hold_window = "3m"
[hold_windows]
"audio." = "1m"

# Each built-in module's settings are its own section.

[apps]
# The browser for web apps ({url} and {profile} are replaced), and the
# gamescope command for apps with window.wrap = "gamescope".
browser   = ["chromium", "--kiosk", "--user-data-dir={profile}", "{url}"]
gamescope = ["gamescope", "-W", "1920", "-H", "1080", "-f"]

[display]
# After this long without keyboard, mouse or controller input, nobody is at
# the screen, and remote launches come to the front again.
idle_after = "5m"
# Apps see controller buttons too, so hostd acts on a button only when it
# is held this long.
hold = "600ms"
# Keys and controller buttons, mapped to display actions. These add to or
# change the defaults; "" removes one.
[display.keys]
"F1" = "display.menu"
[display.buttons]
guide = "display.menu"
# A screen's resolution, set whenever hostd meets Sway (at start, and again
# if Sway restarts), so every app draws at one size. "*" names every screen.
[display.outputs.HDMI-A-1]
mode = "1920x1080@60"

# How the API is served on the network (listen). HTTPS with the machine's
# own CA, always, unless tls = false and insecure_http = true.
[api]
allow = ["100.64.0.0/10"]          # networks trusted beside the private ones (a VPN's)
names = ["core.tail1234.ts.net"]   # more names for the certificate
```

The CA is made at the first start, for the host name, `<host>.local`, the names in `[api] names` and private addresses (Tailscale's included). It is kept in `~/.local/state/hostd/tls`; its certificate for the API is renewed by itself, before it expires and when the machine's addresses change. A name added to `names` later is left out of the certificate (`hostctl tls` says so), because the CA cannot vouch for names it was not made for: to remake it, stop hostd, remove that folder, start hostd, and log in again on every device. `[api]` may come from a drop-in file, as the Tailscale add-on's does.

In 0.1, `idle_after`, `browser` and `gamescope` were top-level keys, and the bindings were `[input]`, `[input.keys]` and `[input.buttons]`. hostd now refuses those names and says where each one moved. The audio, deploy and automation modules have no settings yet, so a section for one of them is an error.

The default bindings are: Super for the menu, Super+Tab and Super+Shift+Tab for the next and previous app, and Super+Q to close the app in front. Holding a controller's Guide button opens the menu.

## Drop-in files

Files in `~/.config/hostd/hostd.d/` add to `hostd.toml`: every `*.toml` there, in name order. A drop-in holds module sections (`[apps]`, `[display]`...) and `[api]`, so an add-on or a person can change a setting without editing `hostd.toml`. Anything else (`modules`, `listen`...) belongs in `hostd.toml`. A setting given in two files stops hostd with a message naming both.

```toml
# ~/.config/hostd/hostd.d/keys.toml
[display.keys]
"F1" = "display.menu"
```

External modules are not configured here: each is a file in `~/.config/hostd/modules/` ([resources](resources.md); [writing a module](modules.md)).

## Files and folders

| Path | What |
| --- | --- |
| `~/.config/hostd/hostd.toml` | This file |
| `~/.config/hostd/hostd.d/*.toml` | [Drop-ins](#drop-in-files): modules and settings in files of their own |
| `~/.config/hostd/apps/` | [App files](apps.md) |
| `~/.config/hostd/scenes/`, `rules/`, `scripts/` | [Automation](automation.md) |
| `~/.config/hostd/modules/` | [External modules](modules.md#in-any-language-an-external-module), one file each |
| `~/.config/hostd/<kind>/` | [Resources](resources.md): one folder per kind, one file each |
| `~/.config/hostd/secrets/` | [Secrets](services.md#secrets) (0600) |
| `~/.local/state/hostd/state.db` | Tokens and the audit trail |
| `~/hostd/git/`, `~/hostd/releases/`, `~/hostd/keys/` | [Services](services.md): repositories, releases, deploy keys |
| `~/.config/hostd/update.conf` | [Updates](install.md#updates) |

## Several devices

Every keyboard, mouse and controller is a **device** with an ID of its own (vendor:product and its serial or USB port): two keyboards or two controllers of one model are two devices. `hostctl devices` lists them with their kind, a controller's player slot, and when each was last used; `device.connected` and `device.disconnected` events say when one comes or goes.

**Players.** The first controller is player 1, the next takes the lowest free slot, and a controller keeps its slot while hostd runs and across restarts (slots are saved in hostd's state folder). `hostctl device assign <id> <player>` moves one; the controller that had the slot gets the other's old one. A device that is not a controller has no slot.

**The menu belongs to the controller that opened it.** Holding Guide opens it for that controller: only it is taken from the apps (the others keep playing) and only its buttons move the menu. The `display.nav` events say which device and player.

**Bindings per player.** `[display.buttons]` binds controller buttons for everyone; a player's own table changes that for one slot:

```toml
[display.players.2.buttons]
guide = ""                  # player 2's Guide does nothing
select = "window.next"      # and Select goes to the next app
```

Keyboards and mice are never taken from the apps. (Keys come from the compositor, which does not say which keyboard sent them, so key bindings are the same for every keyboard.)

