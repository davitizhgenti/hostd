# hostd: simplification and modularity plan

Status: draft for review, 2026-10-14. Companion to [design/plan.md](design/plan.md) (milestones M1–M6) and [design/architecture-report.md](design/architecture-report.md) (state at commit `de2af28`).

This plan covers what to change after 0.1 is tagged. Nothing here is a blocker for 0.1. Every claim about the code was checked against the tree on the date above. Where a number is a target, it is labelled a target. Where it is unmeasured, it says so.

## 1. Principles

1. **Don't touch the frozen surface casually.** The `sdk.Core` interface, the module protocol (`sdk/PROTOCOL.md`) and the HTTP API are v1. Additions are fine. Renames and removals need a compatibility path (§4).
2. **Measure before optimising.** Performance items start with a measurement. If it shows no problem, the item is dropped.
3. **Build an abstraction when a second user exists.** No interface for a compositor, runner or transport nobody has asked for. Splitting by responsibility is fine. Pluggability for its own sake is not.
4. **Stay generic.** No app-specific code in hostd (Steam, Pterodactyl and the like go through handoff, match rules and optional add-ons).
5. **Every step ships alone**, passes `make` and the existing gates, and can be reverted on its own.

## 2. Findings

Each finding has a verdict from reading the code: **confirmed**, **partly true** or **dropped**.

| # | Finding | Verdict | Evidence |
| - | - | - | - |
| F1 | Rules can only run a scene or a script. | Confirmed | `modules/automation/rules.go:108` requires exactly one of `Do.Scene` / `Do.Script`. A one-action rule needs a scene file. |
| F2 | Scenes know only audio, apps and display. | Confirmed | `Scene` struct in `modules/automation/scenes.go:23`. Modules loaded later, built-in or external, can't take part. |
| F3 | Input, presence, controllers and GPU live in `display`, so display is a grab-bag and a machine without it has no presence or controllers. | **Partly true** | `daemon.go:102` wires evdev, profiles, idle and bindings into `display.New`. But `display` already runs without a compositor ("a missing compositor is not an error: the module attaches when Sway appears", `module.go`), so a headless box *can* keep input by loading it. The module is large (2,576 lines) and mixes concerns, but the functional cost is small. Presence also has no value without a screen. |
| F4 | `audio.Graph` forks `pw-dump` on every read while a `--monitor` stream already runs. | Confirmed | `modules/audio/outputs.go:35` and `wireplumber.go:114`. |
| F5 | The secrets directory belongs to `apps`, and `deploy` is handed `apps.DefaultSecretsDir()`. | **Partly true, low impact** | `modules/deploy` never imported `modules/apps`; only the composition root (`daemon.go`) borrowed `apps.DefaultSecretsDir()`. What was duplicated was the name rule (`secretName` regexp in both modules), the directory convention and the file handling. |
| F6 | `hostctl commands.go` is 1,089 lines of hand-written commands next to a manifest-driven generator (`modules.go`, 291 lines). | Confirmed | `builtins` map at `commands.go:28`. |
| F7 | The handoff poll scans `/proc` for every process in the OS every 2s. | **Partly true** | It only scans while at least one handoff instance exists (`handoff.go` loops over `r.handoffs`). It reads each process's environ for the user's processes only. The cost is real during a Steam session, not constant. |
| F8 | The runner switch in `apps` is closed. | **Partly true** | `Options.Backends` is already a registry keyed by runner. Only flatpak, url and process alias to exec (`instances.go:476`). Adding a *new engine* is one `Backend` plus its catalog validation, not a four-file edit. |
| F9 | The menu reads evdev in Python and duplicates input handling. | **Dropped** | `hostd-menu` has no evdev code. It follows `/v1/events` over WebSocket and takes keyboard input through GTK. |
| F10 | `docs/protocol.md` duplicates `sdk/PROTOCOL.md`. | **Dropped** | It is already a symlink. |
| F11 | `install.sh` is 909 lines of bash and add-ons are sourced bash (793 lines across `deploy/addons/*/addon.sh`). | Confirmed, but it works. The "two install paths" claim is **dropped** | The `.deb` (`packaging/nfpm.yaml`) only ships the two binaries, the user unit and docs. `install.sh` sets up the whole machine (Sway, PipeWire, greetd, the `screen` user, NVIDIA). They are different layers. Both are tested: `test/install/*` (container) and `test/e2e` (VM). |
| F12 | Extmod modules must open a unix socket. | Confirmed | `internal/extmod/module.go:100`. Stdio would be simpler for module authors. |
| F13 | `display` ↔ `apps` contracts exist only as strings. | **Mostly dropped** | The names are already constants in `contract/apps.go` (actions, events, reads, scopes, unit names) and both modules use them. `display` declares `Requires: apps`, and `modules = ["display"]` stops with `module "display" requires "apps", which is not enabled` (reproduced). The apps side already tests the `Instance` shape and unit names (`runners_test.go`). What was missing was a test running the two real modules together, since `display`'s tests used a fake apps module. |
| F15 | The input path is entangled with display state, which constrains any split (§W5). | Confirmed | `onInput` (`bindings.go:117`) updates `m.down`, `presence`, `menuFront` and the controller grab. Keys do not come from evdev at all: Sway reports them (`Backend.Bind`), and they also feed presence (`windows.go:27`). Presence is read by display itself to decide whether a launch comes to the front (`launches.go:197,234,240`, `actions.go:304`). |
| F16 | There is no `display.Backend` interface. | **Dropped: it exists.** | `modules/display/sway.go:71` defines `Backend` (Windows, Outputs, Show, Move, Focus, Fullscreen, CloseWindow, Bind, Place, SetOutput, Watch). Sway is its only implementation. |
| F14 | `display/actions.go` (718 lines) and `module.go` (487) are oversized. | Confirmed | Resolves partly as a by-product of F3. |

