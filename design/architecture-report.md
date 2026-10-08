# hostd architecture report: quality, stability, modularity

Date 2026-10-08, commit `de2af28` (M1 complete; M2 code complete). Scope: every folder in the repository. Method: static reading, dependency and size measurements, test coverage, and the behaviour seen on the devbox and on core.

## 1. Summary

| Aspect | Rating | In one line |
| --- | --- | --- |
| **Modularity** | Good, with two leaks | The layering is strict: modules import only `sdk` and `internal/clock`. But the display module depends on the apps module through unwritten contracts: action names, the JSON shape of a read, unit names. |
| **Stability** | Good core, weaker edges | The core is well tested (95% coverage, race detector, goroutine-leak and property tests). The weak spots are recovery after a lost event stream (event bus, systemd, Podman) and errors silently ignored when talking to Sway. |
| **Quality** | Good | Consistent style; context everywhere; lint gates every push. Two oversized files (display `module.go` and hostctl `commands.go`). The Python menu and the shell scripts get no checks in CI. |

**Top 5 risks**, details in §6:
1. **H1** If the display module falls behind on events, the event bus drops its subscription, and the display module silently stops following app starts and ends until hostd restarts.
2. **H2** If the event stream from systemd or Podman breaks and reconnects, any app that ended during the gap stays "running" forever.
3. **H3** Display↔apps contracts exist only as strings in code (`app.start`, `instance.*`, the `instances` read, the `hostd-<id>.service` unit name, the `apps` scope). `modules = ["display"]` alone fails to start with an unclear error.
4. **M2** Failures of commands sent to Sway (show, focus, fullscreen) are ignored in 11 places, so the screen can end up wrong with nothing in the log.
5. **M3** Every push to main publishes a build to the real machine, gated only by unit tests and lint. The install test, shellcheck and checks on the Python menu are not in CI.

## 2. Inventory

| Folder | Role | Go src / test lines | Coverage |
| --- | --- | --- | --- |
| `sdk/` | Module contract: Module, Core, Action/Event, Manifest, schemas, keys | 926 / 600 | 96.8% |
| `core/` | Registry, action pipeline, locks, holds, bus, audit, limits | 1395 / 1710 | 94.6% |
| `core/store/` | SQLite: tokens (hashed), audit trail, backups | 591 / 400 | 86.6% |
| `core/api/` | HTTP API, WebSocket events, private-network filter, update upload | 826 / 759 | 84.2% |
| `internal/daemon/` | Composition root: flags, hostd.toml, module wiring, serve/shutdown | 479 / 234 | 68.8% |
| `internal/update/` | Staged binaries, `current`/`previous` symlinks | 237 / 144 | 80.6% |
| `internal/client/` | Go API client (used by hostctl) | 351 / 0 | (via hostctl) |
| `internal/{clock,ids,sdnotify,version,testutil}` | Fake clock, ULIDs, sd_notify, version, test module | 511 / 354 | 90–100% |
| `modules/apps/` | Catalog (desktop, Flatpak, files), instances, runners (exec, docker, handoff) | 3315 / 2174 | 82.7% |
| `modules/display/` | Sway backend, windows, focus, presence, input bindings, menu action | 2576 / 1612 | 85.7% |
| `modules/audio/` | Master volume and mute (wpctl, pw-dump) | 530 / 440 | 89.0% |
| `modules/demo/` | Pretend lamp for trying hostctl | 110 / 0 | n/a |
| `cmd/hostctl/` | CLI (cobra), commands generated from manifests | 1508 / 104 + 4 testscripts | 59.4% |
| `cmd/hostd/` | `main` → `daemon.Run` | 14 | n/a |
| `deploy/` | Installer (548 lines of bash), checks, M1 gate, unit files, Sway/mako configs | ~720 shell | container test (55 checks) |
| `deploy/files/menu/` | On-screen menu (Python, GTK 4) | 543 Python | none |
| `test/install/`, `test/devbox/` | Container install lifecycle; devbox (headless Sway, VNC) | ~220 shell | n/a |
| `design/` | Design doc (781 lines), plan (675 lines) | n/a | n/a |
| `tools/`, `.github/` | Pinned linters; CI (lint → test → build → cover → edge release) | n/a | n/a |

In total: about 13,400 lines of Go, a test/source ratio of about 0.75, 13 direct dependencies, and a pure-Go static binary (no cgo).

## 3. Architecture as built

