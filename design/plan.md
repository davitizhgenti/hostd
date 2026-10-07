# hostd: Build and Test Plan

Companion to [hostd Display and Services Daemon Design.md](hostd%20Display%20and%20Services%20Daemon%20Design.md). The design says *what* hostd is; this file says *in what order* to build it and *how each piece is tested*. Milestones M1–M6 match the design; this plan breaks each into ordered steps with a definition of done.

Status legend: `[ ]` not started · `[~]` in progress · `[x]` done

---

## 0. Decisions

These affect the core's registration rules or the M1 scope. Resolutions below came out of the plan review; §0.1 lists the design-doc edits they require.

| # | Issue | Resolution |
| --- | --- | --- |
| D1 | **Namespace ownership.** Rule 1 says a module owns its namespace, but the design splits `instance.*` between `apps` (start/stop) and `display` (focus/close/fullscreen/place), and `deploy` emits `service.*`. | **Rename so no namespace is split.** `apps` owns `app.*`, `instance.*`. `display` owns `display.*`, `window.*`; its instance actions become `window.focus <instance>`, `window.close`, `window.fullscreen`, `window.place`. `audio` owns `audio.*`, `media.*`. `deploy` owns `deploy.*` (event `deploy.done` replaces `service.deployed`). `automation` owns `scene.*`, `rule.*`. Manifests still declare `owns`, and the core enforces it: no overlap between modules, no action or emit outside the declared set. |
| D2 | **Scopes.** Design says "one scope per module", but the scope table has `read`, `admin`, `scenes`, `services` and the sub-scope `display.front`. | Core scopes: `read`, `admin`. Every other scope is declared in a manifest; each action lists the scope it needs (e.g. `app.start` → `apps`, plus `display.front` when `front=true`). |
| D3 | **Pipeline features in M1 vs M5.** Priority holds, versions and loop limits are core logic but listed under M5. | Build the **complete pipeline in M1** (queues, reentrant keys, holds, versions, cause chain, loop limits, audit). M5 only adds the rule engine, scenes and scripts on top. |
| D4 | **Self-update in M1.** Full rollback is heavy for a thin slice. | M1: `hostctl update push` + symlink switch + a correctly configured `OnFailure=` rollback (§3.11–3.12). M6: signed GitHub releases, branch builds, crash-loop handling and migrations-from-backup. |
| D5 | **Resource key declaration for external modules.** Go modules can use a function `args → keys`; JSON-RPC modules cannot. | Manifests declare keys as templates (`"instance:{id}"`, `"audio.stream:{instance}"`). Built-ins use the same templates. Keys are **reentrant for synchronous child actions** (§3.3). |
| D6 | **Overlay UI** (M2). | **No layer-shell.** The overlay is a normal fullscreen window that hostd launches as an instance on its own workspace (`hostd:overlay`) and focuses on demand, so the display module manages it like any app. Toolkit: Gio (pure Go, one static binary) unless the spike shows controller navigation is much easier in GTK4. On-screen notices over a fullscreen game go through the session's notification daemon (`mako`, via `org.freedesktop.Notifications` on D-Bus), so hostd itself has no Wayland protocol dependency. |
| D7 | Open questions from the design. | No separate service user before 0.1. No phone page before 0.1; the API is enough. Test lists still need filling in (§12). |

### 0.1 Design-doc edits (applied 2026-10-05)

All applied to the design doc:

- [x] Rename display actions to `window.*` and the deploy event to `deploy.*` (D1); update the endpoint table and CLI examples.
- [x] Window apps run as **transient services** (`hostd-<instance>.service`, `Type=exec`), not scopes; cgroup matching looks for `.service` (§3.8).
- [x] Add `Validate` to the `Module` interface (§3.1) and a `parent` action field for synchronous `core.Do` children (§3.3).
- [x] Reentrant resource keys for child actions (§3.3).
- [x] Rollback unit settings: `StartLimitBurst`, `TimeoutStartSec`, restart over D-Bus (§3.11–3.12).
- [x] Overlay as a regular window instance; notices via the notification daemon (D6).
- [x] Zero-downtime deploys use a built-in reverse proxy (§6).
- [x] Secrets: 0600 file in v1 and what that protects against (§6).
- [x] WebSocket token via subprotocol (§3.5).

---

## 1. Environment setup

Current dev machine (checked 2026-10-04): x86_64, Debian 13, systemd 257, git 2.47, Python 3.13, `wpctl` present. **Missing:** Go, sway, swaymsg, playerctl, docker/podman, nfpm, qemu.

### 1.1 Dev machine
- [x] Install Go (≥ 1.22, use current stable) → `go version`. Go 1.27.1 installed in `~/.local/go` (checksum verified); add `~/.local/go/bin` to `PATH`.
- [x] Install `podman` (rootless): 5.4.2, cgroups v2 and enable the user socket: `systemctl --user enable --now podman.socket`
- [x] `golangci-lint` and `govulncheck`: pinned in `tools/go.mod`, run through `go tool` by the Makefile, so nothing to install
- [ ] `nfpm` (needed in M6)
- [ ] Optional for local manual testing: `sway`, `foot`, `grim`, `playerctl`, `pipewire-pulse`
- [ ] Install `qemu-system-x86` + `cloud-image-utils` (needed from M1.9 for VM tests; required in M6)

Integration tests run **in containers**, so the dev desktop session is never touched.

### 1.2 Target machine (mini PC)
- [x] Debian 13, user `screen` (no sudo), member of `input`, `video`, `render`. Machine `core` (192.168.18.33): Ryzen 5 2400G, GTX 1050 Ti (NVIDIA 550 driver), Epson projector on HDMI-A-1 at 1280×800.
- [x] greetd autologin → Sway session; PipeWire + WirePlumber user services. **greetd needs `vt = 7`**: Debian runs agetty on tty1, and with `vt = 1` the session died silently.
- [x] `loginctl enable-linger screen`
- [x] Rootless Podman (or rootless Docker) socket for `screen` (Podman 5.4.2; socket enabled by the installer)
- [x] SSH key from the laptop in `screen`'s `authorized_keys` (aliases `core` and `core-screen`)
- [x] Install `grim` (screenshots for checks)
- [x] GPU: **proprietary NVIDIA 550** (Debian `nvidia-driver`, contrib/non-free). `nvidia-drm modeset=1 fbdev=1`; greetd runs `env WLR_NO_HARDWARE_CURSORS=1 sway --unsupported-gpu`. Reclocking works (139–1987 MHz), unlike nouveau.
- [x] mako `layer=overlay` (`~/.config/mako/config`): the default `top` layer is hidden under fullscreen windows, so notices would not show over games.

### 1.2.1 Installer

- [x] `deploy/install.sh`: everything in §1.2, done by hand on `core`, as one re-runnable script for a fresh Debian 13 machine (NVIDIA driver when present, packages, `screen` user, SSH keys, lingering, Podman socket, Sway and mako config, greetd on VT 7). Installing hostd itself is added once M1 has a release.
- [x] `make test-install`: runs it twice on a fresh Debian 13 container; the second run must change nothing, then 15 checks on the result. Not covered there: the NVIDIA driver, greetd on a real console, reboot. Those are covered by `core` and later the VM (§9.4).
- [x] `deploy/check.sh` checks a running machine (17 checks); `make devbox-check` and `make check-server` run the same script.
- [x] Run the installer on `core`: it filled the gaps from the manual setup (`xwayland`, `libnotify-bin`, `curl`, Podman socket), a second run changed nothing, and after a clean reboot `make check-server` passes 17/17 (2026-10-07).

