# hostd: Display and Services Daemon Design

Oct 4, 2026 · @Davit

## Summary

hostd is a small core plus modules. The core only routes actions to modules and events to listeners; everything that touches the machine (apps and windows, audio, background services, deploys, automation) is a module. Adding a feature means adding a module, in Go or in any language over a socket, without changing the core. Server management (VPN, firewall, users) is out of scope for now.

| Topic | Decision |
| --- | --- |
| Window behavior | Fullscreen by default; multiple windows possible |
| Local use | Keyboard and mouse, game controller, and remote control may all happen |
| Command sources | Laptop (CLI), phone or other HTTP client, scripts and automations on the server |
| App list | Auto-discovered from installed apps (desktop entries, Flatpak); files override or add |
| App already running | Configurable per app: focus, new copy, or restart |
| Remote launch while someone uses the screen | Starts in the background; comes forward when someone switches to it |
| Background services | Kind not decided yet, so runners are pluggable; Docker first |
| Scripts | Any language; they act only through the CLI or HTTP API |
| Service updates | `git push` to the machine, or push to GitHub or any git host; images from a registry |
| Network | Home network only |
| Auth | Per-device tokens with scopes |
| Web control page | Not in v1; API and CLI only |
| After reboot | Empty screen; services restart per their policy |
| Audio | Master volume and mute, output switching, per-app volume, media controls |
| First milestone | A thin vertical slice of everything together |
| Language and platform | Go, Linux only, open source (Apache-2.0) |

## The core idea

The core understands exactly three things, and nothing about windows, sound or containers.

| Concept | Meaning | Example |
| --- | --- | --- |
| Module | A plug-in that owns part of the machine and declares what can be done with it | `audio` |
| Action | A request to change something, owned by one module | `audio.volume.set {"percent": 40}` |
| Event | A notice that something changed, emitted by a module | `audio.volume.changed` |

The core's whole job: load modules, check and route each action to the module that owns it, and deliver events to everyone listening.

**Contracts between modules.** A module never imports another. When one uses another's actions, events or reads (the display module places, focuses and closes the apps module's instances), it declares that in its manifest (`Requires`), and both sides take the names and data shapes from the `contract` package rather than repeating strings. Contract tests check that the owner's data still decodes into the contract type field for field. External modules (M6) will get the same contract as JSON, and the module SDK's `Core` interface is frozen at `sdk.CoreVersion`. Clients (the CLI, a phone, scripts, the on-screen menu) are not modules; they only send actions and read events.

**Built-in modules**

| Module | Owns | Namespaces | Example actions |
| --- | --- | --- | --- |
| `apps` | App catalog and running instances, window or background | `app.*`, `instance.*` | `app.start`, `instance.stop` |
| `display` | Outputs, windows, focus, presence | `display.*`, `window.*` | `window.focus`, `display.power` |
| `audio` | Sinks, per-app streams, media players | `audio.*`, `media.*` | `audio.volume.set`, `media.pause` |
| `deploy` | git repos and releases of background apps | `deploy.*` | `deploy.run`, `deploy.rollback` |
| `automation` | Scenes and rules | `scene.*`, `rule.*` | `scene.apply` |

No namespace is split between modules: starting and stopping an instance belongs to `apps` (`instance.*`), while everything about its window (focus, fullscreen, close, placement) belongs to `display` (`window.*`, addressed by instance ID).

**Where to expand**

| Extension point | Plugs into | Examples |
| --- | --- | --- |
| New module | Core | Lights, notifications, Home Assistant bridge, backups |
| Runner | `apps` | `exec`, `flatpak`, `url`, `docker`, `compose`, `process` |
| Catalog source | `apps` | Desktop files, Flatpak, TOML files |
| Backend | `display`, `audio` | Sway (v0), gamescope or KDE later; WirePlumber |

Inside the `apps` module an app is a runner plus a surface (`window` or `background`), and an instance is one running copy. A game and a website differ only in those two fields.

## Architecture

&#91;embedded content: hostd architecture · sources, API, action pipeline, controllers\]

Clients send actions to the core; the core checks them and routes each to the module that owns it, and modules send events back up through the same bus. Only modules and their plug-ins (dashed: runners, backends, your own modules) touch the machine. `automation` never touches the machine at all: it only sends actions to other modules.

hostd runs as a systemd user service of `screen` with lingering enabled, so it starts at boot. Background services work without a display; display and audio controllers attach when the Sway session appears.

## Module contract

Every module, built-in or external, implements the same small contract. Built-in modules are compiled into the daemon; external ones are separate programs speaking the same contract as JSON-RPC over a unix socket, so they can be written in any language.

```go
type Module interface {
    Manifest() Manifest                      // name, version, owned namespaces, actions + JSON schemas + resource keys, events, requires, scopes
    Start(ctx context.Context, core Core) error
    Validate(ctx context.Context, a Action) error         // re-check targets when the action reaches the front of its queue
    Handle(ctx context.Context, a Action) (Result, error)
    Stop(ctx context.Context) error
}

// What the core gives every module.
type Core interface {
    Emit(e Event)                                       // publish a state change (Resource set: bump its version)
    Do(ctx context.Context, a Action) (Result, error)  // ask another module, via the pipeline
    Subscribe(ctx context.Context, filter string) <-chan Event  // closed when ctx is done
}
```

**Four rules keep modules independent**

1. A module owns its namespaces: only `audio` handles `audio.*` and `media.*` actions and emits those events. Each manifest declares the namespaces it owns (`owns`); the core refuses overlapping claims at startup and rejects any action or event outside them. The core keeps `action.*`, `bus.*` and `core.*` for its own events.
2. Modules never touch each other's resources. Cross-module work goes through `core.Do`, so priority, permissions and the audit trail always apply. Example: `automation` applies a scene only by sending actions.
3. A module declares what it `requires` (`deploy` requires `apps`); the core starts modules in dependency order and refuses a missing dependency at startup.
4. Extension points inside a module follow the same pattern: the `apps` module accepts runners and catalog sources, `display` and `audio` accept backends, each as a Go interface for built-ins or the socket contract for external ones.

**Conditional scopes.** An action names the scope it needs, plus `arg_scopes` for scopes needed only when a boolean argument is true: `app.start` declares `{"front": "display.front"}`.

**Resource keys** for each action are declared in the manifest as templates over its arguments (`"instance:{id}"`, `"audio.stream:{instance}"`), so built-in and external modules declare them the same way. `Validate` exists so the core can refuse a stale target with a uniform error code before `Handle` runs, and so the audit trail can tell a refused action from a failed one.