```
cmd/hostd ──► internal/daemon (composition root: config, wiring, lifecycle)
                 ├─► core ─► sdk, internal/{clock,ids}
                 ├─► core/store ─► core (implements core.Audit), sdk
                 ├─► core/api ─► core, core/store, sdk
                 ├─► internal/{update,sdnotify,version}
                 └─► modules/{apps,display,audio,demo} ─► sdk, internal/clock   (never each other)
cmd/hostctl ─► internal/client ─► sdk                          (no core import: API only)
deploy/files/menu (Python) ─► HTTP API over the unix socket       (an API client, not a module)
```

**What holds well:**
- **Dependency direction is clean and acyclic.** No module imports another module or the core, and the core never imports `store` or `api` (`store` plugs into the `core.Audit` interface).
- **One way to change anything.** Every change, whether it comes from the CLI, the API, keys, the controller, the menu or a module, is an `sdk.Action` through `core.Engine.submit` (`core/engine.go:213`): validate, authorize, take locks, hold rules, audit. The same applies to the screen inputs since the input refactor (`modules/display/bindings.go`).
- **Reads and events are declared in manifests,** so routes and CLI commands are generated (`core/api/api.go`, `cmd/hostctl/modules.go`), and the registry checks scope references across modules (`core/registry.go:98`).
- **Runners sit behind interfaces** (`apps.Backend`, `apps.Systemd`, `display.Backend`, `audio.Backend`, `display.Input`, `display.Notifier`). Every external system has a fake, which is why coverage is high without real hardware.

**Where the boundaries leak (modularity debt):**
- **display → apps**, implicit:
  - Actions `app.start` and `instance.stop` (`modules/display/actions.go:373`, `modules/display/module.go:756`).
  - The `instance.*` events, decoded into display's own struct (`modules/display/module.go:355-400`).
  - The `apps/instances` read as untyped JSON through `sdk.Core.Read` (`modules/display/actions.go:40`).
  - The unit-name convention `hostd-<instance>.service`, parsed in `modules/display/match.go:45` and generated in `modules/apps/exec.go:167`.
  - The scope `apps`, which only the apps module declares.

  None of this is written in the display manifest (it has no `Requires`).
- **The menu ↔ hostd:** controller reading exists twice, in Go (`modules/display/input.go`) and in Python (`deploy/files/menu/hostd-menu`, the `Controllers` class). The menu polls `/v1/instances` and `/v1/apps` every second instead of using the `/v1/events` WebSocket that exists for this.
- **`sdk.Core` grows with each need** (`Read` was added in M2). Once external modules exist (M6), this interface becomes a wire protocol and must be frozen and versioned.

## 4. Folder by folder

### `sdk/`
- **Strengths.** Small and documented. Golden tests on manifest JSON; schema validation with readable messages; key templates; error codes shared by the API and the CLI.
- **Issues.**
  - `Manifest.Validate` is 167 lines (`sdk/manifest.go:135`).
  - The `Reads` declarations have no response schema, so cross-module reads have no contract.
  - `mustJSON` helpers that panic are copied four times (`core/engine.go` and `modules/{apps,audio,display}`); they belong in `sdk`.

### `core/`
- **Strengths.**
  - The pipeline is the best-tested part: 94.6% coverage, race detector, `goleak`, `rapid` property tests on locks and holds, and a fake clock.
  - Locks are per key and FIFO, with re-entry for child actions.
  - Cause chains are capped at depth 5, with a per-rule limiter.
  - The bus is bounded (256 events per subscriber), and a slow subscriber is dropped with `bus.lagged` rather than blocking publishers (`core/bus.go:62-68`).
- **Issues.**
  - A dropped subscriber is a closed channel. API WebSocket clients handle this (`bus.gap`, replay with `since`), but in-process modules do not (H1).
  - `run` (93 lines) and `submit` (80 lines) are long but readable.
  - `panic(err)` in `core/engine.go:599` is another copy of the `mustJSON` helper; it is reached only by a programming error.

### `core/store/`
- **Strengths.**
  - WAL, `busy_timeout`, foreign keys (`core/store/store.go:90`).
  - Tokens are stored as SHA-256 hashes, and the audit trail is capped (`core/store/audit.go:118`).
  - Online backup with `VACUUM INTO`, and schema migrations.
- **Issues.** None of importance. The audit cap is by row count, not age; fine for one machine.

### `core/api/`
- **Strengths.**
  - Bearer tokens checked per request.
  - The TCP listener accepts private networks only (`core/api/netcheck.go`).
  - `ReadHeaderTimeout`, a body size limit (`core/api/api.go:547`), and a status table matching the SDK error codes.
  - Subscribe before Accept on WebSocket, so no event is lost.
