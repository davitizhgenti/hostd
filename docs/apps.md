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
| `args` | What `app.start` fills in: `{name}` in the command or URL. `args = { file = "path" }` with `command = ["imv", "-f", "{file}"]`; the kinds are `url` (http or https), `path` (absolute) and `text`. Started with new values while it runs, the app is replaced |
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

## The screen

One app is in front at a time, fullscreen. Super (or holding a controller's Guide button) opens the menu. Super+Tab and Super+Shift+Tab switch apps, and Super+Q closes the one in front. These keys can be changed in [`hostd.toml`](config.md).

While someone uses the screen (input in the last 5 minutes), an app started from elsewhere opens in the background and a notice says so. To bring it forward anyway, start it with `front = true`, which needs the `display.front` scope.

A screen-only extra: `hostctl action display.notice '{"text": "Dinner is ready"}'` shows a notice above any app.

## JupyterLab, shared

The `jupyter` add-on runs JupyterLab on `~/projects`, with real-time collaboration (`jupyter-collaboration`: everyone who opens a notebook edits it together, and sees the others' cursors) and a Git panel (`jupyterlab-git`) for your GitHub repositories.

```sh
sudo hostd-setup add jupyter                       # a few minutes the first time: its Python environment
ssh screen@<machine> 'cat ~/.config/hostd/jupyter/token'
```

Open `https://<machine>.local:8888` and give the token. It is an app like any other (`hostctl ps`, `hostctl logs jupyter`, `hostctl stop jupyter`), marked `autostart`, so it is back after a reboot.

**GitHub.** The add-on makes the machine a key of its own for GitHub (`~/.ssh/github_ed25519`, used for github.com only, whose host key it pins) and prints its public half until GitHub knows it. Add that once to your account, at <https://github.com/settings/ssh/new> as an authentication key; then the machine can clone, pull and push every repository you can. Revoke it there when you want it to stop. Tell git who commits:

```sh
ssh screen@<machine> "git config --global user.name 'Your Name' && git config --global user.email you@example.com"
```

Then clone into `~/projects`: in JupyterLab, the Git panel's *Clone a Repository* with `git@github.com:you/repo.git`, or `git clone` in its terminal. Commit, pull and push from the panel.

- **Who can reach it**: the home network, and devices on the machine's VPN (the `wireguard` add-on), with the token. Anyone with both can run code as the `screen` user, and so push to your repositories with the machine's key: share the token only with people you would trust that far.
- **HTTPS**: it serves the machine's certificate, from hostd's CA, and is restarted when hostd renews it. Browsers that trust the CA (see [Install](install.md#other-devices)) show no warning.
- **Packages**: install them from a notebook (`%pip install pandas`), into JupyterLab's own environment (`~/.local/share/hostd/jupyter/venv`). `sudo hostd-setup update jupyter` upgrades JupyterLab itself; the update timer never does.
- **A new token**: delete `~/.config/hostd/jupyter/token` and `hostctl stop jupyter && hostctl start jupyter`; the next start makes another.