**Enabling modules** is one line of config, so a headless machine simply leaves out `display` and `audio`:

```toml
# ~/.config/hostd/hostd.toml
modules = ["apps", "display", "audio", "deploy", "automation"]

[modules.external.lights]
exec = "/opt/hostd-lights/run.py"
```

From a manifest the core generates everything else: API routes, CLI commands (`hostctl <module> <action>`), token scopes and OpenAPI entries. The core itself defines only the `read` and `admin` scopes; every other scope is declared by a module's manifest, and each action names the scope it needs (`app.start` needs `apps`, plus `display.front` to force a window to the front). A new module needs no change to the core, the API or the CLI.

## Module: apps

The app catalog is built automatically from what is installed, then merged with hand-written files that override or add entries. Discovery re-runs at startup, on a file-system change in the watched folders, and on `hostctl apps rescan`.

**Discovery sources**

| Source | Where it looks | Becomes |
| --- | --- | --- |
| Desktop entries | `/usr/share/applications`, `~/.local/share/applications` | `exec` app, `window` surface |
| Flatpak | `/var/lib/flatpak/exports/share/applications`, user exports | `flatpak` app |
| Definition files | `~/.config/hostd/apps/*.toml` | Anything, including `docker`, `compose`, `url` apps |

**IDs.** Each app gets a stable, readable ID: the desktop file name (`firefox`) or the Flatpak ID's last part (`retroarch`). Files can set an alias (`id = "dota2"`).

**App actions.** An app can have extra ways to start it, such as a private browser window. They come from the desktop entry's `[Desktop Action]` groups, so most browsers and editors bring their own. App files add or replace them with `[[actions]]` (`id`, `name`, `command`). `app.start` with `action = "<id>"` runs one as a new instance, recorded in the instance (`action`). The menu shows them in a small box at the entry: right click, the Menu key, or the controller's X button. The box also has focus and close for a running app, and start for one that is not running.

**No app is built in.** Steam, a browser or an emulator is an app like any other; hostd has no code for any one of them. Launchers fit through two generic features: **handoff** (`runner.handoff = "KEY=value"`) for commands that hand the app to another program and exit, so the instance runs while processes with that variable exist; and **match rules** for windows that are not in the instance's unit. Flatpak moves each app into a scope of its own, so a single-instance Flatpak app hands off by default to `FLATPAK_ID=<id>`, which every process in its sandbox has. When several instances' rules match a window, the one matching most rules wins (a Steam game's window has Steam's `FLATPAK_ID` too). An action of a handoff app that already runs (Steam's Big Picture) is passed to the running program rather than becoming an instance of its own. Discovering a game library (Steam's, say) can be an optional external module.

**Definition file**

```toml
# ~/.config/hostd/apps/dota2.toml
name     = "Dota 2"
# Steam starts the game and this command exits: follow the game by the
# variable Steam gives its processes.
runner   = { type = "exec", command = ["steam", "steam://rungameid/570"], handoff = "SteamAppId=570" }

[match]
class = "steam_app_570"      # Proton games' window class (handoff alone also matches by SteamAppId)

[window]
fullscreen = true
wrap       = "gamescope"     # optional: run inside gamescope for stable fullscreen

[instance]
policy  = "single"           # single | multiple
if_running = "focus"          # focus | new | restart

[audio]
volume = 70                   # per-app volume applied on start
```

```toml
# ~/.config/hostd/apps/jellyfin-tv.toml
id       = "jellyfin-tv"
name     = "Jellyfin"
runner   = { type = "url", url = "http://localhost:8096" }
surface  = "window"
requires = ["jellyfin"]      # a background service app
```

**Instance policy.** Every app has `policy` and `if_running`. Defaults: `window` apps are `single` with `focus`; `background` apps are `single` with `focus` meaning "already running, do nothing". Starting a `single` app that is running applies `if_running`; a `multiple` app always gets a new instance (`firefox#2`).

**Hidden apps.** Discovery finds many tools nobody wants on a TV (settings panels, terminals). Files can set `hidden = true`, and a default ignore list hides common system entries.

## Module: display

The screen runs Sway, and each window instance gets its own workspace, fullscreen by default. "Switching apps" is switching workspaces, and "launch in the background" is opening on a workspace nobody is looking at. That one rule gives fullscreen-by-default, multiple windows, and background launches without fighting the compositor.

**Session**

- A dedicated, non-admin Linux user `screen` autologs in via greetd; its session runs Sway, PipeWire and `hostd`.
- After boot the screen is empty (a plain background). Nothing is restored.
- Splitting a workspace to show two windows side by side is allowed (`window.place <id> beside <id>`), but never the default.

**Matching windows to instances.** Apps often spawn helper processes, and some are started by another program (Steam starts games; Flatpak runs apps in a scope of its own), so a window's process ID alone is not enough. hostd matches in order:

1. **cgroup:** every instance runs as its own transient systemd user service, `hostd-<instance>.service`; the window's PID is looked up in `/proc/<pid>/cgroup` to find the unit.
2. **Match rules:** the app's `match` block: globs on `class`, `app_id` and `title`, or `env = "KEY=value"` in the window's process (Flatpak apps default to `FLATPAK_ID=<id>`, handoff apps to their handoff variable). Any rule that is set and matches is enough.
3. **Unowned:** windows that match nothing (a dialog opened by hand) still appear in `/windows`, with no instance.

**Game controllers.** The kernel drives most pads as input devices (xpad for Xbox, hid-playstation, hid-nintendo, hid-wiimote). hostd recognises them by **profiles**: small TOML files that match a USB vendor, products or a name, and say which evdev key each of hostd's buttons is (`guide`, `start`, `select`). Profiles are built in (Xbox, PlayStation, Switch, 8BitDo, Wii Remote, DolphinBar), and `~/.config/hostd/controllers/*.toml` adds to or replaces them. A gamepad no profile knows gets a generic one, with Guide on `BTN_MODE`. A raw device with no input device (a Mayflash DolphinBar in Dolphin mode) is listed as its app's (`raw`, `app`). Supporting a new kind of controller means adding a file, not code. `GET /v1/controllers` (and `hostctl controllers`) lists what is connected; `controller.connected` and `controller.disconnected` announce changes.