- **Issues.**
  - No TLS on `:7300`: tokens cross the LAN in clear text (M5).
  - No write/idle timeouts on the HTTP servers (WebSockets need special care, but plain routes could have them).

### `internal/daemon/`
- **Strengths.**
  - The one place that knows every concrete module.
  - Strict `hostd.toml`: unknown keys, modules or bindings stop startup with a clear message.
  - Signal handling and sd_notify readiness.
- **Issues.**
  - `Run` is 91 lines, and coverage is 68.8%.
  - Module wiring is a hard-coded `switch` (fine until external modules arrive in M6).

### `internal/update/` and `deploy/files/rollback.sh`
- **Strengths.**
  - Atomic symlink switch, version check of the uploaded binary, pruning of old versions.
  - The rollback script needs no hostd, and both paths are tested in the container lifecycle test.
- **Issues.**
  - Rollback trusts the `previous` file.
  - There is no "never-ready" end-to-end test on real systemd beyond the container (already in the plan).

### `internal/client/`
- **Issues.** No direct tests: it is covered only through hostctl testscripts. Its error-mapping code would benefit from table tests.

### `modules/apps/`
- **Strengths.**
  - Catalog building is pure and golden-tested.
  - The instance state machine is explicit (`modules/apps/instance.go`).
  - Instances survive a hostd restart (adoption from systemd units and container labels).
  - Handoff is generic, with no app-specific code (a rule from the user).
- **Issues.**
  - After an event-stream reconnect there is no reconciliation (H2): `watchBackend` (`modules/apps/instances.go`) calls `Watch` again but never re-lists units or containers.
  - `handleStart` is 112 lines and mixes policy (single/new/restart/focus), the handling of app actions, and runner calls.
  - Closing a terminal through hostd is recorded as `failed`: the terminal's exit status 1 counts as failure (L4).
  - Handoff liveness scans the environment of every process in `/proc` every 2 s for each handed-off instance. That's fine at this scale; note it for many handoffs.

### `modules/display/`
- **Strengths.**
  - The Sway backend is isolated behind `Backend`, and tested against a fake IPC server with recorded fixtures.
  - Presence uses a fake clock.
  - The input model is one table driving actions.
  - Focus protection, match rules and placement are each tested.
- **Issues.**
  - **`module.go` is 990 lines.** One mutex guards 13 fields (windows, stack, prefs, closeWait, launches, ending, gone, focusSeq, keys, buttons, pressing, presence, backend). `onEvent` alone is 126 lines (`modules/display/module.go:423`). This is the main readability and regression risk in the codebase (M1).
  - **Ignored errors.** Eleven `_ =` on compositor calls in `module.go`, and three in `actions.go` (M2).
  - **No recovery.** `followInstances` ends silently if its subscription is dropped (H1).
  - **Implicit contracts** with apps (H3).

### `modules/audio/`
- **Strengths.**
  - Small. Echo suppression for its own changes.
  - The `wpctl` feedback loop found on core was fixed, with a regression test (`modules/audio/wireplumber.go`, `relevant`).
- **Issues.**
  - Every relevant PipeWire change starts a `wpctl` process. Acceptable now; M3 (per-app audio) will multiply this.
  - Consider reading the state from the `pw-dump` stream itself.

### `cmd/hostctl/`
- **Strengths.** Commands are generated from manifests; distinct exit codes for each error code; testscripts run against a real hostd.
- **Issues.** `commands.go` is 1040 lines, and coverage is 59.4%. It should be split by command group: tokens, apps and instances, windows, update, events (L1).

### `deploy/` (installer, configs, units)
- **Strengths.**
  - Idempotent, with explicit install, update and uninstall commands, and backups of files it replaces.
  - Tested end to end in a fresh Debian container (55 checks, including the update, rollback and reinstall paths).
- **Issues.**
  - 548 lines of bash with no shellcheck in CI (M3).
  - Downloads are verified against a checksum file from the same release, with no signature (M6, planned).

### `deploy/files/menu/` (Python, GTK 4)
- **Strengths.** Only an API client: it changes the screen through hostd actions, with the `local` token.
- **Issues.**
  - No tests, no lint, no CI check (M3, M8).
  - Duplicate gamepad parsing (M4).
  - Polls every second while in front (M4).
  - Several GTK subtleties found by trial on the devbox: popover parenting, focus refresh, key routing. They deserve a short comment block and a smoke test that runs under the devbox's headless Sway.