## 3. Work items

Ordered by value over risk. Items are independent unless a dependency is stated.

### W1. Rules dispatch an action directly (F1) — done

**Change:** `do = { action = "audio.mute.set", args = { muted = true } }` as a third alternative to `scene` / `script`. Exactly one of the three is required, and `args` goes only with `action`. The action goes through `core.Do` with the rule's source (automation, the rule's name) and the event as its cause, the same way a scene does, so loop limits, the rate limiter and the audit trail apply unchanged.

**Compatibility:** additive. Existing rules files are valid. The one change is the wording of the error for a rule with no `do`: "do needs a scene, a script or an action (one of them)".

**Differs from the first draft:** the action's name and arguments are checked when the rule fires, not when the file loads. A rule may name a module that starts later (an external one), and rejecting the whole `rules.toml` for that would stop every other rule. At load, only the shape is checked (an action type like `audio.mute.set`); an action no loaded module handles gets a warning in the log. At fire time the core rejects it, and `hostctl rules` shows "failed: ...".

**Checked:** parse tests (valid, two alternatives, bad type, `args` without `action`, unknown key); a rule sends the action with no scene in between, as automation with a cause in the audit trail; an unknown action is reported as the rule's failure; a rule whose action produces the event that fires it is paused by the loop limit. `docs/automation.md` and `hostctl rules` (which shows `action <type>`) are updated.

### W2. Audio reads come from the live graph (F4) — done

**Change:** `pw-dump --monitor` already streams the graph. A cache (`modules/audio/graphcache.go`) now keeps nodes, devices and the default sink from it, and `WirePlumber.Graph` answers from the cache. Writes stay on `wpctl` and `pw-metadata`.

**What the stream does, found by running it** (the first draft of this item assumed otherwise):
- The first batch is the whole graph. Later batches carry each changed object *in full*, with all its params, so the cache replaces objects by ID. A removed object is `{"id": N, "info": null}`.
- A **metadata object carries only the key that changed**, not the whole list. Replacing it would lose the default sink, so metadata is merged by key.
- The audio module re-reads the graph after *every* settled change (`followGraph`), not only on a read request, so the forks were per change batch as well as per read.

**Fallback:** before the first dump and after the monitor stops (the cache is cleared then), `Graph` runs `pw-dump` as before.

**Also changed:** `parseGraph` sorts streams by ID, since pw-dump lists objects in no fixed order.