**Add-ons.** Optional software comes as installer add-ons (`deploy/addons/<name>/addon.sh`): `sudo hostd-setup add NAME`, `remove NAME`, `addons` to list them. Installed ones are recorded in `/etc/hostd/addons` and applied again by every install and update, which also updates them. They live outside hostd's core:
- **controllers** installs udev rules giving the screen session raw HID and uinput access for common pads.
- **flatpak** sets up Flatpak with Flathub, and updates every Flatpak app and runtime, including the NVIDIA GL runtime that must match the driver.
- **steam** installs the Steam Flatpak, started in Big Picture. Its `hostd-steam-games` writes an app file per installed game (a handoff to Steam, followed by `SteamAppId`), kept current by a user path unit.
- **dolphin** installs Dolphin, which reads Wii Remotes through a DolphinBar.

**Input and shortcuts**

Every key or button that changes the screen is a named input, mapped by one table to a display action. The action goes through the core like any other, with source `local`, so it is audited, outranks phones and scripts, and gets the same protection.

- **Keys** reach hostd through Sway: hostd installs its bindings at runtime over IPC (`bindsym … nop hostd key <name>`), and Sway reports each press back as a binding event. No scripts, no tokens; it works with any keyboard, VNC included. hostd installs them again after a config reload.
- **Controller buttons** come from hostd's evdev reader. Only buttons with no in-game use are bindable (today the Guide button), because games own the rest. Even those reach the app as well: hostd cannot take a button from apps that read the controller themselves (Steam opens its own menu on Guide). So a button's action runs only when it is **held** (`[input] hold`, 600 ms by default); a tap stays the app's.
- **Apps handle their own input.** The menu moves through its list itself, but going back or closing an app are actions it sends to the API.

The model: Super is the system key. Super on its own opens the menu, Super+key acts on the app in front, and holding Guide is the controller's Super.

| Input | Action |
| --- | --- |
| Super | `display.menu` (open, or back if it is in front) |
| Super+Tab / Super+Shift+Tab | `window.next` / `window.prev` |
| Super+Q | `window.close` (the app in front: politely, then stop) |
| Guide (held) | `display.menu` |

`[input.keys]` and `[input.buttons]` in `hostd.toml` add or change bindings; `""` removes one. Only display actions that need no arguments can be bound, and a typo stops hostd with a clear message.

**Focus and the person at the screen**

| Situation | What happens |
| --- | --- |
| Command comes from someone at the screen (keyboard, controller, local launcher) | App opens in front |
| Remote or automated launch, nobody active at the screen in the last 5 minutes | App opens in front |
| Remote or automated launch while someone is active | App opens on a background workspace; a small on-screen notice says it is ready |
| Remote command with `--front` (scope `display.front` required) | App opens in front regardless |
| App closes | The previously focused instance comes back to the front |

"Active" means keyboard, mouse or controller input. The idle threshold is configurable.

**The menu.** People at the screen need a way to reach background apps. hostd ships a small on-screen menu (a fullscreen list of running instances plus the catalog), opened by Super on a keyboard or by holding the Guide/Home button on a controller. It uses the same API as remote clients. Controller input is read from evdev devices, which needs the `screen` user in the `input` group.

The menu is an ordinary fullscreen window app (`hostd-menu`, hidden from the catalog list) that runs as an instance on its own workspace; opening it is the `display.menu` action, sent with source `local`. It needs no special Wayland protocol, and the display module manages it like any app.

**On-screen notices** ("Jellyfin is ready") are desktop notifications sent over D-Bus (`org.freedesktop.Notifications`) and drawn by the session's notification daemon (`mako`), configured with `layer=overlay` so they appear above fullscreen windows; mako's default layer is hidden under them. hostd itself contains no Wayland protocol code.

**Display actions:** `display.power` on/off (DPMS), `display.mode` (resolution and refresh), `display.output.enable`, plus `window.focus`, `window.fullscreen`, `window.close` (polite close request, then `instance.stop` after a timeout) and `window.place`, each addressed by instance ID.

## Module: audio