### `test/`
- **`install/`**: excellent value; it should run in CI on changes under `deploy/`.
- **`devbox/`**: a good manual environment. The VNC key/click driver used during development (`vnckeys.py`, currently only in the scratchpad) should become `test/devbox/vnc.py`, to script end-to-end checks.
- **Integration (`test/integration`), end-to-end (`test/e2e`) and scheduled fuzzing:** referenced by the Makefile but not built yet. Only one fuzz target exists (`modules/apps/catalog_test.go`).

### `design/`, `README.md`
- **Drift:**
  - `README.md` says "Status: early development, nothing usable yet".
  - `plan.md` D6 still describes a Gio "overlay" on workspace `hostd:overlay`, and `plan.md:103` lists a `clients/overlay/` directory that doesn't exist.
- **The design doc is current** after this session's edits: input model, menu, handoff, app actions.

## 5. Cross-cutting

| Topic | State |
| --- | --- |
| **Concurrency** | Contexts are passed everywhere. Every long-running goroutine is tied to a `WaitGroup` and a cancel; `goleak` runs in the core and module tests; tests use the race detector with shuffled order (`Makefile:30`). The risk is concentrated in the display module's single big mutex and its callbacks from three sources: Sway events, input, and core events. |
| **Error handling** | `sdk.Error` codes flow from module to API to CLI exit code. Weak spots: ignored compositor errors (M2); best-effort `_ =` on cleanup (acceptable, but these should log). |
| **Failure recovery** | The display reattaches to Sway in a loop; hostd restarts and rolls back through systemd. Missing: resubscribing after bus lag (H1), and reconciling after a backend stream gap (H2). |
| **Configuration** | One strict `hostd.toml`; app files with clear problem reports; bindings validated at startup. Good. |
| **Security** | Hashed 256-bit tokens with scopes and kinds; private-network filter; admin-only update upload. Gaps: no TLS on the LAN (M5); unsigned builds (M6). The `screen` user is in the `input` group, so every app hostd starts can read keyboards (document this; consider a separate input helper later). |
| **Observability** | `slog` and an audit trail of every action with its source; `hostctl log` and `hostctl events`. Missing: debug logs for ignored compositor errors; a `/v1/health` endpoint with module states (partly covered by `/v1/state`). |
| **Testing** | Strong unit layer and a full container lifecycle test. Fragile spots: 9 `time.Sleep` calls in tests and polling `waitFor` helpers. A flaky audio test was fixed twice this session; prefer explicit hooks and the fake clock. |
| **Toolchain/CI** | Pinned linters (`tools/go.mod`), `govulncheck`, golden files; publishing gated on tests. Missing jobs: shellcheck, Python checks, install test, fuzzing. |

## 6. Findings, ranked