**Checked:**
- Unit tests: cache equals `parseGraph` of a dump; a changed node, an unrelated and a default-sink metadata delta, stream add and remove, output removal; callers can't edit the cache through what they get; reset.
- A fake `pw-dump` that counts forks: none while the monitor runs, one again after it stops.
- Against the live PipeWire on the dev laptop, the cache and a fresh fork returned the same outputs and streams at start, with a stream added, with volumes changed and with a stream ended (streams differed only in order, which is the sort above).
- **On core (2026-10-14):** the new build ran as a second, isolated instance (audio module only, its own socket and state, with a counting `pw-dump` shim; the running hostd was untouched). Three silent streams (6 graph changes) and 5 reads: the old build (`HEAD` before W2) made **6 extra `pw-dump` forks** (8 total with its 2 at start); the new build made **none** (1 total, at start). Streams appeared and disappeared correctly in `GET /v1/audio`. The test files on core were removed afterwards.
- Not measured: read latency (reads were already answered from `m.outputs` after each change; what W2 removed is the fork on every change).

**Known gap:** a metadata key *removal* is not visible in what pw-dump printed in a test (setting a key was; deleting it printed nothing). Losing `default.audio.sink` is rare (`wpctl set-default` replaces it), and the cache is rebuilt whenever the monitor restarts.

### W3. Handoff polling: measured, closed with no code change (F7)

