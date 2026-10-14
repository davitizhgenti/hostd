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

Before this version `idle_after`, `browser` and `gamescope` were top-level keys, and the bindings were `[input]`, `[input.keys]` and `[input.buttons]`. Those names still work for one release; hostd logs where each one moved. A setting given both ways stops hostd. The audio, deploy and automation modules have no settings yet, so a section for one of them is an error.

The default bindings are: Super for the menu, Super+Tab and Super+Shift+Tab for the next and previous app, and Super+Q to close the app in front. Holding a controller's Guide button opens the menu.

## Files and folders

| Path | What |
| --- | --- |
| `~/.config/hostd/hostd.toml` | This file |
| `~/.config/hostd/apps/` | [App files](apps.md) |
| `~/.config/hostd/scenes/`, `rules.toml`, `scripts/` | [Automation](automation.md) |
| `~/.config/hostd/secrets/` | [Secrets](services.md#secrets) (0600) |
| `~/.local/state/hostd/state.db` | Tokens and the audit trail |
| `~/hostd/git/`, `~/hostd/releases/`, `~/hostd/keys/` | [Services](services.md): repositories, releases, deploy keys |
| `/etc/hostd/update.conf` | [Updates](install.md#updates) |
