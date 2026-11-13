# Apps

hostd's catalog has the installed desktop apps (their `.desktop` files), Flatpak apps, and the apps you describe in **app files**: `~/.config/hostd/apps/<id>.toml`. The file name is the app's ID. Apps are [resources](resources.md): a new or changed file is picked up within a second, and `hostctl apps --all` lists the catalog, with any file that cannot be used on a row of its own and the reason beside it (the version before it, if there was one, stays in use).

```sh
hostctl apps                 # the catalog (--all: hidden apps too)
hostctl get apps jellyfin    # one: its file, or what hostd found
hostctl apply -f show.toml --kind app    # add or replace an app file (--check: only check)
hostctl edit app jellyfin    # change its file in $EDITOR
hostctl delete app jellyfin  # remove its file
hostctl open jellyfin        # start it, or bring it to the front if it runs (also: hostctl start)
hostctl ps                   # running instances
hostctl switch firefox#2     # bring an instance's window to the front (also: hostctl focus)
hostctl close firefox#2      # close it as its window's button would; stop it if it stays open
hostctl stop jellyfin        # end it now
```

## App files

```toml
# ~/.config/hostd/apps/jellyfin.toml
name   = "Jellyfin"
runner = { type = "docker", image = "jellyfin/jellyfin:latest", ports = ["8096:8096"] }
restart = "always"
env    = { TZ = "Europe/Tbilisi", API_KEY = "{secret:jellyfin-key}" }

[health]
http = "http://127.0.0.1:8096/health"
```

| Key | Meaning |
| --- | --- |
| `name`, `icon` | As shown in menus |
| `runner` | How it runs (below) |
| `surface` | `window` (on the screen) or `background`; the runner sets a default |
| `extends` | Change a discovered app instead of defining one: `extends = "firefox-esr"` |
| `env` | Its environment. `{secret:<name>}` takes a secret's value (see [Services](services.md#secrets)) |
| `args` | What `app.open` fills in: `{name}` in the command or URL. `args = { file = "path" }` with `command = ["imv", "-f", "{file}"]`; the kinds are `url` (http or https), `path` (absolute) and `text`. Started with new values while it runs, the app is replaced |
| `restart` | `always`, `on-failure` (background default) or `never` (window default) |
| `autostart` | Background apps: `true` starts the app when hostd starts (at boot, and after an update), unless it already runs. `hostctl stop` stops it until the next start |
| `requires` | Apps started first: `requires = ["database"]` |
| `hidden` | Not listed (still startable) |
| `under` | Listed in another app's actions box: `under = "steam"` |
| `[instance]` | `policy = "single"` (default) or `"multiple"`; `if_running = "focus"` (default), `"new"` or `"restart"` |
| `[window]` | `fullscreen` (true for installed desktop apps; set it in an app file); `wrap = "gamescope"`; `shown_by = "<app>"` for an app drawn in another's window |
| `[audio]` | `volume`: its own volume (0-150) when it starts |
| `[health]` | Background apps: `http` (a URL answering 2xx) or `tcp` (`host:port`), `start` (default 60s), `every` (default 10s) |
| `[match]` | Find its windows when they are not in its own processes: `class`, `app_id`, `title` (globs), `env` (`KEY=value`) |
| `[[actions]]` | Extra ways to start it: `id`, `name`, `command` (a new instance each time); `replace_actions = true` drops the desktop entry's |
| `[source]` | Makes it a service that deploys: see [Services](services.md) |

## Runners

| `type` | Runs | Keys |
| --- | --- | --- |
| `exec` | A command on the screen | `command = ["foot"]` |
| `process` | A command in the background | `command = ["./server"]` |
| `flatpak` | A Flatpak app | `app_id = "org.videolan.VLC"` |
| `url` | A web page in a kiosk browser | `url = "https://..."` |
| `docker` | A container (Podman or Docker) | `image`, `ports` |
| `compose` | A compose project | `file = "media/compose.yaml"` (beside the app file) |

Each instance runs in its own systemd unit or container. Stopping hostd never stops an app, and after a restart hostd finds its apps again.

**Handoff.** Some commands pass the app to another program and exit, such as `steam steam://rungameid/620`. `runner.handoff = "KEY=value"` makes the instance last as long as processes with that variable exist. A single-instance Flatpak app does this by default (`FLATPAK_ID`). The same variable finds the app when hostd did not start it: a game started from Steam's own menu has its app file's `SteamAppId`, so it becomes that app's instance, listed in the menu, in front on a workspace of its own, closed like any other, and ended with its processes. (An app that just ended is not looked for again for 30 seconds, while its processes finish.)

**Quitting.** Some apps keep running when their window closes (Steam goes to the background). `runner.quit = ["command", ...]` is how such an app quits cleanly: closing the instance (the menu's Close, Super+Q, `hostctl close`) runs it and waits up to 20 seconds for the app to end, before closing its windows and, if it still runs, stopping it. The steam add-on's Steam uses `quit = ["flatpak", "run", "com.valvesoftware.Steam", "-shutdown"]`.

## The screen

One app is in front at a time, fullscreen. Super (or holding a controller's Guide button) opens [the menu](design.md#the-menu). Super+Tab and Super+Shift+Tab switch apps, and Super+Q closes the one in front. These keys can be changed in [`hostd.toml`](config.md).

While someone uses the screen (input in the last 5 minutes), an app started from elsewhere opens in the background and a notice says so. To bring it forward anyway, start it with `front = true`, which needs the `display.front` scope.

A screen-only extra: `hostctl action display.notice '{"text": "Dinner is ready"}'` shows a notice above any app.