| ID | Sev | Finding | Evidence | Fix | Effort |
| --- | --- | --- | --- | --- | --- |
| H1 | High | A module's event subscription can be dropped by the bus (lag), and the module never notices | `core/bus.go:62-68`; `modules/display/module.go:271,355` | Add `sdk`-level resubscribe: `Core.Subscribe` resubscribes after `bus.lagged` and tells the module to resync (display re-reads `apps/instances`). Test it with a tiny bus buffer. | S |
| H2 | High | Instances that end while a runner's event stream is down stay "running" | `modules/apps/instances.go` `watchBackend`; `exec.go` / `docker.go` Watch | After every (re)connect, reconcile: list units/containers (reusing `Adopt` logic) and emit ends for vanished instances | M |
| H3 | High | Display↔apps contracts are implicit; `modules=["display"]` fails with a confusing scope error | `modules/display/actions.go:40,373`; `match.go:45` vs `apps/exec.go:167`; display manifest has no `Requires` | Declare `Requires: ["apps"]` (or an optional "uses"). Move the unit-name convention and the instance JSON type into a small shared contract package under `sdk` (e.g. `sdk/contract`), or give reads response schemas checked in tests. | M |
| M1 | Med | Display `module.go` is a 990-line god object behind one mutex | `modules/display/module.go`; `onEvent` 126 lines | Split into a window tracker, focus history, placement policy, launches/end-waits, and inputs; each with its own small lock or owned by one goroutine | M |
| M2 | Med | Compositor command failures are ignored | 11× `_ =` in `modules/display/module.go`, 3× in `actions.go` | Wrap them in a helper that logs (warn once per kind) and counts; surface them in `/v1/display` | S |
| M3 | Med | CI checks neither the shell nor the Python code, and doesn't run the install test before publishing to devices. Example: the "menu" rename broke an install-test check, CI still published the build, and only a local run caught it | `.github/workflows/ci.yml` | Add jobs: shellcheck; `python3 -m py_compile` plus ruff on the menu; `make test-install` when `deploy/**` or `test/install/**` changes (and nightly); scheduled fuzzing | S |
| M4 | Med | The menu duplicates controller parsing and polls the API | `deploy/files/menu/hostd-menu` (`Controllers`, `poll`) | Use `/v1/events` over WebSocket for refresh. Either keep in-app controller reading as a documented in-app concern, or have hostd emit `input.nav` events to the focused menu. | M |
| M5 | Med | API tokens travel in clear text on the LAN | `core/api/api.go:204` (TCP listener, no TLS) | Optional TLS (self-signed with a pinned fingerprint in `hostctl login`), or document running behind Tailscale or a reverse proxy | M |
| M6 | Med | Builds are not signed; the installer trusts the checksums from the same release | `deploy/install.sh`, CI edge job | Planned for M6: minisign/cosign, verified by the installer and the updater | M |
| M7 | Med | Timing-based tests (9 sleeps, polling) can still flake | `modules/display/*_test.go`, `modules/apps/*_test.go` | Replace sleeps with hooks (e.g. a `done` channel on handlers) or the fake clock; run `go test -count=20` on the timing-heavy packages nightly | S |
| M8 | Med | Thin coverage at the edges: hostctl 59%, daemon 69%, client and menu untested | coverage table | Table tests for `internal/client` error mapping; daemon wiring tests; a menu smoke test on the devbox | M |
| L1 | Low | `cmd/hostctl/commands.go` is 1040 lines | | Split by command group | S |
| L2 | Low | Docs drift (README status, plan D6 and `clients/overlay`) | `README.md`, `design/plan.md:20,103` | Update the text | S |
| L3 | Low | Long functions: `Manifest.Validate` 167, `handleStart` 112, `daemon.Run` 91 lines | §4 | Extract steps when next touched | S |
| L4 | Low | An app closed through hostd is recorded as "failed" | `modules/apps` end handling | Treat an end within the close window as `exited` | S |
| L5 | Low | Four copies of a panicking `mustJSON` | `core/engine.go:596`, `modules/{apps,audio,display}` | Move into `sdk` | S |
| L6 | Low | `sdk.Core` grows ad hoc (`Read` added in M2) | `sdk/module.go` | Freeze and version it before external modules (M6) | S |

### Status after the first round of fixes (2026-10-08)

| ID | Status |
| --- | --- |
| H1 | **Fixed.** Module subscriptions survive lag: the core subscribes again and passes on `bus.lagged` (`sdk.EventLagged`). The display module then re-reads the running instances. Tests: `core` `TestModuleSubscriptionSurvivesLag`, `display` `TestResyncAfterLag`. |
| H2 | **Fixed.** After a runner's stream reconnects, the apps module asks the runner what still runs and ends the instances that vanished (`reconcile`). Test: `TestReconcileAfterLostStream`. |
| H2b | **Found and fixed while testing H2.** `ExecRunner.Watch` waited for its poller before cancelling it. A stream that ended with an error (a lost D-Bus connection) never returned, so hostd stopped noticing app ends until restarted. This was in production since the handoff change. |
| M2 | **Fixed.** Failed compositor commands are logged (a warning once a minute for each kind, the rest at debug level) and counted in `GET /v1/display` (`failed_commands`). |
| M3 | **Fixed.** CI job `scripts` (`make lint-scripts`: shellcheck, plus ruff and a compile check for the menu) gates the edge release. The workflow `install.yml` runs the container install test on installer changes, nightly and on demand, with nightly fuzzing. |
| L2 | **Fixed.** README status, plan D6 and the planned layout. |
| M7 | **One cause fixed.** The display module subscribed to instance events in a goroutine, so an event right after `Start` could be missed (`TestWindowedPreference` failed under shuffle). It now subscribes before `Start` returns. Other timing-based tests remain. |
| L4 | **Open.** Belongs with H3: the apps module cannot tell that an end followed a close request without a declared contract with the display module. |

## 7. Recommended order

1. **Stability now, before M3** (about 1–2 days): H1, H2, M2, M3, L4, plus the L2 doc fixes.
2. **Modularity before M3/M4**, while only three modules exist: H3 (contract package, `Requires`), M1 (split display), L5, L6.
3. **Menu**: M4 (WebSocket events), M8 (menu smoke test, using the VNC driver from `test/devbox`).
4. **Release hardening (M6)**: M5 (TLS option), M6 (signing), external-module protocol built on a frozen `sdk.Core`.