Audio runs on PipeWire with WirePlumber in the `screen` session; hostd controls it and mirrors its state from PipeWire events, so changes made elsewhere (a game's own volume slider, a Bluetooth headset connecting) show up in the API.

| Action | Backend | Notes |
| --- | --- | --- |
| `audio.volume.set` / `audio.mute.set` | WirePlumber default sink | Absolute 0–150 %; relative `+5`/`-5` allowed for buttons |
| `audio.output.set` | WirePlumber default sink | HDMI, analog speakers, Bluetooth; outputs listed by stable name, not numeric ID |
| `audio.app.volume.set` / `audio.app.mute.set` | Stream nodes linked to an instance | Streams matched to instances by PID and cgroup, like windows |
| `media.play` / `pause` / `next` / `previous` | MPRIS over D-Bus | Targets the focused instance's player, or a named one |

Outputs need stable IDs because PipeWire's numeric node IDs change on every reconnect. hostd names them from device properties (`hdmi-1`, `speakers`, `bt-sony-wh1000`) and keeps the name when the device returns.

The initial version uses `wpctl` and `playerctl` as subprocesses, behind a Go interface. A native PipeWire client can replace them later without changing the API.

## Background services and the deploy module

Background services are ordinary apps with `surface = "background"`. They share the instance model, actions and events with window apps, so `hostctl stop blog` works the same as `hostctl stop dota2`. Because the kind of service is not decided yet, runners are pluggable behind one Go interface (`Start`, `Stop`, `Status`, `Logs`, `Events`, `Adopt`).

**Window apps run as transient services, not scopes.** The `exec`, `flatpak` and `url` runners start each instance as a transient systemd user service, `hostd-<instance>.service` (`Type=exec`, what `systemd-run --user` does). A scope can only adopt a process that already exists, so hostd would have to fork the app itself; the app would then be hostd's child, and after a hostd restart nothing could collect its exit status. With a service, systemd starts and owns the process and records its exit status, so hostd can re-adopt instances after a restart. Because the systemd user manager may not have the graphical session's environment, hostd sets `WAYLAND_DISPLAY`, `XDG_RUNTIME_DIR`, `XDG_SESSION_TYPE`, `DBUS_SESSION_BUS_ADDRESS` and `SWAYSOCK` explicitly in each unit, plus the app's `[env]`.

| Runner | Runs | Backend |
| --- | --- | --- |
| `docker` | One container from an image or a Dockerfile | Docker Engine API over a socket; works with rootless Docker or Podman's Docker-compatible socket |
| `compose` | A compose project as one instance | `docker compose` against the same socket |
| `process` | Any script or binary, in any language | A systemd user service generated by hostd, with restart policy and journald logs |

**Service definition**

```toml
# ~/.config/hostd/apps/blog.toml
id      = "blog"
surface = "background"
runner  = { type = "docker", build = ".", ports = ["8080:80"] }
source  = "git"              # deployable with git push
restart = "always"           # always | on-failure | never

[env]
NODE_ENV = "production"

[health]
http = "http://localhost:8080/healthz"
```

**Deploy with `git push`**

1. `hostctl service init blog` creates a bare repo at `~/hostd/git/blog.git` and prints the remote: `screen@mini-pc:hostd/git/blog.git`.
2. On the laptop: `git remote add box …` once, then `git push box main`.
3. The `post-receive` hook checks out the commit into `~/hostd/releases/blog/<sha>` and calls `hostctl service deploy blog <sha>`. Output streams back to the laptop terminal.
4. hostd builds the image (or, for `process`, runs an optional `build` command), starts the new instance, waits for the health check, switches traffic to it, then stops the old one.
5. If the health check fails, the new instance is removed and the old one keeps running. The last 5 releases are kept for `hostctl service rollback blog`.

Pushing over SSH reuses the laptop's existing SSH key, so no separate deploy credentials are needed.

**Zero-downtime switch.** For HTTP services, hostd owns each public port (`ports = ["8080:80"]`) with a small built-in reverse proxy (Go's `httputil.ReverseProxy`). Each release listens on an internal port that hostd picks; after the health check passes, the proxy's upstream is swapped atomically, requests still running on the old release finish, and only then is the old release stopped. Services that are not HTTP fall back to stop-then-start, with a short gap.

### Deploy sources: git push, GitHub or any git host

Pushing straight to the machine is one of four ways an update can arrive. All four end in the same deploy steps (build or pull, health check, switch, rollback on failure); they differ only in how hostd learns about a new version.

| Source | How hostd notices a new version | Works with home-network-only API | Best for |
| --- | --- | --- | --- |
| `push` | You push to the machine's bare repo over SSH | Yes | Quick iteration from the laptop |
| `remote` | Polls the branch with `git ls-remote` (default every 60 s), then fetches the new commit | Yes, outbound only | Pushing to GitHub as usual; also GitLab, Gitea, Codeberg or any git server |
| `image` | Polls a registry tag (e.g. GHCR) for a new image digest and pulls it | Yes, outbound only | GitHub Actions builds and tests the image; the machine only pulls |
| `webhook` | GitHub calls `POST /v1/hooks/git/<app>`; the signature is verified with the webhook secret | No: the endpoint must be reachable from the internet | Instant deploys, once the machine is exposed through a tunnel or relay |

Because the API stays on the home network, `remote` is the default for GitHub: it needs no open ports, and a delay of up to a minute is fine for home services. An app can use several sources at once, for example `push` for experiments and `remote` for the main branch.

```toml
# follow a GitHub branch
[source]
type   = "remote"
url    = "git@github.com:me/blog.git"
branch = "main"             # or: tag = "v*" to deploy only matching release tags
poll   = "60s"
key    = "blog"             # deploy key, see below
```

```toml
# pull an image that GitHub Actions built
[source]
type  = "image"
image = "ghcr.io/me/blog"
tag   = "main"
poll  = "2m"
auth  = "ghcr-token"        # secret name, only for private images
```

**Private repositories.** `hostctl deploy key blog` generates a per-app SSH key pair and prints the public key, to be added on GitHub under the repository's Deploy keys as read-only. Registry credentials for private images are stored as secrets (`hostctl secret set ghcr-token`) and need only read access. hostd never needs write access to any remote.

**Secrets** are stored unencrypted in files with mode 0600 under `~/.config/hostd/secrets/`, outside any config repository that hostd follows. Encrypting them with a key kept on the same disk would add little. This protects against other local users and against secrets being committed with the config; it does not protect against anything running as `screen` or against someone holding the disk. TPM-backed `systemd-creds` is a candidate for a later version.

**Recommended GitHub workflow.** For anything that takes long to build, let GitHub Actions build and test the image and push it to GHCR, and use the `image` source. The mini PC then never compiles anything, and only tested images are deployed. Use `remote` for small apps that build quickly on the machine, and `push` for experiments.

**Config from git too.** The hostd config folder (app definitions, scenes, rules) can follow a repository the same way: `hostctl config follow git@github.com:me/hostd-config.git`. On each new commit hostd validates the whole config first. A valid config is applied and reloaded without a restart; an invalid one is rejected, the previous config stays active, and a `config.rejected` event explains why.

**Every deploy is an action.** Deploys triggered by a source run as `deploy.run` with source `automation` on the resource key `deploy:<app>`, so they queue behind each other and appear in the audit trail with the commit SHA, author and message. `hostctl deploy status` shows each app's source, current version, last check and last result.

**Running after reboot.** The `screen` user has systemd lingering enabled, so services with `restart = "always"` start at boot, before and independent of the display session. Window apps never auto-start.

## Core: actions, concurrency and conflict rules

Every change, from any source, is an action that passes through one pipeline: validate, check permission, check priority, queue, execute, publish an event. Nothing in hostd changes the system any other way, which is what makes many simultaneous controllers safe.

**An action**

```json
{
  "id": "act_01J9…",
  "type": "audio.volume.set",
  "args": { "percent": 40 },
  "expect_version": 17,
  "source": { "kind": "script", "name": "evening.sh", "token": "laptop" },
  "cause": "evt_01J9…",
  "parent": "act_01J8…"
}
```

`expect_version` is optional (see stale state below); `cause` is set automatically when an action is sent in reaction to an event. `parent` is set automatically when a module sends an action through `core.Do` while handling another one; it is how the core recognizes a child action (see resource keys below).

**Pipeline**

1. **Validate** arguments against the action's schema; reject unknown targets ("instance `dota2#1` is not running") before anything happens. Targets are checked again by the module's `Validate` when the action reaches the front of its queue.
2. **Authorize** against the token's scopes.
3. **Prioritize** against recent actions on the same resource (rules below).
4. **Queue** on the resource's queue. Each resource (master audio, each output, each instance, the focus, the display) has its own FIFO queue, so actions on one resource never race, and unrelated ones run in parallel.
5. **Execute** through the controller (Sway, PipeWire, a runner), with a timeout per action type. On timeout the caller gets `timeout` at once, but the action keeps its resource keys until its handler really returns, so nothing races it; the outcome follows as an event. If the handler is still busy after a second timeout, its context is cancelled.
6. **Publish** the result as an event with the action ID, so the caller and every listener see the outcome. A module links its own events to the action it is handling by setting the event's `action`; events that change a resource are published after the action finishes, carrying the version it produced.

### Conflict handling

There are seven kinds of conflict, and each has one mechanism. All of them work because every change goes through the pipeline, so the core always knows what is happening, in what order, and who asked.

| Conflict | Example | Mechanism |
| --- | --- | --- |
| Same moment | Phone sets volume 30 while a script sets 70 | One queue per resource |
| Sources disagree | You lower the volume; a rule raises it 2 minutes later | Priority with a hold window |
| Screen in use | A remote launch while someone is playing | Focus protection |
| Stale state | A script focuses Firefox, which closed a second ago | Validate at execution, versions, clear errors |
| Loops | Rule A triggers rule B, which triggers rule A | Cause chain with depth and rate limits |
| Outside changes | A game changes its own volume | Modules mirror the real system |
| Module vs module | `automation` wants to change audio | Ownership: only the owning module acts |

#### 1. Same moment: one queue per resource

Every action locks one or more resource keys, declared in the module's manifest as templates over the arguments:

| Resource key | Locked by |
| --- | --- |
| `audio.master` | Master volume and mute |
| `audio.output` | Default output switching |
| `audio.stream:<instance>` | Per-app volume and mute |
| `instance:<id>` | Stop, close, fullscreen, place of one instance |
| `display.focus` | Focus changes and front launches |
| `display.power` | Power and mode |
| `deploy:<app>` | Deploys and rollbacks of one app |

Actions on the same key run strictly in arrival order; actions on different keys run in parallel. An action needing several keys takes them in sorted order, which rules out deadlocks. Each sender receives its own result, and the events show the final state, so a sender whose change was later replaced can see it.

**Child actions reuse their parent's keys.** A module keeps its keys while its `Handle` runs, and it may call `core.Do` from there. Without a rule for this, a child needing a key its parent holds would wait for the parent forever. Example: `app.start firefox` with `if_running = "restart"` holds `instance:firefox` and sends `instance.stop firefox`, which needs the same key. So a child action (one with `parent` set) does not queue for keys already held by its parent chain; it queues only for the keys it adds. This applies only along `parent`, never along `cause`: actions sent in reaction to an event run on their own and queue normally.

#### 2. Sources disagree: priority with a hold window

| Priority | Source | Includes |
| --- | --- | --- |
| 3 | `local` | Keyboard, mouse, controller, the on-screen menu |
| 2 | `manual` | You or another person via CLI, phone or HTTP client, with a device token |
| 1 | `automation` | Rules, and scripts running with a script token |

Priority comes from the origin of a chain: a scene you apply by hand runs at `manual`; the same scene applied by a rule runs at `automation`.

When an action of priority P changes a resource, the resource is held at P for the hold window (default 3 minutes, configurable per resource key). During the hold:

- Actions of priority ≥ P apply normally and renew the hold at their own priority.
- Actions of lower priority are skipped and return `{"status": "skipped", "reason": "held", "held_by": "manual", "until": "…"}`. Skipping is not an error, so scripts and rules continue.

After the window expires, any source can change the resource again. A human therefore always beats automation, and automation cannot immediately undo what someone just did.

#### 3. Screen in use: focus protection

Presence means keyboard, mouse or controller input within the last 5 minutes. While someone is present, any non-`local` action that would change focus (`app.start`, `window.focus`) runs with `front = false`: the app starts on a background workspace and the screen shows a short notice. Forcing it to the front requires `front = true` and the `display.front` scope. When nobody is present, remote launches come to the front as usual. Details are in the display module section.

#### 4. Stale state: validate at execution, versions, clear errors

- **Validated at the last moment.** Arguments and targets are checked when the action reaches the front of its queue, not when it was sent. A missing target returns a specific error code instead of acting on the wrong thing.
- **Absolute and repeatable.** `volume.set 40` gives the same result regardless of what ran before; relative forms exist only for buttons. Starting a running `single` app applies its `if_running` policy instead of failing or duplicating.
- **Versions for strict callers.** Every resource has a version number that increases on each change and is returned by every read and event. An action with `expect_version` is refused with `precondition_failed` (and the current state) if the resource changed since the caller looked. It is optional; most callers never need it.

**Stable error codes** that scripts can branch on:

| Code | Meaning |
| --- | --- |
| `invalid_args` | Arguments fail the action's schema |
| `not_found` | Unknown app, instance, output or window |
| `instance_not_running` | The target instance has exited |
| `precondition_failed` | `expect_version` did not match |
| `unauthorized` | No token, or an invalid, expired or revoked one (HTTP 401) |
| `forbidden` | Token lacks the scope |
| `timeout` | The module did not finish in time; the outcome is reported later by event |
| `module_unavailable` | The owning module is not running, e.g. `display` before the session starts |
| `loop_detected` | The cause chain is too deep, or the rule that sent the action is paused (see loops below) |
| `internal` | Anything else: a bug or an unexpected system error |

#### 5. Loops: cause chain with limits

Every action triggered by an event carries that event's ID, and every event carries the ID of the action that caused it, so the core can follow any chain back to its origin.

- A chain deeper than 5 rule-triggered steps is stopped.
- A rule firing more than 10 times in one minute is paused for 10 minutes.
- Both produce an `action.loop_detected` event naming the rules involved, and `hostctl rules` shows paused rules.
- Rules can also set their own `cooldown`.

#### 6. Outside changes: modules mirror the real system

Modules subscribe to the system's own events (Sway IPC, PipeWire, Docker), so hostd's state stays true when something changes outside it: a game's own volume slider, `pactl` typed by hand, a headset connecting. Such changes are recorded with source `external`, update the resource version, and are published as normal events. They do not create a hold, because hostd cannot tell whether a person or a program made them.

#### 7. Module vs module: ownership

A module may handle only actions in its own namespace and emit only its own events; the core rejects anything else at registration. Cross-module work always goes through `core.Do`, so all six mechanisms above apply to modules as well. Example: `automation` applies a scene only by sending `audio.*`, `apps.*` and `display.*` actions.

#### Multi-step sequences

There are no locks spanning several actions in v1. One limit of child actions reusing their parent's keys: a child that waits for a key held by an unrelated action, which in turn waits for a key of the child's ancestors, would deadlock; the parent's timeout then reports it. Modules avoid this by sending child actions only for resources their action already holds or for keys no other action combines with theirs. Something that must reach several states together should be a scene, which applies its steps in a fixed order and reports each step's result. Short-lived leases ("keep the display for me for 10 seconds") are a candidate for a later version.

### Audit trail

The last 10,000 actions are stored with time, type, arguments, source, cause, result and duration, readable via `GET /v1/actions` and `hostctl log`. This answers "why did my volume change?" directly:

```
19:02:11  audio.volume.set 30   manual (phone)         applied   v17
19:04:05  audio.volume.set 70   rule "gaming audio"    skipped   held by manual until 19:05:11
19:06:40  audio.volume.set 55   external (pipewire)    observed  v18
19:07:30  audio.volume.set 70   rule "gaming audio"    applied   v19
```

## Module: automation, and scripts

Scenes describe a result, rules decide when to reach it, and scripts handle real logic. All three end up as actions in the same pipeline.

**Scenes** are TOML files in `~/.config/hostd/scenes/`. Applying one diffs it against current state and emits only the missing actions, in a fixed order: stop apps, audio output, volume, start apps, focus, display power.

```toml
# scenes/movie.toml
[audio]
output = "hdmi-1"
volume = 40

[apps]
stop    = ["dota2"]
running = ["jellyfin-tv"]
focus   = "jellyfin-tv"

[display]
power = "on"
```

Applying a scene twice does nothing the second time. Each step's result is returned; a failed step does not undo earlier ones, but the result lists exactly what applied and what did not.

**Rules** live in `~/.config/hostd/rules.toml`:

```toml
[[rule]]
name = "gaming audio"
on   = "instance.started"
if   = { app = "dota2" }
do   = { scene = "gaming" }

[[rule]]
name     = "screen off when idle"
on       = "display.idle"
if       = { minutes = 30 }
do       = { scene = "screen-off" }
cooldown = "10m"
```

Rules match events by type and simple conditions (app ID globs, time ranges, idle minutes). Anything more complex calls a script: `do = { script = "evening.py" }`.

**Scripts** can be any executable. hostd runs them with three environment variables set, so they need no configuration:

| Variable | Value |
| --- | --- |
| `HOSTD_URL` | The local API address, e.g. a unix socket path |
| `HOSTD_TOKEN` | A short-lived token limited to the script's declared scopes |
| `HOSTD_EVENT` | The JSON of the event that triggered it, if any |

A script then uses the CLI or plain HTTP:

```sh
#!/bin/sh
hostctl audio volume set 40
hostctl start jellyfin-tv
hostctl focus jellyfin-tv
```

Scripts declare their scopes in a header comment (`# hostd-scopes: audio display`) or a sidecar TOML file. Each run gets a timeout (default 60 s) and is logged with its exit code and the actions it sent.

## API, events and CLI

One HTTP API (`/v1`) with JSON bodies, plus a WebSocket for events. Reads are plain REST; every change is a POST that becomes an action, and returns its result (or `202` with the action ID for slow ones like builds).

**Generic by design.** Any module's action can be sent to `POST /v1/actions` as `{"type": "audio.volume.set", "args": {"percent": 40}}`, and any module's state read at `GET /v1/state/<module>`. The friendly routes below are generated from module manifests, so a new module gets its routes without changes to the core.

**Endpoints**

| Method and path | Action / purpose | Scope |
| --- | --- | --- |
| `GET /v1/apps` · `GET /v1/apps/{id}` | Catalog | `read` |
| `POST /v1/apps/rescan` | Re-run discovery | `apps` |
| `POST /v1/apps/{id}/start` | `app.start` (`front`, `args`, `fullscreen`) | `apps` |
| `GET /v1/instances` | Everything running, window and background | `read` |
| `POST /v1/instances/{id}/stop` | `instance.stop` | `apps` |
| `GET /v1/windows` | All windows, including unowned | `read` |
| `POST /v1/windows/{instance}/focus` · `/close` | `window.focus`, `window.close` | `apps` |
| `POST /v1/windows/{instance}/fullscreen` · `/place` | `window.fullscreen`, `window.place` | `display` |
| `GET /v1/display` · `POST /v1/display/power` · `/mode` | Display state and actions | `display` |
| `GET /v1/audio` · `POST /v1/audio/volume` · `/mute` · `/output` | Master audio | `audio` |
| `POST /v1/audio/apps/{instance}/volume` · `/mute` | Per-app audio | `audio` |
| `POST /v1/media/{play,pause,next,previous}` | MPRIS | `audio` |
| `POST /v1/services/{id}/deploy` · `/rollback` · `GET …/logs` | Services | `services` |
| `POST /v1/scenes/{id}/apply` | Scene | `scenes` |
| `GET /v1/actions` | Audit trail | `read` |
| `POST /v1/tokens` · `DELETE /v1/tokens/{id}` | Token management | `admin` |
| `GET /v1/events` (WebSocket) | Event stream, filterable by type | `read` |

The event stream accepts the token either in the `Authorization` header or as a WebSocket subprotocol, because browsers cannot set headers on WebSocket connections and a phone page will need it later: the client offers `hostd.v1` and `hostd.token.<token>`, and the server answers `hostd.v1`, never echoing the token. The subprotocol is preferred over a query parameter so tokens stay out of access logs. `?since=<event id>` replays what a reconnecting client missed from the last 1,000 events; if that is too long ago, the stream starts with a `bus.gap` event.

**HTTP status per error code:** `invalid_args` 400, `unauthorized` 401, `forbidden` 403, `not_found` 404, `instance_not_running` and `loop_detected` 409, `precondition_failed` 412, `internal` 500, `module_unavailable` 503, `timeout` 504. Every response carries an `X-Request-ID` (the caller's, if it sent one).

**Errors** use one shape: `{"error": {"code": "instance_not_running", "message": "…", "action": "act_…"}}` with stable codes scripts can branch on. Skipped actions return `200` with `"status": "skipped"` and the reason, not an error.

**Events** (all carry a timestamp and, when caused by an action, its ID and source): `instance.starting`, `instance.started`, `instance.exited`, `instance.failed`, `window.opened`, `window.focused`, `window.closed`, `audio.volume.changed`, `audio.output.changed`, `media.changed`, `display.power.changed`, `display.idle`, `display.active`, `display.notice`, `deploy.started`, `deploy.done`, `deploy.failed`, `deploy.rolled_back`, `action.done`, `action.skipped`, `action.failed`, `action.loop_detected`, `bus.lagged` (the last event a subscriber gets before it is dropped for falling behind).

**CLI.** `hostctl` is a thin client of the same API, so anything the CLI can do, a script or phone can do. It reads the server address and token from `~/.config/hostctl/config.toml` on the laptop, or from `HOSTD_URL`/`HOSTD_TOKEN` inside scripts.

```sh
hostctl apps                         # list the catalog
hostctl start dota2 --front          # launch in front
hostctl ps                           # running instances
hostctl focus firefox                # by app ID when only one instance runs
hostctl audio volume set 40
hostctl audio app firefox volume 30
hostctl audio output set bt-sony-wh1000
hostctl media pause
hostctl scene apply movie
hostctl events --type 'instance.*'   # live event stream
hostctl log                          # recent actions and their sources
```

The OpenAPI document is generated from the Go action definitions and served at `/v1/openapi.json`, so clients in other languages (and a phone app later) can be generated from it.

## Security and access

The API is reachable only from the home network, every request needs a token, and hostd runs without root as the `screen` user.

**Listening**

- A unix socket at `$XDG_RUNTIME_DIR/hostd.sock` for local scripts and the CLI on the machine itself.
- TCP port 7300 bound to the LAN interface only, with a source check that rejects non-private addresses. IPv4-mapped IPv6 addresses (`::ffff:a.b.c.d`, as a dual-stack socket reports IPv4 clients) are unwrapped before the check. A one-line config change enables HTTPS with a self-signed certificate pinned by `hostctl`.

**Tokens and scopes**

| Scope | Allows |
| --- | --- |
| `read` | All GET endpoints and the event stream, except `/v1/version` and `/v1/manifests`, which any valid token may read (they describe the API, and hostctl needs them) |
| `apps` | Start and stop instances, focus and close their windows |
| `display` | Display power and mode, window fullscreen and placement; `display.front` to force a window in front |
| `audio` | Volume, mute, outputs, per-app audio, media |
| `scenes` | Apply scenes |
| `services` | Deploy, roll back, restart services |
| `admin` | Tokens, config reload, everything else |

- The first start prints a one-time `admin` token. `hostctl login` on the laptop stores it.
- Each device or script gets its own named token (`hostctl token create phone --scopes read,apps,audio`). Tokens are stored hashed (SHA-256; secrets start with `hostd_` so leaked ones are easy to spot) and can be revoked individually.
- Every token has a **kind** that sets the priority of the actions sent with it: `manual` for a person's device (the default), `automation` for scripts, `local` for the on-screen menu. A token can only create tokens with scopes it has itself.
- Scripts run by rules get short-lived tokens limited to their declared scopes.

**Process isolation**

- hostd and the display session run as `screen`, a user without sudo.
- Docker group membership is equivalent to root, so the default runner targets rootless Docker or Podman's rootless socket. Plain Docker works but is documented as a weaker setup.
- `process` services run as systemd user units with `NoNewPrivileges` and a private `/tmp`.
- git push access is limited by SSH `authorized_keys` with a forced command, so a deploy key can only push, not open a shell.

## Updating hostd itself

hostd can be updated two ways: pushed from the laptop, or fetched from GitHub. Both install the new version side by side, restart into it, and roll back automatically if it fails to come up. Running games, apps and services are not interrupted by an update.

**Update sources**

| Source | How it works | Use it for |
| --- | --- | --- |
| Push from laptop | `hostctl update push` cross-compiles hostd on the laptop (`GOOS=linux`, the server's architecture) and uploads the binary | Development: change code, push, test in seconds |
| GitHub releases | hostd checks the repository's latest release (default every 6 hours), downloads the binary for its architecture and verifies the signature | Normal use: tag a release on GitHub, every machine picks it up |
| GitHub branch | hostd follows a branch, pulls new commits and builds with the Go toolchain on the server | Testing unreleased work without making a release |

```toml
# ~/.config/hostd/hostd.toml
[update]
source  = "github-release"     # github-release | github-branch | push-only
repo    = "me/hostd"
channel = "stable"             # stable | prerelease
check   = "6h"
apply   = "auto"               # auto | notify | manual
```

With `apply = "notify"`, hostd only emits `update.available`, and `hostctl update apply` installs it. Pushes from the laptop are always applied immediately, because you asked for them.

**Pushing from the laptop**

```sh
hostctl update push                 # build from the current checkout and install
hostctl update push ./dist/hostd    # install a binary you built yourself
hostctl update push --ssh           # fallback over SSH when the running hostd is broken
```

The normal path uploads through the API (scope `admin`). The `--ssh` path copies the binary with SSH and calls the installer directly, so a broken version that no longer serves the API can still be replaced. Laptop builds are marked as dev builds in `hostctl version`.

**Install and rollback**

1. **Stage.** The new binary goes to `~/.local/lib/hostd/versions/<version>/`. Release downloads are checked against the release's signed checksum file; a bad signature stops here.
2. **Back up.** The state database and config are copied next to the current version.
3. **Wait for quiet.** hostd waits until no deploy or scene is in progress (at most 5 minutes), so an update never cuts an action in half.
4. **Switch.** The `current` symlink is pointed at the new version, the old version is recorded as `previous`, and hostd asks systemd over D-Bus to restart its unit, without waiting for the result. hostd never runs `systemctl --user restart` on itself: that process would live in hostd's own cgroup and be killed halfway through the restart. The API answers the caller before the restart is requested, and clients expect the connection to drop.
5. **Health check.** The new process must report ready to systemd (`sd_notify READY=1`) within 30 seconds, after loading config, modules and state.
6. **Rollback.** If it does not become ready, or crashes three times in a row, systemd starts a tiny separate unit, `hostd-rollback.service`, which points the symlink back to `previous`, clears the failed state and starts hostd again. The rollback is a shell script and never depends on the broken binary.

`OnFailure=` only fires once a unit actually enters the `failed` state, and with `Restart=on-failure` that happens only after the start limit is used up. The unit therefore sets the limits explicitly:

```ini
# ~/.config/systemd/user/hostd.service (excerpt)
[Unit]
OnFailure=hostd-rollback.service
StartLimitIntervalSec=120
StartLimitBurst=3            # three failed starts in 2 minutes → failed → rollback

[Service]
Type=notify
TimeoutStartSec=30           # started but never ready also counts as a failure
Restart=on-failure
RestartSec=2
```

The last 3 versions are kept, and `hostctl update rollback` switches back by hand.

**Why running apps survive an update.** Every app instance runs as its own transient systemd service and every container in Docker or Podman, never as a child process of hostd. When the new version starts, each module re-adopts what is already running: `apps` re-reads its units and containers, `display` re-reads the Sway window tree, `audio` re-reads PipeWire. A game keeps running through the restart; the API is unavailable for a few seconds and clients reconnect to the event stream automatically.

**Config and state migrations.** The config and the state database both carry a schema version. A new hostd migrates them on start, from the backup taken in step 2, so a rollback always returns to data the old version understands.

**External modules** are separate programs, so they are updated like background apps: through the deploy module with a `push`, `remote` or `image` source. hostd restarts only that module, and a module that fails its handshake is rolled back to its previous release.

## Implementation plan

Build a thin slice of everything first, then deepen each area. Milestone 1 already proves the whole loop: CLI → action pipeline → Sway, PipeWire and Docker → events.

**Stack**

| Part | Choice |
| --- | --- |
| Daemon and CLI | Go, two binaries: `hostd` and `hostctl` |
| HTTP and WebSocket | `net/http` (Go 1.22+ routing), `coder/websocket` |
| State | In memory, rebuilt from the system on start; SQLite (`modernc.org/sqlite`) for tokens and the audit trail |
| Sway | Sway IPC over its unix socket (own small client or `joshuarubin/go-sway`) |
| systemd | `coreos/go-systemd` D-Bus for transient and generated user services |
| Service proxy | `net/http/httputil.ReverseProxy` for zero-downtime switches |
| Audio | `wpctl` and `playerctl` subprocesses behind interfaces; native later |
| Containers | Docker Engine API client against a configurable socket |
| Config | TOML (`BurntSushi/toml`), file watching with `fsnotify` |
| Packaging | `.deb` via nfpm; systemd user unit with lingering |

**Repository layout**

```
hostd/
  cmd/hostd/              daemon: core + built-in modules
  cmd/hostctl/            CLI, generated from manifests
  core/                   module registry, action pipeline, event bus, API, tokens
  sdk/                    module contract (Go) and JSON-RPC protocol spec
  modules/
    apps/                 catalog and instances
      runners/            exec (also flatpak, url, handoff), docker, compose, process
      sources/            desktop (incl. Flatpak exports), toml
    display/              module + backends/sway
    audio/                module + backends/wireplumber, mpris
    deploy/
    automation/
  deploy/files/menu/      the on-screen menu (an API client, not a module)
  examples/python-module/ an external module written in Python
  docs/
```

**Milestones**

The step-by-step build and test plan for these milestones is in [plan.md](plan.md).

### M1 · Thin slice of everything

- [x] Daemon as a lingering systemd user service; connects to Sway when the session appears
- [x] Module registry and contract; the complete action pipeline: per-resource queues with reentrant child keys, priority holds, versions, cause chain and loop limits, audit trail, events
- [x] Tokens with scopes; self-update pushed from the laptop with automatic rollback; `hostctl login`
- [x] Catalog from desktop files plus TOML files
- [x] `exec` and `docker` runners; instance state machine
- [x] Workspace-per-instance, fullscreen default, cgroup window matching
- [x] Master volume and mute
- [x] Gate: from the laptop, start Firefox, set volume and start a container, and see all three in `hostctl events`

M1 completed 2026-10-08: the gate passes on the real machine.

### M2 · Display depth

- [x] Presence detection and background launches with on-screen notice
- [x] On-screen menu on keyboard, mouse and controller
- [x] Flatpak discovery; handoff for launchers such as Steam (no built-in Steam support); match rules; gamescope wrap; web apps
- [x] `window.place`, display power and mode
- [ ] Gate: a remote launch during a game opens in the background, and the controller's Guide button switches to it

### M3 · Audio depth

- [ ] Stable output names, output switching including Bluetooth
- [ ] Per-app volume and mute; MPRIS media controls
- [ ] Gate: a game and a browser have different volumes, and audio moves to a headset on command

### M4 · Services

- [ ] `compose` and `process` runners
- [ ] git push deploys with health checks, the switching proxy and rollback; remote, image and webhook sources; deploy keys and secrets; config following a repo
- [ ] Gate: `git push box main` updates a site with no downtime, and a broken build rolls back

### M5 · Automation

- [ ] Scenes with diff-and-apply
- [ ] Rules and the script runner with scoped tokens, on top of the holds, loop limits and audit trail built in M1
- [ ] Gate: a rule and a manual change on the same volume resolve as specified

### M6 · Release 0.1

- [ ] OpenAPI generation, docs site, and an example external module in Python
- [ ] `.deb` package, install script, signed GitHub releases, the release and branch update channels, and migrations restored from the update backup; end-to-end tests in a VM with a virtual Sway output (headless backend)

## Risks and open questions

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Sway on the proprietary NVIDIA driver (GTX 1050 Ti, driver 550) is unsupported upstream (`--unsupported-gpu`) | Glitches in fullscreen games or after driver updates | Driver from Debian stable only; test top titles early in M2; `gamescope` wrap; Ryzen iGPU as fallback |
| Games misbehave under a tiling compositor (focus, resolution, fullscreen) | Games start but look or feel wrong | Per-app `wrap = "gamescope"`; test the top games early (as app files with a handoff) |
| Window matching fails for apps launched through other launchers | Instances show no window; focus and close fail | cgroup first, match rules second, unowned windows still listed; matching reported in `hostctl ps` |
| PipeWire node IDs and device names change | Wrong output selected | Stable names from device properties; tests with Bluetooth reconnects |
| Services and the display share the `screen` user | A compromised service could affect the screen session | Rootless containers by default; a separate service user is a later option |
| Docker group equals root | Privilege escalation if the default Docker socket is used | Default to rootless Docker or Podman; document the trade-off |
| Too many sources of commands confuse users | "Why did my volume change?" | Audit trail with source on every action; `hostctl log` |
| The systemd user manager lacks the graphical session's environment | Apps start but cannot open a window | hostd passes the session variables explicitly into each instance's unit; covered by an integration test |

**Open questions**

- [x] Should background services move to their own Linux user before 0.1, or after? **After.** Rootless containers are the mitigation for 0.1.
- [x] Is a phone web page needed before 0.1 after all, or is the API enough for the first release? **The API is enough.** The event stream already accepts browser-style token auth for a later page.
- [ ] Which games, emulators and services should be in the M2 and M4 test lists?
