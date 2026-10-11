# hostd

hostd turns a Linux mini PC into a controllable screen and home server. It runs apps on the screen and services in the background, and gives phones, scripts and the on-screen menu one way to control them.

- **Apps on the screen**: desktop apps, Flatpak apps, web pages, containers. One app is in front at a time, fullscreen. An on-screen menu opens with the Super key or by holding a game controller's Guide button.
- **Services in the background**: processes and containers with health checks. They update by `git push`, from a git remote, by webhook, or from a container registry, with no downtime and with rollback.
- **Audio**: volume, outputs (HDMI, speakers, Bluetooth), each app's own volume, media keys.
- **Automation**: scenes (a state to reach), rules (when to reach it) and scripts.
- **One API** (`/v1`, HTTP and a WebSocket for events) and the `hostctl` CLI. Modules in other languages use a small JSON-RPC protocol.

## How it works

Every change is an **action**: `audio.volume.set {"percent": 40}`, `app.start {"id": "jellyfin"}`. Every action goes through the same pipeline, whether it comes from the CLI, a phone, a key on the keyboard or a rule:

1. **Validated** against the action's schema, and checked against the current state when its turn comes.
2. **Authorized** by the token's scopes.
3. **Ordered** per resource: actions on the same volume run one at a time.
4. **Held by priority**: what a person just changed is not undone by automation for 3 minutes (`local` input beats `manual` beats `automation`). A held action is skipped, not failed.
5. **Audited**: `hostctl log` shows who changed what, when, and why.

Modules own their namespaces (`audio.*`, `app.*`, `deploy.*`) and never touch each other's resources: cross-module work is an action too.

## Where to go next

- [Install](install.md) a machine.
- [Apps](apps.md): app files, runners, windows.
- [Services and deploys](services.md).
- [Automation](automation.md): scenes, rules and scripts.
- [Configuration](config.md): `hostd.toml`.
- [The API](api.md) and [the CLI](cli.md).
- [Writing a module](modules.md), in any language.