- [x] The installer also installs **hostd** (2026-10-07): the binaries come from the rolling `edge` release that CI publishes after the tests pass on `main` (checked against `SHA256SUMS`; `--from DIR` installs a local build), side by side in `~screen/.local/lib/hostd/versions/<version>` with a `current` symlink; `hostd.service` is a user unit (`Type=notify`: hostd reports ready over `sd_notify` once its API listens) enabled at boot; `hostctl` goes to `/usr/local/bin`, and the screen user's hostctl is logged in with the first admin token. Nothing is set up on other computers: they use the API with their own token. `make test-install` checks all of it.

### 1.3 devbox: the mini PC in a container (until hardware exists)

- [x] `test/devbox/`: one Podman container with systemd as PID 1, the `screen` user with lingering, headless Sway (VNC on `127.0.0.1:5900`), PipeWire with fake sinks, rootless Podman and mako. `make devbox`, `make devbox-check` (16 checks, all passing 2026-10-05), `make devbox-shell`. See [test/devbox/README.md](../test/devbox/README.md).
- Each milestone's manual gate runs here first, then on the real machine. It covers M1, M4 and M5 fully and M2/M3 partly (no controllers, games, HDMI or Bluetooth).
- It is also the starting point for the `hostd-all` integration image (§9.3).

---

## 2. Phase 0: scaffolding (≈1–2 days)

- [x] `git init`, `LICENSE` (Apache-2.0), `README.md`, `.gitignore`
- [x] `go.mod`: `module github.com/davitizhgenti/hostd`; remote `git@github.com:davitizhgenti/hostd.git`
- [~] Repository layout (packages are created as each step needs them; so far `cmd/`, `core/`, `sdk/`, `internal/`, `tools/`):

```
hostd/
  cmd/hostd/              daemon entry point
  cmd/hostctl/            CLI entry point
  core/                   registry, pipeline, queues, holds, bus, audit, api, tokens, store
  sdk/                    module contract types + JSON-RPC protocol
  internal/clock/         real + fake clock
  internal/ids/           ULID-based act_/evt_/tok_ IDs
  internal/testutil/      shared test helpers, fake modules
  modules/
    apps/  runners/{exec,flatpak,steam,url,docker,compose,process}  sources/{desktop,flatpak,steam,toml}
    display/  backends/sway/
    audio/    backends/{wireplumber,mpris}/
    deploy/
    automation/
  clients/overlay/
  examples/python-module/
  test/
    integration/          container harnesses (Containerfiles + Go tests, tag `integration`)
    e2e/                  VM image build + gate scripts (tag `e2e`)
    testdata/             shared fixtures (desktop files, vdf, pw-dump, sway trees)
  systemd/                unit files
  packaging/              nfpm config, install script
  docs/
```

- [x] `Makefile` (or `justfile`) targets:

| Target | Does |
| --- | --- |
| `build` | `CGO_ENABLED=0 go build ./cmd/...` |
| `test` | `go test -race -shuffle=on ./...` |
| `test-integration` | builds test containers, `go test -tags integration ./test/integration/...` |
| `test-e2e` | boots the VM, runs gate scripts |
| `fuzz` | runs each fuzz target for `FUZZTIME` (default 30s) |
| `lint` | `golangci-lint run`, `govulncheck ./...` |
| `cover` | coverage report, fails if `core/` < 85 % |

- [x] CI (GitHub Actions): `.github/workflows/ci.yml` runs lint, test, build, cover on every push; integration and nightly jobs are added when those tests exist
  - `lint` + `test` on every push
  - `test-integration` on every PR (Ubuntu runner with podman)
  - nightly: `fuzz` (5 min per target) + `test-e2e`
- [x] `internal/clock`: `Clock` interface (`Now`, `After`, `NewTimer`, `NewTicker`) with a fake you can `Advance(d)`. **Every time-dependent component takes a `Clock`**: holds, idle detection, cooldowns, polling, timeouts, audit timestamps.
- [x] `internal/ids`: monotonic ULIDs with prefixes.

**Done when:** `make build test lint` pass on an empty skeleton in CI. *Passing on GitHub Actions since the first push (2026-10-06).*

---

## 3. M1: thin slice of everything

Goal from the design: *from the laptop, start Firefox, set volume and start a container, and see all three in `hostctl events`.*

Build in the order below; each step has its own tests and does not depend on later steps.

### 3.1 SDK: the module contract (`sdk/`)