**Measurement (core, 2026-10-14, hostd's own `pidsWithEnv` scan run as a benchmark on the machine):** one full scan of `/proc` for a marker takes **1.9 ms** with 217 processes on core (no Steam running). Scaled to a busy Steam session of about twice the processes, that is about 4 ms. The poll runs every 2 s and **only while a handoff instance exists**, so the cost is about **0.1% of one core**, roughly 0.2% with Steam up, and zero otherwise. On the dev laptop (385 entries, many not readable) a scan takes 5.6 ms.

**Decision:** negligible, so the cache-the-matched-PIDs change in the first draft is not worth its added state. No code change. Reopen only if a machine with thousands of processes shows up, or if the 2 s wake-up matters for power (it is a timer, not I/O, so it does not keep disks busy).

**Not measured:** a real Steam session on core (it would need Steam started on core's screen); the figure above scales the process count instead.

### W4. One secret store (F5) — done

**Change:** `internal/secrets` holds the name rule, the default directory, and `Store` (read, atomic 0600 write, remove, list). The apps module's `secret.*` actions, its `{secret:<name>}` expansion and the deploy module's webhook secret all use it. `daemon.go` takes the directory from it. The duplicate regexp and the file code in `deploy` are gone.

**What did not change:** the `secret.set` / `secret.remove` actions, their scope (admin), the `GET /v1/secrets` read, the directory (`~/.config/hostd/secrets`), file modes and messages. The secret actions stay in the apps module, since moving them to a new module would change the API for no gain here.

**Checked:** the existing apps, deploy and daemon tests pass unchanged. New tests cover the name rule, permissions (0600 files, 0700 directory), replace and remove, and that a name with a path in it never reaches the file system.

**Not done:** the plan's "a third consumer" case (an external module reading secrets). It would need a way for modules to read secrets through the core, which nothing asks for yet.

### W5. Slim `display` by moving the pure input code out (F3, F14, F15) — done

**Decision: no new `input` module, no renamed actions, no changed events.** An earlier draft of this plan proposed one. Reading the code shows why it is the wrong trade (F15):

- Keys come from Sway, not evdev, so a separate `input` module can't own them. Only controller buttons and presence's device half are evdev.
- Display consumes presence itself (does a launch come to the front, is a remote action allowed) and must tell the input side when the menu is in front (`takeControllers` / `EVIOCGRAB`). A module split makes that a two-way protocol over `core.Do` and events, with races between "menu opened" and "button still held" that the single-mutex design avoids today.
- The public surface that would break is large: `display.active`, `display.idle`, `display.nav`, `controller.*` events, `GET /v1/controllers`, `hostctl controllers`, `[input]` in `hostd.toml`, `docs/automation.md` rules, `deploy/files/menu/hostd-menu` and the design doc.
- The gain is mostly cosmetic. Headless machines can already keep input by loading `display` with no compositor (F3).

**What to do instead: move the parts that don't touch `Module` state into internal packages.** From the method counts, these files have no or almost no `Module` methods:

| File | Lines | `Module` methods | Destination |
| - | - | - | - |
| `input.go` (evdev reader, `Grabber`) | 310 | 0 | `internal/evdev` |
| `controllers.go` (profiles, listing) | 376 | 2 (`followControllers`, `emitController`) | `internal/evdev` (keep the two methods in `display`) |
| `presence.go` | 96 | 0 | `internal/presence` |
| `gpu.go` | 134 | 1 (`followGPU`) | `internal/gpu` (keep the method in `display`) |
| `notify.go` | 71 | 0 | `internal/notify` |

`display` keeps the glue: `bindings.go` (`onInput`, hold, grab sync), the actions, windows and the Sway backend. About 900 of 2,576 lines move (the five files total about 990 lines, less the three `Module` methods that stay). This is an estimate until the move is tried. `Profiles` and `Evdev` are exported types used by `internal/daemon/daemon.go`, so those imports change in one place.

**What was done:** five files moved into internal packages with `git mv`, the glue stayed.

| Was (`modules/display`) | Now |
| - | - |
| `input.go`, `controllers.go` (profiles, sysfs scan) and the `controllers/*.toml` | `internal/evdev` (plus `evdevtest` for fake sysfs trees in tests) |
| `presence.go` | `internal/presence` (`Tracker`) |
| `gpu.go` (the reader) | `internal/gpu` (`Reader`, `System`) |
| `notify.go` | `internal/notify` (`Notifier`, `Desktop`) |

`display` keeps `followControllers`, `followGPU`, the bindings and the actions, in small `controllers.go` and `gpu.go` files. Non-test Go in `modules/display` went from 3,737 to 2,894 lines (843 out); the moved packages are about 1,170 lines with their new package headers and exported names. The moved packages import no module (checked with `go list -deps`).

**Compatibility:** nothing public changed: no action, event, route, config key, file format or controller profile. Go names changed (`display.Evdev` is `evdev.Evdev`, and so on), and `internal/daemon` is the only other user.

**Tests:** the tests named after those files mostly drive the display module through its rig, so only the pure ones moved: profile and scan tests, the evdev reader and hot-plug, the grab test, and the AMD sysfs reader. The rest stayed in `display`. Every test that existed still exists (one was renamed, `TestSystemGPUFromAMDSysfs` to `TestSystemFromAMDSysfs`). The full suite passes under `-race` and lint reports nothing. `presence` has no tests of its own; the display tests cover it through the rig, and a direct test of its idle and wake cycle with a fake clock would be a small addition.

**Not re-verified on core.** Behaviour should not change, since this was a move and a rename, but the controller, shortcut and presence checks have not been run on the real machine. If you want that done, I'd ask before driving core's screen.

**Revisit a real `input` module only if** a second consumer of input appears that is not display, or a second compositor makes keys come from evdev. The coupling that blocked it is in F15.

### W6. Test the real `display` and `apps` modules together (F13) — done

**Finding:** the contract was already named in `contract/` and enforced by the registry (`Requires`, declared scopes), and the missing-`apps` error was already clear, so steps 1 and 3 of the first draft needed no code. The gap was an integration test: `display`'s own tests use a fake apps module, which never needed `instance.closing` and declares its own copy of the scopes.

**Change:** `modules/display/apps_contract_test.go` runs the real apps module and the real display module on one core, faking only the compositor and the process runner. It checks:
- Registering both validates every scope and dependency the actions use (including `display.front`, declared only by apps).
- `app.start` through apps, then a window of the instance's unit turns up: display finds the instance through `contract.UnitName` and puts the window on `hostd:<instance>`.
- `window.focus` reaches that window.
- `window.close` on a window that stays open tells apps `instance.closing`, then `instance.stop`, and the runner is asked to stop the instance.

**Checked:** it passes under `-race` three times in a row. As a mutation test, dropping the `display.front` scope from apps made it fail with `scope "display.front" is not declared by any module`; the change was reverted.

**Not covered:** the path where the window closes by itself and display waits for the app to end (the existing display tests cover that against the fake).

### W7. Manifest-driven `hostctl` (F6) — re-scoped, built

**What reading the code showed (2026-10-14).** The plan assumed the hand-written commands could be deleted once the generator matched them. Most cannot, because they carry presentation the manifest does not describe:

| Command | What it does that a generated one would not |
| - | - |
| `volume`, `mute` | `volume +5 / -5 / 40` and no argument as a read; `mute on / off / toggle` mapped to JSON; both print the audio state line, not the raw result |
| `start`, `stop`, `focus` | Sentences from the result: "already running", "skipped: held by ...", "not switched: someone is using the screen" |
| `ps`, `controllers` | Computed cells (state plus exit code, `-` for no pid, "id" from vendor and product, "read by app" for raw devices) and `ps --all` as a query |
| `rules`, `scenes`, `apps` | The same, plus pause state and nested results |

The generator covered **actions only**; a module's **reads** had no commands at all. A hint format rich enough to replace the hand-written commands would only move their code into a schema, so they stay. The earlier "commands.go roughly halves" target is dropped.

**What was built** (optional, additive, ignored by the core and by older hostctl):
- `sdk.CLI` on `ActionSpec` and `ReadSpec`: `alias` (extra top-level names), and for reads `columns` (`title`, `path` with `a.b` and `a|b` fallback, `format` `list` or `time`, `max`) and `empty`. Validated by `Manifest.Validate` (alias shape and uniqueness, columns only on reads, at most 12, field paths, formats).
- `hostctl get <module> <read>` for every read a server declares: path parameters as arguments (`hostctl get apps app firefox`), `--param key=value` for the query, a table when the manifest gives columns, otherwise the server's JSON, and `--json` always the JSON. An empty or missing cell is `-`.
- Aliases: `hostctl windows` is now `hostctl get display windows`, declared by the display module. An alias never replaces a built-in command or an earlier module's name.
- `hostctl windows` was migrated (its hand-written command deleted). `controllers` and `ps` stay hand-written, with the reasons above.
- `displayZone` in hostctl, so time output can be pinned in tests; `TestMain` sets it to UTC.

**One visible change:** empty cells in `hostctl windows` now show `-` (the old output left them blank, with trailing spaces). Everything else in `windows`, and all of `controllers` and `ps`, is byte-identical (golden files in `cmd/hostctl/testdata/golden`, written before the change). `hostctl windows --json` now prints the server's JSON as sent, which has the same fields as before and any more the server adds.

**Checked:** golden output of `windows`, `controllers`, `ps` (and empty lists) against a fake server, stable under another time zone; `get` tests (table with all formats and a long cell, the alias, an alias that a built-in has not taking its name, `--json`, an empty list, a non-list answer, path and query parameters with escaping, usage errors exit 2, not-found exit 4, help text); an alias for an action; manifest validation tests; the Python example now declares columns and a real `hostd` and `hostctl` run it over both transports and print the table; the CLI reference is regenerated, and the module docs describe `cli`.

**Not done:** more built-in modules declaring columns for their reads (`apps`, `audio`, `media`, and the rest print JSON under `get` until they do). Adding `cli` to a built-in module is now a manifest change only.

### W8. Extmod stdio transport (F12) — done

**Change:** `transport = "stdio"` in `[external.<name>]` (default `"socket"`, unchanged). With stdio, hostd uses the program's standard input and output as the connection: no socket file, no accept and connect wait, no `.sock` to clean up. The JSON-RPC framing, handshake, manifest and restart rules are identical. The module's standard error goes to hostd's log, as standard output and error did before. In socket mode nothing changed.

**Differences worth knowing:**
- In stdio mode standard output is the protocol, so a module must not print anything else to it (the Python example now logs to standard error in both modes).
- Closing hostd's end closes the module's input, so a module that reads until end of file ends by itself on stop. One that does not is killed after 2 seconds, as before.
- Both transports set `HOSTD_MODULE_TRANSPORT`; `HOSTD_MODULE_SOCKET` is set only for `socket`.

**Checked:**
- Unit tests: the Python example over stdio does an action, an unknown-light refusal, child actions, a read, a subscription and a restart after it is killed, and leaves no files in the socket directory. A module that reads until end of file ends when its input closes. An unknown transport is refused at load.
- Config: an unknown `transport` fails with a clear message.
- Whole system: `cmd/hostctl/testdata/script/external_stdio.txtar` runs a real `hostd` and `hostctl` against the example over stdio.
- Docs: `sdk/PROTOCOL.md`, `docs/modules.md`, the example's README and the config comment.

**Open:** the default is still `socket`. Make `stdio` the default only after it has run on core for a while; changing it would be a behaviour change for existing configs.

### W9. Scenes can send actions of any module (F2) — done

**Syntax (as planned):** an optional `[[do]]` list in a scene file, each entry `action` plus optional `args`, run after the fixed `audio` / `apps` / `display` parts, in file order. Existing scene files load unchanged.

**Behaviour:**
- Steps run through `core.Do` like the fixed ones and appear in the scene's result as `do`, with `applied`, `skipped` (held by a higher-priority source) or `failed`.
- An action no loaded module handles fails its own step with the core's error; the other steps still run. The module may start later, so this is not a load-time error.
- At load only the shape is checked (an action type), and `scene.*` actions are refused so a scene cannot apply scenes (a scene that applies itself would only be stopped by the chain depth).
- **`[[do]]` steps are not idempotent.** The fixed parts are compared with the current state and send nothing when reached; an action of another module has no such comparison, so it is sent each time. The docs say so. The "read, compare and correct" form stays unbuilt (no module exposes its state that way).

**Checked:** parse tests (valid, missing or malformed action, `scene.apply`, unknown key); a scene with a stand-in external module sends its actions after the scene's own parts in file order with the arguments intact, an unhandled action fails only its step, and a second apply re-sends the actions but not the fixed parts. Not tested against the real Python `lights` example over the module protocol; the stand-in uses the same core path.

### W10. Install and add-ons (F11) — decision recorded, docs note done

**What exists:** `install.sh` and every `addon.sh` are already under `shellcheck -S warning` (`make lint-scripts`, run by CI before publishing). `test/install` (container) and `test/e2e` (VM, nightly) run the installer, add-ons, update and uninstall. The `.deb` is a separate, thin artifact.

**Decision: keep the bash installer and add-ons. Do not build `hostctl setup`.** There is no evidence of defects in them: the installer is tested end to end, and the real cost is size (909 + 793 lines). A Go rewrite means redoing that behaviour and tests, for readability. Reconsider only when add-on bugs keep coming from the bash code, which this plan would see in the issue history.

**Cheap improvements, in order of value:**
1. **Say what the `.deb` is for.** Add two sentences to `docs/install.md` ("`.deb`: the binaries and the user unit for a machine you set up yourself; `hostd-setup`: a whole fresh machine"), so nobody reads them as rivals.
2. **Split `install.sh` by function, not by language.** The file already has named functions (`addon_load`, `ensure_packages`, `fetch_release`, `verify_release`). If it is edited anyway, move the add-on functions into a sourced file. Do this only with a reason, and keep the single-file download that `hostd-setup update` relies on (the release-key check in CI, `ci.yml:91`, compares a key in the script).
3. **A spike on `addon.toml`** for one small add-on (`chromium` or `dolphin`, not `pterodactyl`): can a declarative file express its real steps (packages, files, units, an app file)? Write down what it couldn't express. If more than a third of the steps need escape hatches, the format isn't worth it.

**Done:** the decision is recorded above, and `docs/install.md` now says that the `.deb` and `install.sh` are different layers and that one machine uses one or the other. The `addon.toml` spike and the optional split of `install.sh` were not started (both allowed to stay unstarted).

## 4. Compatibility rules (for W7, W9 and anything that renames)

- A renamed action keeps its old name as a forwarding alias for one minor release, flagged deprecated in the manifest and logged once per use.
- Config files from the previous release must load unchanged. A test holds the old fixtures.
- Events a client may subscribe to (menu, hostctl watch, docs examples) are duplicated, not moved, for one release.
- `contract/` (HTTP contract) and `sdk/PROTOCOL.md` are updated in the same PR as the change.
- Every release that changes a namespace is deployed to core through the normal update path, with the rollback check (`previous` symlink) confirmed.

## 5. Order and dependencies

```
0.1 tag ──► W1 ──► W9
        ├─► W2
        ├─► W3 (measure; may close with no code)
        ├─► W4
        ├─► W6
        ├─► W5             (mechanical move; any time, one PR)
        ├─► W7             (incremental, any time)
        ├─► W8
        └─► W10 (decision recorded; docs note)
```

W1, W2, W4, W6, W8 and W10 are independent and can go in any order. Suggested first three: **W2, W1, W6.** They are cheap, local and high value. W9 needs W1. W7 can run alongside anything, a few commands per PR.

## 6. What this plan does not do

- No second `display.Backend` (Gamescope, Hyprland). The interface exists already (F16); a second implementation waits for a real need.
- No `input` module and no renamed input actions or events (see W5).
- No D-Bus rewrite of audio writes.
- No cgroup/PID rewrite of handoff tracking.
- No Go rewrite of the installer or `hostctl setup` (see W10).
- No new runner abstraction. `Options.Backends` already is one (F8). Revisit only when a new engine is added, and then fix whichever part of adding it was painful.

## 7. Success measures

Reported when each item lands, not forecast here:

| Item | Measure |
| - | - |
| W1 | Number of shipped scene/script files that become one-line rules (on core) |
| W2 | `pw-dump` forks per graph change on core: 1 before, 0 after (measured). |
| W3 | One `/proc` scan: 1.9 ms on core (217 processes), every 2 s while a handoff runs: about 0.1% of a core. Closed. |
| W4 | One name rule and one file implementation for secrets |
| W5 | `display` non-test lines: 3,737 to 2,894; moved files tracked as renames; all tests kept |
| W6 | Contract tests present, clear error when `apps` is missing |
| W7 | `commands.go` 1,089 to 1,045 lines (not the goal any more); external modules get reads and tables with no hostctl change; golden tests pass |
| W8 | `lights` example works on both transports |
| W10 | Decision recorded; `.deb` note in docs; `addon.toml` spike result (optional) |

## Second round: how the pieces fit (2026-10-14)

An architecture review one level up from W1–W10, at `9a9a81e`. Grade: **B+**.

| Area | Grade | Finding |
| - | - | - |
| Core pipeline | A | One path for every change: per-resource order, holds, cause chains, audit. |
| Module contract (sdk, contract, extmod) | A- | Manifest-driven routes, scopes, OpenAPI, CLI. External modules over socket or stdio. Four extension mechanisms with no shared story. |
| Module internals | B | apps was 4.4k lines with the runners inside. |
| Composition and config | C+ | The daemon built every module in one switch; `config.go` imported display; module settings were top-level keys; only `hostd.toml` could add modules. |
| Updates | C | Three ways to switch versions, and only the API upload kept the state for a rollback. `hostd-setup update` and the timer did not. |
| Efficiency | B+ | No forks or polls in steady state after W2, except the measured handoff scan. |

What was done:

- **A. One way to switch versions** (`a30fb09`): `installVersion` stages, copies the state database (`store.BackupFile`, `VACUUM INTO` from the installing process, no migration) and `hostd.toml`, writes `backup-for` and switches, for the API upload and for `hostd -install`. The installer runs `hostd -install -lib DIR -no-restart` instead of switching symlinks in bash. Checked by unit tests and `make test-install` (new check: the installer's update leaves the rollback data).
- **B. Modules own their settings** (`4a44889`): a table of built-in modules (`internal/daemon/modules.go`), with `apps.Config` (`[apps]`) and `display.Config` (`[display]`) decoded and checked by their modules. `config.go` no longer imports a module. The old `idle_after`, `browser`, `gamescope` and `[input]` work for one release, with a log line. Core has no `hostd.toml`, so nothing there needs moving.
- **C. Drop-ins** (`0813421`): `~/.config/hostd/hostd.d/*.toml` merged into `hostd.toml`, holding module sections and `[external.NAME]`. A setting in two files is an error naming both. The module docs open with the four ways to extend hostd and how an add-on ships an external module as a drop-in.
- **D. The runners move out of apps** (first narrowed, then done in full). The first attempt stopped at the systemd client (`internal/systemd`), because the runners seemed to need the apps package and its test rig, meaning an import cycle and a rewrite of about 2,300 lines of tests. Put the shared types in a leaf package and both problems go away: `modules/apps/spec` (`App`, `Instance`, `State`, `Ended`, `Backend`, `Passer`, the runner and surface names) depends on nothing of apps. The runners now live in `modules/apps/runner/units` (exec, flatpak, url and process apps as systemd units, with handoff tracking) and `modules/apps/runner/podman` (docker and compose through the Podman API). Each has its fakes in a test-helper package (`unitstest`, `podmantest`) that the apps module's tests share, and the runner tests moved next to their runners. apps keeps aliases (`apps.App` and friends), so nothing outside changed but the daemon's wiring. modules/apps went from 4,410 to 2,532 lines (spec 249, units 657, podman 741); every test is kept. A new runner is a package implementing `spec.Backend` plus one line in the daemon's table, with no change to apps.

## Third round: the installer in Go (W11, started 2026-10-15)

Asked for after round two ("rewriting code is fine as long as it's better and more efficient"). Goals: one typed, unit-tested implementation of the machine setup; signatures checked with Go's ed25519 (no openssl); the files it installs embedded in the signed binary; quiet output from structured steps instead of an awk filter; add-ons as data.

Design:
- `deploy/` becomes a Go package (`github.com/davitizhgenti/hostd/deploy`) that embeds `files/` and `addons/`. `cmd/hostd-setup` is the binary: `install`, `update [NAME...] [--all]`, `auto-update`, `uninstall [--purge] [--all]`, `addons`, `add`, `remove`, with today's flags (`--gpu`, `--user`, `--from`, `--source`, `-v`).
- Steps run against a `Root` (`/`, or a temp dir in tests) and a command `Runner` (the real one, or a fake that records), so each step is unit-tested; the container install test and the VM test check the real thing.
- `deploy/install.sh` shrinks to a bootstrap: fetch and verify the release's `hostd-setup` binary (or take it from `--from`), then run it. An old installed `hostd-setup update` downloads the release's installer tarball and runs its `install.sh`, which is now this bootstrap, so old machines move over by themselves.
- Add-ons: controllers, chromium, compose, dolphin, flatpak and steam become `addon.toml` (packages, files, units, app files, Flatpak apps, requirements). nvidia and pterodactyl keep their command sequences as a `hook` script the engine runs with the same helpers, because they are chains of external commands (NVIDIA's .run installer; PHP, MariaDB, composer, artisan) and nvidia can only be verified on core.
- Releases carry `hostd-setup-linux-{amd64,arm64}` in the signed SHA256SUMS, beside the tarball older installers fetch.

Stages: (1) the engine: output, runner, files, release verification; (2) the base install steps and install/update/uninstall; (3) the bootstrap, Makefile and CI; (4) add-ons; (5) container test, VM test, then core.

**Done (2026-10-15), stages 1–5:** `deploy/*.go` (engine, install, update, auto-update, uninstall, add-ons; unit-tested with a fake runner and a temp root) and `cmd/hostd-setup`. `deploy/install.sh` is a 95-line bootstrap. Six add-ons are now only an `addon.toml`; steam keeps a small `hook.sh` (gamescope, VRAM cap), nvidia and pterodactyl keep theirs. An update runs the add-ons only when the build's add-on files changed (a stamp in `/etc/hostd/addons.applied`), or when named, or with `--all`. Checked: `go test -race ./...`, golangci-lint, shellcheck, `make test-install` (90 checks) and `make test-e2e` (35 checks, including `sudo hostd-setup update` in the VM). Core moves over at its next update: the installed bash `hostd-setup update` fetches the build's `install.sh`, now the bootstrap, which runs the Go installer.

Core moved over at its first update (2026-10-15, edge-5252e4f): the bash `hostd-setup` fetched the bootstrap, which ran the Go installer. Checked on core: `hostd-setup` is the binary and the old shell files are gone, the previous version keeps its state backup, the 8 add-ons are recorded with their stamp, the NVIDIA driver (580) and the Pterodactyl panel still work, and no unit failed.
