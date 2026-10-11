# Apps

hostd's catalog has the installed desktop apps (their `.desktop` files), Flatpak apps, and the apps you describe in **app files**: `~/.config/hostd/apps/<id>.toml`. The file name is the app's ID. Changes are picked up within a second. `hostctl apps --all` lists the catalog and any file that cannot be used, with the reason.

```sh
hostctl apps                 # the catalog
hostctl start jellyfin       # start, or bring to the front if it runs
hostctl ps                   # running instances
hostctl stop jellyfin
hostctl focus firefox#2
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
| `restart` | `always`, `on-failure` (background default) or `never` (window default) |
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

**Handoff.** Some commands pass the app to another program and exit, such as `steam steam://rungameid/620`. `runner.handoff = "KEY=value"` makes the instance last as long as processes with that variable exist. A single-instance Flatpak app does this by default (`FLATPAK_ID`).

## The screen

One app is in front at a time, fullscreen. Super (or holding a controller's Guide button) opens the menu. Super+Tab and Super+Shift+Tab switch apps, and Super+Q closes the one in front. These keys can be changed in [`hostd.toml`](config.md).

While someone uses the screen (input in the last 5 minutes), an app started from elsewhere opens in the background and a notice says so. To bring it forward anyway, start it with `front = true`, which needs the `display.front` scope.

A screen-only extra: `hostctl action display.notice '{"text": "Dinner is ready"}'` shows a notice above any app.