- [x] Types: `Module`, `Core`, `Manifest`, `ActionSpec`, `EventSpec`, `Action`, `Result`, `Event`, `Source`, `Error`
- [x] `Module` gains `Validate(ctx, Action) error` (not in the design's interface): called by the core when the action reaches the front of its queue, while its keys are held, to re-check targets. Kept separate from `Handle` so the core can report `not_found` / `instance_not_running` uniformly and the audit can tell "refused" from "failed".
- [x] `Action` gains `parent` (action ID) for synchronous `core.Do` children, alongside `cause` (event ID) for event-triggered actions
- [x] `Manifest` fields: `name`, `version`, `owns` (action/event prefixes, D1), `requires`, `scopes`, `actions[]` → `{type, schema (JSON Schema), keys (templates, D5), scope, timeout, route (method + path template for the friendly API)}`, `events[]`
- [x] `Source.Kind`: `local | manual | automation | external`, plus `Priority()` (3/2/1/none)
- [x] `Result.Status`: `applied | skipped | observed | accepted` (202 for slow actions)
- [x] Stable error codes as constants: `invalid_args`, `not_found`, `instance_not_running`, `precondition_failed`, `forbidden`, `timeout`, `module_unavailable` (+ internal `conflict` for registration errors)
- [x] JSON Schema validation: pick `santhosh-tekuri/jsonschema` (draft 2020-12)

**Tests**
- JSON round-trip of every type (golden files).
- Schema validation: unknown fields, wrong types, ranges (volume 0–150).
- Key template expansion: `"instance:{id}"` with and without the arg; missing arg → `invalid_args`.

*Done 2026-10-07: `sdk/` (96.5% coverage). Beyond the list above: `Core.Subscribe` takes a ctx; codes `loop_detected` and `internal`; `Event.Resource` for observed changes; `Source.Module`; `arg_scopes`; `Manifest.Validate` reports every problem at once; object schemas reject unknown arguments unless they say otherwise; `$ref` to files or URLs is refused.*

### 3.2 Core: registry and event bus

- [x] Registry: register modules, topological start order by `requires`, reverse order on stop
- [x] Refuse at startup: missing dependency, dependency cycle, overlapping `owns`, action type outside `owns`
- [x] Event bus: `Subscribe(filter)` with glob filters (`instance.*`, `*`), bounded per-subscriber buffer; a subscriber that falls behind is dropped and receives a final `bus.lagged` event (publishers never block)
- [x] `Emit` from a module checks the event type is in its `owns`; violations are logged and dropped

**Tests** (fake modules in `internal/testutil`)
- Start order for a diamond dependency graph; reverse stop order.
- Missing dependency / cycle / overlap each produce a specific startup error.
- Glob matching table.
- Fan-out to N subscribers; a blocked subscriber does not delay others (`goleak` + timing with fake clock).
- A module emitting outside its namespace is rejected.

*Done 2026-10-07: `core/registry.go`, `core/bus.go` (97.6% coverage, goleak on every core test). The core reserves `action.*`, `bus.*` and `core.*` for its own events. A module whose `Start` fails stops the ones already started, so nothing is left half-started. Emit rights are per declared event type, not just per namespace.*

### 3.3 Core: the action pipeline

Stages: **validate → authorize → prioritize → queue → execute → publish**.

- [x] **Validate (early):** action type known, args match schema → `invalid_args`
- [x] **Authorize:** token scopes include the action's scope (and `display.front` when relevant) → `forbidden`
- [x] **Resource queues:** one FIFO per key, created lazily, garbage-collected when idle. Multi-key actions acquire keys in sorted order.
- [x] **Reentrant keys for child actions:** a module's `Handle` holds its keys while calling `core.Do`; if the child needs one of them, both wait forever. Example: `app.start firefox` with `if_running = "restart"` holds `instance:firefox` and calls `instance.stop firefox`, which needs the same key. Fix: the `ctx` passed to `Handle` carries the set of keys held by the action and its ancestors; a `core.Do` child made with that `ctx` (it gets `parent` set) skips keys already held in its chain and queues only for the rest. Reentrancy follows `parent` only, never `cause`: event-triggered actions are asynchronous and queue normally.
- [x] **Validate (late):** when the action reaches the front of its queue, the core calls the module's `Validate` (§3.1) → `not_found` / `instance_not_running`
- [x] **Versions:** a counter per resource key, bumped on each applied or observed change; `expect_version` mismatch → `precondition_failed` with current state
- [x] **Holds:** per key, `{priority, until}`. Action priority < hold → `skipped` with `reason: held`, `held_by`, `until`. ≥ hold → applies and renews at its own priority. Hold window default 3 min, configurable per key prefix. `external` changes never create a hold.
- [x] **Priority from chain origin:** an action with `cause` inherits the priority of the chain's root action
- [x] **Execute:** call `Module.Handle` with a context deadline from the action spec; on deadline → `timeout`, and the late result is published as an event
- [x] **module_unavailable:** module registered but not started/attached
- [x] **Cause chain:** every action carries `cause`, every event carries the causing action ID. Depth > 5 rule-triggered steps → refused + `loop_detected`. Per-rule rate limit (> 10/min → paused 10 min) is a core service the automation module calls.
- [x] **Publish:** `action.done` / `action.skipped` events with action ID, plus the module's own events
- [x] `Core.Do` for modules goes through the same pipeline, with the calling module recorded in `source`

**Tests**
- Ordering: 1,000 goroutines send actions on one key; the applied order equals the enqueue order.
- Parallelism: two keys with blocking fake handlers run concurrently (detected with barriers, not sleeps).
- Deadlock freedom: random multi-key actions over 5 keys, 10k iterations, `-race`, must finish under a timeout.
- Nested actions: a parent holding `k` calls `core.Do` for a child needing `k` → completes; a child needing `k` plus `j` waits only for `j`; a grandchild reenters keys held by its grandparent; an unrelated action on `k` from another sender waits until the parent finishes; an event-triggered action (with `cause`, no `parent`) on `k` does **not** reenter.
- Late validation: `window.close x` queued behind `instance.stop x` (both on `instance:x`) returns `instance_not_running`.
- Versions: fresh vs stale `expect_version`; external observation bumps the version.
- Holds (table test on fake clock): every pair of priorities × inside/outside window; renew; expiry; external never holds; per-key window override.
- Chain priority: a manual scene's sub-actions run at `manual`; the same from a rule at `automation`.
- Loop: synthetic A→B→A chain stops at depth 5 with `loop_detected`.
- Timeout: handler sleeping past its deadline → `timeout`, then a later event with the real result.
- **Property test** (`pgregory.net/rapid`): random sequences of actions from random sources over random keys, compared to a simple reference model (last-applied value + hold state) after every step.

*Done 2026-10-07: `core/engine.go`, `locks.go`, `auth.go`, `audit.go`, `limits.go` (97.1% coverage; 2,000-case property test against a reference model of holds and versions). Decided while building: holds and `expect_version` are checked after queueing, not before, so an action queued behind a manual change is skipped once that change applies; `expect_version` needs an action on exactly one key; child actions inherit the caller's authorization, and a module's own actions are trusted; a timed-out handler keeps its keys until it returns and is cancelled after a second timeout; core events `action.failed` and `action.loop_detected` added. The audit trail is in memory until 3.4 (`MemoryAudit` behind the `Auditor` interface).*

### 3.4 Core: storage, tokens and audit

- [x] SQLite via `modernc.org/sqlite` at `~/.local/state/hostd/state.db` (XDG state directory); WAL mode
- [x] Migrations with a `schema_version` table from day one (needed by update rollback)
- [x] Tokens: 32 random bytes, shown once, stored as SHA-256 hash; fields `id, name, scopes, created, expires, revoked`
- [x] First start with an empty token table prints one `admin` token to the journal and to `$XDG_RUNTIME_DIR/hostd-admin-token` (mode 0600, deleted after first use)
- [x] Short-lived tokens (for scripts, M5) supported now: `expires` field + scope subset check
- [x] Audit trail: every action (incl. skipped and observed) → `time, type, args, source, cause, status, reason, version, duration`; capped at 10,000 rows (delete oldest in batches)

**Tests**
- Migrations apply from empty and from every earlier version (fixtures added as versions appear).
- Token create / verify / revoke / expiry; hash never equals the plaintext; scope subset rules.
- Audit: trimming at 10,000; ordering; skipped entries carry `held_by`/`until`.

*Done 2026-10-07: `core/store` (85.1% coverage; pure-Go SQLite keeps the binary static). Decided while building: tokens have a kind (local, manual, automation) that becomes the action source kind; secrets start with `hostd_`; revoked names can be reused; `EnsureAdminToken` mints only when the token table is empty, so a restart never creates a second admin token; audit writes are batched off the action path and dropped (with a log line) only if the writer falls 1,024 entries behind; a database from a newer hostd is refused with a hint to restore the backup. Writing the first admin token to `$XDG_RUNTIME_DIR` is the daemon's job (3.11).*

### 3.5 Core: HTTP API and WebSocket

- [x] `net/http` with Go 1.22 patterns. Middleware: request ID, token auth, source-address check, JSON error shape, panic recovery
- [x] Listeners: unix socket `$XDG_RUNTIME_DIR/hostd.sock` (mode 0600) and TCP `:7300` (configurable bind address)
- [x] Source check on TCP: unwrap IPv4-mapped IPv6 addresses first (`netip.Addr.Unmap`, since a dual-stack socket reports IPv4 clients as `::ffff:a.b.c.d`), then allow loopback, RFC 1918, link-local, ULA `fc00::/7`; reject everything else with 403
- [x] Generic routes: `POST /v1/actions`, `GET /v1/actions` (audit), `GET /v1/state/{module}`, `GET /v1/manifests`, `POST/DELETE /v1/tokens`, `GET /v1/events` (WebSocket, `?type=` glob filter)
- [x] WebSocket auth: accept the `Authorization` header (CLI) **and** the token as a WebSocket subprotocol (`Sec-WebSocket-Protocol: hostd.token.<token>`), because browsers cannot set headers on WebSocket connections and a future phone page will need it. Prefer the subprotocol over a query parameter so tokens don't end up in access logs.
- [x] **Friendly routes generated from manifests** (`route` field in `ActionSpec`): path params merged into args
- [x] `202 Accepted` with action ID for actions whose spec marks them slow (builds, deploys)
- [x] Event stream: on reconnect, clients may pass `?since=<evt_id>`; the server replays from a small ring buffer (e.g. last 1,000 events)
- [ ] *(moved to M6)* Optional HTTPS with a self-signed certificate (fingerprint printed for `hostctl` pinning). Can slip to M6.

**Tests** (`httptest`)
- Every error code yields the documented status and JSON shape.
- Scope matrix: each route × each scope → allowed / `forbidden`.
- Source check table: 127.0.0.1, ::1, 10.x, 172.16.x, 192.168.x, fd00::1, `::ffff:192.168.1.5` allowed; 8.8.8.8, `::ffff:8.8.8.8`, 2001:db8::1 refused.
- WebSocket auth: header, subprotocol, missing, revoked; the server echoes only the accepted subprotocol name, never the token.
- Generated routes for a fake module appear without any core change.
- WebSocket: filter works; `since` replay; a client that disconnects and reconnects misses nothing in the buffer window.

*Done 2026-10-07: `core/api` (87.3% coverage; three planted bugs, in the network check, the scope check and the replay, were each caught by the tests). Also: `GET /v1/version` (with OS and architecture, for `hostctl update push`), `GET /v1/tokens`, `bus.gap` when `since` is older than the replay window, the `unauthorized` code, `sdk.StateReporter` for `GET /v1/state/{module}`, conflicting manifest routes refused at startup, the admin token file deleted on the admin token's first use. Friendly routes take path parameters as strings.*

### 3.6 CLI: `hostctl`

- [x] Config: `~/.config/hostctl/config.toml` (`url`, `token`, optional `fingerprint`), overridden by `HOSTD_URL` / `HOSTD_TOKEN`; supports `unix:///path` and `http://host:7300`
- [x] `hostctl login <url>` stores URL + token
- [x] Command tree built at runtime from `GET /v1/manifests` (cached on disk with the daemon version; refreshed on mismatch). `hostctl <module> <action...> [args]`
- [~] Hand-written commands: `login`, `version`, `token create/list/revoke`, `log`, `events`, `action` done; `apps`, `start`, `stop`, `ps`, `focus` come with their modules (3.7–3.9), `update push` with 3.12
- [x] Output: human tables by default, `--json` everywhere; exit codes map to error codes (documented)

**Tests**
- `testscript` (`github.com/rogpeppe/go-internal/testscript`): `.txtar` scenarios run `hostctl` against an in-process daemon with fake modules. They double as CLI documentation.
- Exit-code mapping table.
- A new fake module's actions appear as CLI commands with no CLI change.

*Done 2026-10-07: `cmd/hostctl`, `internal/client`, and three testscript files that run real `hostd` and `hostctl` processes against each other. Decided while building:*
- *Module commands are built from `GET /v1/manifests` on each run, not from a disk cache: one small request on the home network, and never stale. Action `a.b.c` becomes `hostctl a b c`; required arguments go in order, every argument also has a `--flag`, and values are converted using the schema.*
- *Exit codes: 0 ok (skipped counts as ok), 1 other errors, 2 usage, 3 unauthorized/forbidden, 4 not_found, 5 instance_not_running, 6 precondition_failed, 7 timeout, 8 module_unavailable, 9 loop_detected, 10 invalid_args (also when hostctl itself rejects a value).*
- *`hostctl events --recent` replays the server's buffered events, and `--count N` exits after N: scripts can wait for an event without a race.*
- *Any valid token may read `/v1/version` and `/v1/manifests`, so script tokens without `read` can use module commands.*
- *Brought forward from 3.11: a minimal `hostd` (`internal/daemon`) that opens the database in `~/.local/state/hostd/`, creates the first admin token, and serves the API; and `hostd --demo`, a pretend lamp module for trying hostctl before the real modules exist. Tried in the devbox: hostctl on the laptop over TCP 7300 works.*

### 3.7 Module `apps`: catalog

- [x] `Source` interface: `Name()`, `Scan(ctx) ([]App, error)`, `WatchPaths() []string`
- [x] `desktop` source: `/usr/share/applications`, `~/.local/share/applications`; parse `Name`, `Exec` (strip field codes `%u %F …`), `TryExec`, `NoDisplay`, `Hidden`, `OnlyShowIn/NotShowIn`, `Icon`; ID = file name without `.desktop`
- [x] `toml` source: `~/.config/hostd/apps/*.toml`; fields from the design (`id`, `extends`, `name`, `runner`, `surface`, `window`, `instance`, `audio`, `requires`, `hidden`, `match`, `restart`, `env`, `health`, `source`)
- [x] Merge: discovered → `extends` overrides → standalone file apps → default ignore list → `hidden`
- [x] Defaults: `window` apps `single`/`focus`; `background` apps `single`/`focus` (= no-op if running)
- [x] Rescan on start, on fsnotify (debounced 500 ms), and on `apps.rescan`; emit `app.catalog.changed`
- [x] Invalid files are reported (`app.file.rejected` event, once per problem, + log) and skipped; they never break the rest of the catalog

**Tests**
- Fixtures in `test/testdata/applications/`: Firefox, Steam, a settings panel (ignored), a terminal (ignored), a file with field codes, a malformed file, a localized-name file.
- Golden test of the merged catalog.
- `extends` to a missing ID → clear error; alias `id` collisions → clear error.
- Fuzz targets: desktop-file parser, TOML app loader.
- Watcher test: write a new file into a temp dir → catalog event within the debounce window (fake clock).

*Done 2026-10-07: `modules/apps` (89.2% coverage; three fuzz targets ran clean), wired into `hostd`; `hostctl apps [--all] [id]`. Decided while building:*
- *Modules can declare read routes (`Reads` in the manifest, `sdk.Reader`), so `GET /v1/apps` and `/v1/apps/{id}` come from the apps module like action routes do.*
- *IDs are lowercase. Reverse-DNS desktop files get their last part (`org.kde.kcalc` → `kcalc`) with the full name as an alias; files in subdirectories get `subdir-name`. A file that renames an app (`id` + `extends`) keeps the old ID as an alias.*
- *A file without `id` or `extends` takes its file name as the ID (`apps/jellyfin.toml` → `jellyfin`). Defining a discovered app's ID without `extends` is an error that suggests `extends`. Of two files with the same ID, the first by name wins and the other is reported.*
- *Hidden by default: `NoDisplay`, `Terminal=true`, `OnlyShowIn`/`NotShowIn` for sway, and the categories Settings, System, TerminalEmulator, ConsoleOnly, PackageManager, Monitor. Hidden apps can still be started by ID.*
- *Exec arguments containing a field code (`--file=%f`) are dropped whole.*
- *Defaults: window apps `fullscreen = true`, `restart = "never"`; background apps `restart = "on-failure"`; both `single`/`focus`.*

### 3.8 Module `apps`: instances and runners

- [ ] `Runner` interface (design): `Start`, `Stop`, `Status`, `Logs`, `Events`, plus `Adopt(ctx) ([]Instance, error)`
- [ ] Instance state machine: `starting → running → stopping → exited`; `starting|running → failed`. Emits `instance.starting/started/exited/failed`.
- [ ] IDs: `<app>` for single instances, `<app>#<n>` for extra copies; `hostctl focus firefox` resolves when exactly one instance runs
- [ ] `policy`/`if_running`: `focus` → `core.Do(window.focus)`; `new` → new instance; `restart` → `core.Do(instance.stop)` (reenters `instance:<id>`, §3.3), then start
- [ ] **`exec` runner:** launches each instance as a transient systemd **service** `hostd-<instance>.service` (`Type=exec`, what `systemd-run --user` does) via `coreos/go-systemd/v22/dbus` `StartTransientUnit`. Not a scope: a scope can only adopt a process that already exists, so hostd would have to fork the app, the app would be hostd's child, and after a hostd restart nobody could collect its exit status. With a service, systemd starts the process, owns its lifetime and records `ExecMainStatus`.
  - The unit's `Environment=` is set explicitly: `WAYLAND_DISPLAY`, `XDG_RUNTIME_DIR`, `XDG_SESSION_TYPE`, `DBUS_SESSION_BUS_ADDRESS`, `SWAYSOCK`, plus the app's `[env]`, because the user manager's environment may not have the session variables.
  - `instance.stop` → `StopUnit`; exit status and code read from unit properties (`ExecMainStatus`, `Result`); units set `CollectMode=inactive-or-failed` so they don't pile up.
- [ ] **`docker` runner:** Docker Engine API over a configurable socket (default `$XDG_RUNTIME_DIR/podman/podman.sock`, then `docker.sock`). Labels `hostd.instance`, `hostd.app`. Pulls the image if missing. Mirrors `/events` into instance events. `restart` policy mapped to the container restart policy.
- [ ] **Adoption on start:** list `hostd-*` units and labelled containers, rebuild instances, emit nothing new for already-running ones

**Tests**
- State machine: table of every (state, event) → next state or error.
- `if_running` matrix: policy × if_running × running/not running.
- Unit tests with `fakeSystemd` and `fakeDocker` (interfaces wrapping the D-Bus and HTTP clients).
- **Contract suite** `runnertest.Run(t, newRunner)`: start, status, stop, exit detection, adoption, logs. Runs against fakes always and real backends under `-tags integration`.
- Integration:
  - `docker`: rootless Podman socket in CI; start `nginx:alpine`, assert running, stop, assert exited.
  - `exec`: systemd-as-PID-1 container (`podman run --systemd=always`) with a user manager, start `sleep 300` as a transient service, kill hostd, restart hostd, assert the instance is adopted with the same ID; then make the process `exit 3` and assert hostd reports exit code 3 (proves exit status survives a hostd restart).
  - `exec` environment: a test app that prints its env shows the session variables set.

### 3.9 Module `display`: Sway backend

- [ ] `DisplayBackend` interface: `Windows()`, `Focus(ws)`, `MoveToWorkspace(win, ws)`, `Fullscreen(win, bool)`, `Close(win)`, `Subscribe()`; module logic is backend-agnostic
- [ ] Sway IPC client (own, small): framing (`i3-ipc` magic + length + type), `RUN_COMMAND`, `GET_TREE`, `GET_WORKSPACES`, `GET_OUTPUTS`, `SUBSCRIBE` (`window`, `workspace`, `output`, `shutdown`)
- [ ] Session attach: watch `$XDG_RUNTIME_DIR` for `sway-ipc.*.sock` (or `SWAYSOCK`); until attached, `display.*` actions → `module_unavailable`; re-attach after Sway restarts
- [ ] Window → instance matching: PID → `/proc/<pid>/cgroup` → `hostd-<instance>.service` → instance. `/proc` root is injectable for tests. Fallback: `match` rules (M2). Otherwise: unowned.
- [ ] Workspace per instance (`hostd:<instance>`), new windows fullscreen by default (per-app `window.fullscreen` setting)
- [ ] Focus stack: on window close, focus the previously focused instance
- [ ] Actions: `window.focus <instance>` (key `display.focus`), `window.fullscreen`, `window.close` (key `instance:<id>`; polite close via Sway `kill`, then `core.Do(instance.stop)` after a timeout)
- [ ] State: `GET /v1/windows` including unowned windows; `GET /v1/display`

**Tests**
- IPC framing unit tests (partial reads, multiple messages per read).
- Fake IPC server replaying recorded `get_tree` / event JSON (fixtures captured from a real Sway into `test/testdata/sway/`).
- cgroup matching over a fake `/proc` dir: main process, grandchild in the same unit, a process in another unit, a process in no `hostd-*` unit.
- Focus stack table test.
- Integration: **headless Sway container** (`WLR_BACKENDS=headless WLR_LIBINPUT_NO_DEVICES=1 sway`, `swaymsg create_output`), apps = `foot` (or a tiny Wayland test client). Assert: own workspace, fullscreen flag, focus returns on close, an unowned window appears in `/windows`. On failure, save a `grim` screenshot as a CI artifact.

### 3.10 Module `audio`: master volume and mute

- [ ] `AudioBackend` interface: `Master()`, `SetVolume(pct)`, `SetMute(bool)`, `Subscribe()`
- [ ] `wireplumber` backend: `wpctl get-volume|set-volume|set-mute @DEFAULT_AUDIO_SINK@`; mirroring via `pw-dump --monitor` (JSON stream), debounced
- [ ] Actions: `audio.volume.set` (absolute 0–150, or relative `+5`/`-5`, clamped), `audio.mute.set` (`true|false|toggle`)
- [ ] External changes → `audio.volume.changed` with source `external`, status `observed`, version bump, no hold
- [ ] Module waits for PipeWire (the user service may start after hostd); `module_unavailable` until then

**Tests**
- Parsers over captured `wpctl` and `pw-dump` output (fixtures).
- Relative/absolute/clamp table.
- Fake backend: external change produces an observed audit entry and no hold.
- Integration: container with `pipewire` + `wireplumber` + a `support.null-audio-sink` node as the default sink; set volume via the API, verify with `wpctl`; change with `wpctl`, verify the observed event.

### 3.11 Daemon lifecycle

- [ ] `cmd/hostd`: load `~/.config/hostd/hostd.toml` (module list, external modules, listeners, hold windows, idle threshold), open the store, start modules, start listeners, `sd_notify READY=1`
- [ ] Graceful shutdown on SIGTERM: stop accepting actions, drain queues (bounded), stop modules in reverse order. Instances keep running.
- [ ] `systemd/hostd.service` (user unit):

```ini
[Unit]
OnFailure=hostd-rollback.service
StartLimitIntervalSec=120
StartLimitBurst=3            # 3 failed starts in 2 min → unit enters "failed" → OnFailure fires

[Service]
Type=notify
ExecStart=%h/.local/lib/hostd/current/hostd
TimeoutStartSec=30           # a binary that starts but never sends READY=1 counts as failed
Restart=on-failure
RestartSec=2
KillMode=mixed               # instances live in their own units, so stopping hostd never kills them
```

  `OnFailure=` only fires when the unit actually enters the `failed` state. With `Restart=on-failure` that happens only once the start limit is used up, so `StartLimitBurst` and `StartLimitIntervalSec` must be set explicitly.
- [ ] Logs to journald (structured `log/slog` with a journald handler)

**Tests**
- Startup with a config that disables `display`/`audio` (headless) works.
- Invalid config → clear error and non-zero exit, nothing half-started.
- SIGTERM with in-flight actions: they finish or are reported, no goroutine leaks (`goleak`).

### 3.12 Self-update (M1 scope, see D4)

- [ ] `POST /v1/update` (scope `admin`): multipart upload of a binary → `~/.local/lib/hostd/versions/<version>/hostd`
- [ ] Back up `state.db` + config next to the current version
- [ ] Switch the `current` symlink, record the previous version in `~/.local/lib/hostd/previous`, then ask systemd to restart the unit **over D-Bus** (`RestartUnit("hostd.service", "replace")`, without waiting for the job). hostd must not run `systemctl --user restart` on itself: that process lives in hostd's cgroup and gets killed halfway through. The API response is sent before the restart request, and the client expects the connection to drop.
- [ ] Minimal rollback: `hostd-rollback.service` (`Type=oneshot`) runs a shell script, independent of the hostd binary, that points `current` at `previous`, runs `systemctl --user reset-failed hostd`, and starts it again
- [ ] `hostctl update push [path]` cross-compiles (`GOOS=linux GOARCH=<server arch>` from `/v1/version`) when no path is given; `--ssh` fallback copies with `scp` and runs the installer script
- [ ] `hostctl version` shows client and server versions; laptop builds marked `-dev`

**Tests**
- Unit: version directory layout, keep last 3 versions, symlink switch is atomic (`rename` of a temp symlink).
- VM (or systemd container), three cases:
  - push a good binary → new version running
  - push a binary that exits 1 → start limit hit → previous version running within 60 s
  - push a binary that runs but never sends `READY=1` → `TimeoutStartSec` fails it → rolled back
- A running instance survives all three (its unit is not in hostd's cgroup).

### 3.13 M1 gate

- [ ] Automated: `test/e2e/m1_gate.txtar` in the integration environment (headless Sway + PipeWire null sink + rootless Podman + systemd user manager):

```
exec hostctl events --type '*' --json &events&
exec hostctl start foot            # firefox on real hardware
exec hostctl audio volume set 40
exec hostctl start nginx-demo
wait-events instance.started:foot audio.volume.changed instance.started:nginx-demo
exec hostctl ps
stdout 'foot.*running'
stdout 'nginx-demo.*running'
```

- [ ] Manual: on the mini PC, from the laptop over TCP with a device token: start Firefox, set volume, start a container, all three visible in `hostctl events` and `hostctl log`.

**M1 done when** both pass, `core/` coverage ≥ 85 %, and CI is green with `-race`.

---

## 4. M2: display depth

Gate: *a remote launch during a game opens in the background, and the controller's Guide button switches to it.*

- [ ] **Presence:** evdev reader for keyboards, mice and gamepads (`/dev/input/event*`, hot-plug via fsnotify on `/dev/input`); idle threshold from config (default 5 min); `display.idle` / `display.active` events
- [ ] **Focus protection:** for `app.start` / `window.focus`, if source ≠ `local` and someone is present and not `front=true` → open on a background workspace, no focus change. `front=true` requires `display.front`.
- [ ] **On-screen notice:** "Jellyfin is ready". The display module emits `display.notice` and sends a desktop notification over D-Bus (`org.freedesktop.Notifications`); `mako` in the Sway session draws it above fullscreen windows. No Wayland protocol code in hostd (D6).
- [ ] **Overlay switcher** (`clients/overlay`, Gio unless the spike says GTK4, D6): a normal fullscreen window app in the catalog (`hostd-overlay`, hidden from the launcher list), run as an instance on workspace `hostd:overlay`. Fullscreen list of instances + catalog launcher. Super (Sway `bindsym` → `hostctl window focus hostd-overlay`) and the controller Guide button (evdev in the daemon → `window.focus hostd-overlay` with source `local`) bring it forward; choosing an entry sends `window.focus` / `app.start` with source `local`.
- [ ] **Flatpak source:** exports dirs → `flatpak` runner (`flatpak run <id>`), ID = last part of the Flatpak ID
- [ ] **Steam source:** parse `libraryfolders.vdf` and `appmanifest_*.acf` (text VDF parser); apps `steam-<appid>`; `steam` runner (`steam steam://rungameid/<id>`); default match rule `class = steam_app_<id>`
- [ ] **Match rules:** `[match] class/app_id/title` used when cgroup matching fails
- [ ] **gamescope wrap:** `window.wrap = "gamescope"` prepends `gamescope -f --` with configured resolution
- [ ] **`url` runner:** opens a kiosk browser window (configurable browser command)
- [ ] `window.place <a> beside <b>`, `display.power` (Sway `output * power on|off`), `display.mode`, `display.output.enable`

**Tests**
- Presence state machine on the fake clock (input bursts, thresholds, hot-plug).
- Focus protection matrix: source × present × `front` × scope.
- evdev: replay a recorded event stream from a file; integration with a **uinput** virtual keyboard/gamepad in the VM.
- VDF/ACF parser: fixtures from real Steam libraries, multiple library folders, fuzz target.
- Flatpak fixtures.
- Headless Sway: place, power, mode commands produce the expected tree/output state.
- Notices: a fake `org.freedesktop.Notifications` service on a private session bus records the call.
- Gate automated in the VM: a uinput gamepad keeps presence active, a remote `app.start` lands on a background workspace and emits `display.notice`, a Guide-button press via uinput focuses the overlay. Gate manual on hardware with real games from the M2 test list (§12).

---

## 5. M3: audio depth

Gate: *a game and a browser have different volumes, and audio moves to a headset on command.*

- [ ] **Stable output names** from node/device properties (`device.bus`, `device.form-factor`, `api.alsa.card.name`, `api.bluez5.address`, `device.description`): `hdmi-1`, `speakers`, `bt-<slug>`. The name ↔ device-identity map is stored in SQLite so names survive reconnects and reboots.
- [ ] `audio.output.set <name>`: set default sink and move existing streams
- [ ] **Per-app streams:** match stream nodes by `application.process.id` → cgroup → instance; `audio.app.volume.set`, `audio.app.mute.set`; app `audio.volume` applied when the stream first appears
- [ ] **MPRIS:** `playerctl` backend behind an interface (`godbus` later if needed); target the focused instance's player (match by PID via `org.freedesktop.DBus.GetConnectionUnixProcessID`) or a named player; `media.changed` events

**Tests**
- Naming: golden test over captured `pw-dump` from setups with HDMI, analog and Bluetooth.
- Reconnect: in the PipeWire container, remove a null sink and re-create it with the same properties → same stable name, new numeric ID.
- Per-app: two `pw-play` streams from two scopes; different volumes applied; mirrored when changed by `wpctl`.
- MPRIS: fake player on a private D-Bus session bus (`dbus-run-session`) in tests.
- Manual: Bluetooth headset connect/disconnect/reconnect on hardware.

---

## 6. M4: services and deploy

Gate: *`git push box main` updates a site with no downtime, and a broken build rolls back.*

- [ ] **`compose` runner:** `docker compose -p hostd-<app>` against the configured socket; status from container labels
- [ ] **`process` runner:** generated user unit `hostd-svc-<app>.service` with `Restart=`, `NoNewPrivileges=yes`, `PrivateTmp=yes`, environment from `[env]`; logs via journald
- [ ] **Health checks:** `http` (2xx within timeout, retries), later `tcp` / `exec`
- [ ] **Deploy state machine** shared by all sources: `fetch → build|pull → start new → health → switch → stop old` or `→ remove new` on failure; releases in `~/hostd/releases/<app>/<sha>`, keep 5; `deploy.rollback`. Runs as `deploy.run` on key `deploy:<app>`.
- [ ] **Zero-downtime switch via a built-in reverse proxy:** for services with `ports`, hostd owns the public port with `httputil.ReverseProxy`. Each release listens on an internal port picked by hostd; after the health check the proxy's upstream is swapped atomically (`atomic.Pointer`), in-flight requests on the old release finish, then the old release is stopped. Plain TCP (non-HTTP) services fall back to stop-then-start, documented as having a short gap.
- [ ] **Sources:**
  - `push`: `hostctl service init <app>` creates `~/hostd/git/<app>.git` + `post-receive` hook; hook output streams back to the pusher; `authorized_keys` forced command wrapper (`git-receive-pack` only)
  - `remote`: poll `git ls-remote` (default 60 s), fetch with a per-app deploy key; branch or tag glob
  - `image`: poll registry manifest digest (OCI distribution API, anonymous or token auth)
  - `webhook`: `POST /v1/hooks/git/<app>`, `X-Hub-Signature-256` HMAC check, exempt from token auth but not from the signature
- [ ] `hostctl deploy key <app>`, `hostctl secret set <name>`. Secrets live in a 0600 file under `~/.config/hostd/secrets/`, unencrypted. Encrypting with a key on the same disk would add little. Documented threat model: protects against other local users and accidental commits of the config repo (the secrets dir sits outside it); does **not** protect against anything running as `screen` or someone holding the disk. `systemd-creds` (TPM-backed) is a later improvement.
- [ ] Deploy events are `deploy.*` (`deploy.started`, `deploy.done`, `deploy.failed`, `deploy.rolled_back`), per D1
- [ ] `hostctl config follow <repo>`: same fetch logic; validate the whole config in a temp dir; apply atomically or emit `config.rejected`
- [ ] `hostctl deploy status`, `GET /v1/services/{id}/logs`

**Tests**
- Deploy state machine with a fake runner: a failure injected at every step leads to the right final state.
- `push` / `remote`: real git with local bare repos in `t.TempDir()`; hook calls a stub `hostctl`.
- `image`: local `registry:2` container; push a new tag → deploy triggered within one poll interval (fake clock in unit tests).
- `webhook`: HMAC valid/invalid/missing/replayed tables.
- Proxy: upstream swap under load (no failed requests, old in-flight requests complete); upstream down → 502 with the error shape.
- Config follow: valid commit applied; invalid commit rejected and the previous config stays.
- **Gate (integration):** deploy v1 of a tiny Go HTTP app → run a curl loop (10 req/s) during the v2 deploy → 0 failures → push v3 with a failing health check → rollback, v2 still serving, `deploy status` shows the failure.

---

## 7. M5: automation

Gate: *a rule and a manual change on the same volume resolve as specified.*

- [ ] **Scenes** (`~/.config/hostd/scenes/*.toml`): read current state from modules, diff, emit only missing actions in fixed order (stop apps → audio output → volume → start apps → focus → display power); per-step results; no undo
- [ ] **Rules** (`rules.toml`): `on` event glob, `if` conditions (app glob, time range, idle minutes), `do` scene or script, `cooldown`; uses the core's chain-depth and rate limits; `hostctl rules` lists rules and paused state
- [ ] **Script runner:** parse `# hostd-scopes:` header or sidecar TOML; mint a short-lived token (expires with the run); env `HOSTD_URL`, `HOSTD_TOKEN`, `HOSTD_EVENT`; timeout (default 60 s); log exit code + action IDs sent with that token
- [ ] Action source for rule-triggered work = `automation` with the rule name; manual `scene.apply` = `manual`

**Tests**
- Scene diff golden tests over combinations of current state; applying twice sends nothing the second time.
- Partial failure: a failing middle step, later steps still attempted, result lists both.
- Rule condition tables (globs, time ranges across midnight, idle).
- Script: missing scope → `forbidden`; timeout kills the process group; token invalid after exit.
- Loop: two real rules triggering each other → `loop_detected`, rule paused, visible in `hostctl rules`.
- **Gate (testscript, fake clock):** reproduce the design's audit example exactly:
  1. manual `volume set 30` → applied v17
  2. rule fires `volume 70` 2 min later → skipped, held by manual
  3. external change to 55 → observed v18
  4. rule fires after the hold expires → applied v19
  Then compare `hostctl log` output to a golden file.

---

## 8. M6: release 0.1

- [ ] **OpenAPI** generated from manifests at `/v1/openapi.json`; golden-file test; validated with an OpenAPI linter in CI
- [ ] **External modules:** core host for JSON-RPC over a unix socket (`hostd.handshake`, `manifest`, `start`, `handle`, `stop`, `emit`, `do`, `subscribe`); process supervision with backoff; same ownership rules. Protocol spec in `sdk/PROTOCOL.md`.
- [ ] `examples/python-module`: a `lights`-style example using only the standard library
- [ ] **Packaging:** nfpm `.deb` (binaries, user units, docs); `deploy/install.sh` (base system done early, §1.2.1) gains the hostd install step
- [ ] **Releases:** GitHub Actions builds `amd64` + `arm64`, signs the checksum file (minisign or cosign); hostd verifies before staging
- [ ] **Update channels:** `github-release` (stable/prerelease), `github-branch` (build on the server), `push-only`; `apply = auto|notify|manual`; wait-for-quiet (no deploy/scene in progress, max 5 min)
- [ ] **Full rollback:** dedicated `hostd-rollback.service`, crash-loop detection (3 failures), state/config migration from the backup
- [ ] **Docs site** (`docs/`, mkdocs or Hugo): install, config reference, API (from OpenAPI), CLI (from testscripts), writing a module

**Tests**
- External-module contract suite: the same module tests used for built-ins run against the Python example over the socket.
- Handshake failure → module rolled back / disabled, core keeps running.
- Signature verification: valid, tampered binary, tampered checksum, wrong key.
- Migrations: start the new version on every old schema; roll back; old version reads its backup.
- **E2E in a VM** (see §9.4): every milestone gate, plus reboot behavior, update during a running instance, broken update rollback.

---

## 9. Test strategy (all milestones)

### 9.1 Layers

| Layer | What | Tooling | When |
| --- | --- | --- | --- |
| Unit | Pure logic: pipeline, holds, parsers, state machines, diffing | `go test -race -shuffle=on`, fake clock | Every commit (< 1 min) |
| Property | Pipeline and scene diff against reference models | `pgregory.net/rapid` | Every commit |
| Fuzz | Action JSON, TOML configs, `.desktop`, VDF/ACF, `wpctl`/`pw-dump` output, Sway IPC framing | Go native fuzzing | CI short run; nightly 5 min/target |
| Contract | One shared suite per interface: `Module`, `Runner`, `DisplayBackend`, `AudioBackend`, catalog `Source` | `xxxtest.Run(t, impl)` | Fakes every commit; real backends with `-tags integration` |
| API / CLI | Routes, errors, scopes, WebSocket; CLI scenarios | `httptest`, `testscript` | Every commit |
| Integration | Real Sway, PipeWire, Podman, systemd, git, registry, in containers | Podman + Containerfiles in `test/integration/` | Every PR |
| E2E | Full system in a VM, milestone gates | QEMU + cloud-init Debian 13 | Nightly, before every release |
| Hardware | Games, controllers, TV/HDMI, Bluetooth | Manual checklists (§12) | End of each milestone |
| Hygiene | Leaks, lint, vulnerabilities | `goleak`, `golangci-lint`, `govulncheck` | Every commit |

### 9.2 Rules for testable code
- Every external system sits behind an interface with an in-memory fake in a `fake` subpackage: systemd D-Bus, Docker API, Sway IPC, `wpctl`/`pw-dump`, `playerctl`, evdev, `/proc`, git, registry, clock.
- No `time.Sleep` in tests; use the fake clock or channels/barriers.
- File-system roots (`/proc`, `/usr/share/applications`, `$XDG_*`) are configurable.
- Fixtures captured from real systems live in `test/testdata/` with a note on how they were captured.

### 9.3 Integration containers (`test/integration/`)

| Image | Contents | Used by |
| --- | --- | --- |
| `hostd-sway` | Sway (headless backend), foot, grim, a minimal Wayland test client | display |
| `hostd-pipewire` | PipeWire, WirePlumber, null sinks, `pw-play` | audio |
| `hostd-systemd` | systemd as PID 1 with a user manager + lingering | exec/process runners (transient services), adoption, self-update and rollback |
| `hostd-all` | All of the above + rootless Podman-in-Podman | M1 gate, deploy |
| `registry:2` | OCI registry | image source |

### 9.4 E2E VM (`test/e2e/`)
- Debian 13 genericcloud image, cloud-init: `screen` user, greetd autologin → Sway with `WLR_BACKENDS=headless`, PipeWire, rootless Podman, lingering, hostd from the freshly built `.deb`.
- Driven from the host over SSH + the hostd API; uinput devices created inside the VM for input.
- Scenarios: each milestone gate; reboot → empty screen and `restart=always` services up; self-update good/broken; an update while a window app and a container run (both survive and are re-adopted).

### 9.5 Coverage and quality bars
- `core/` ≥ 85 % line coverage; `sdk/` ≥ 90 %; modules ≥ 70 % (most behavior covered by contract + integration)
- Zero data races; zero goroutine leaks in core tests
- Every stable error code has at least one API test and one CLI test

---

## 10. Suggested schedule

Rough, for one developer part-time. Adjust after M1.

| Weeks | Work |
| --- | --- |
| 1 | Setup, Phase 0, SDK, registry, bus |
| 2–3 | Pipeline (queues, holds, versions, chain), store, tokens, audit, property tests |
| 4 | HTTP API, WebSocket, `hostctl`, testscripts |
| 5 | Catalog (desktop + TOML), instance state machine, `exec` runner on scopes |
| 6 | `docker` runner, adoption, integration containers |
| 7 | Sway backend, display module, headless-Sway tests |
| 8 | Audio master, daemon lifecycle, simple self-update, **M1 gate**; overlay toolkit spike (D6) |
| 9–11 | **M2** display depth |
| 12–13 | **M3** audio depth |
| 14–17 | **M4** services and deploy |
| 18–19 | **M5** automation |
| 20–22 | **M6** release 0.1 |

---

## 11. Risks to the plan

| Risk | Effect on plan | Mitigation |
| --- | --- | --- |
| systemd user manager in containers is fiddly | Integration tests for scopes delayed | Fall back to the VM for those tests early; keep fakes authoritative for logic |
| Headless Sway behaves differently from a real output (fullscreen, DPMS) | False passes in CI | Run each display gate on real hardware too |
| Built-in reverse proxy adds scope to M4 | M4 grows | Keep it HTTP-only on `httputil.ReverseProxy`; plain TCP services accept a short gap |
| Sway on the proprietary NVIDIA driver is unsupported upstream | Glitches in fullscreen games or after driver updates | Pin the driver from Debian stable; test top titles in M2; per-app `gamescope` wrap; the Ryzen iGPU is the fallback |
| Overlay controller navigation | M2 slips | Spike in week 8 (D6); it is a plain window, so no layer-shell risk |
| Session environment missing from transient services | Apps start but can't open a window | Pass session variables explicitly (§3.8); integration test checks the environment |
| Games misbehave under Sway | M2 gate fails on real titles | Test list early (§12), `gamescope` wrap |
| `wpctl`/`playerctl` output format changes between versions | Parsers break | Fixtures per version; parse JSON (`pw-dump`) where possible |

---

## 12. Hardware test lists (to fill in before M2/M4)

**Games and emulators (M2)**
- [ ] _Steam title 1 (native)_
- [ ] _Steam title 2 (Proton)_
- [ ] _Emulator (RetroArch Flatpak)_
- [ ] _Browser-based (Jellyfin via `url` runner)_

**Controllers (M2):** _e.g. Xbox, DualSense, 8BitDo_ (Guide button mapping per model)

**Audio outputs (M3):** HDMI to TV, analog speakers, _Bluetooth headset model_

**Services (M4)**
- [ ] _Static site via `docker` + `push`_
- [ ] _Jellyfin via `compose` + `image`_
- [ ] _Python script via `process` + `remote`_

---

## 13. Definition of done (per milestone)

A milestone is done when:
1. All its checklist items are `[x]`.
2. Its gate passes automatically (integration or VM) **and** manually on the mini PC.
3. CI is green: unit, race, lint, integration; nightly fuzz and e2e green for 3 consecutive nights.
4. Docs updated: config reference, CLI testscripts, any new error codes or events.
5. The design doc is updated if the implementation changed a decision.
