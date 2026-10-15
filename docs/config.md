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

# External modules: programs in any language (see Writing a module).
[external.lights]
command = ["python3", "/home/screen/hostd/modules/lights.py"]
env     = { LIGHTS_BRIDGE = "192.168.1.20" }
```

In 0.1, `idle_after`, `browser` and `gamescope` were top-level keys, and the bindings were `[input]`, `[input.keys]` and `[input.buttons]`. hostd now refuses those names and says where each one moved. The audio, deploy and automation modules have no settings yet, so a section for one of them is an error.

The default bindings are: Super for the menu, Super+Tab and Super+Shift+Tab for the next and previous app, and Super+Q to close the app in front. Holding a controller's Guide button opens the menu.

## Drop-in files

Files in `~/.config/hostd/hostd.d/` add to `hostd.toml`: every `*.toml` there, in name order. A drop-in holds module sections (`[apps]`, `[display]`...) and external modules (`[external.NAME]`), so an add-on or a person can plug in a module, or change one setting, without editing `hostd.toml`. Anything else (`modules`, `listen`...) belongs in `hostd.toml`. A setting given in two files stops hostd with a message naming both; an external module counts as one setting, so it lives in one file.

```toml
# ~/.config/hostd/hostd.d/lights.toml
[external.lights]
command   = ["python3", "/home/screen/hostd/modules/lights.py"]
transport = "stdio"
```

## Files and folders

| Path | What |
| --- | --- |
| `~/.config/hostd/hostd.toml` | This file |
| `~/.config/hostd/hostd.d/*.toml` | [Drop-ins](#drop-in-files): modules and settings in files of their own |
| `~/.config/hostd/apps/` | [App files](apps.md) |
| `~/.config/hostd/scenes/`, `rules.toml`, `scripts/` | [Automation](automation.md) |
| `~/.config/hostd/secrets/` | [Secrets](services.md#secrets) (0600) |
| `~/.local/state/hostd/state.db` | Tokens and the audit trail |
| `~/hostd/git/`, `~/hostd/releases/`, `~/hostd/keys/` | [Services](services.md): repositories, releases, deploy keys |
| `/etc/hostd/update.conf` | [Updates](install.md#updates) |
