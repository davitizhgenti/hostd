# Automation

Scenes describe a state, rules decide when to reach it, and scripts handle real logic. All three only send actions, so permissions, priority holds and the audit trail apply to them as to anything else. A rule's work runs at `automation` priority, so it never undoes what a person changed in the last 3 minutes.

## Scenes

A scene is a file in `~/.config/hostd/scenes/` (the file name is its name). Every part of it is optional.

```toml
# scenes/movie.toml
name = "Movie night"

[audio]
output = "hdmi"          # hdmi, speakers, headphones, bt-<device>, usb-<device> (hostctl get audio)
volume = 40
muted  = false

[apps]
stop    = ["game"]       # every instance
running = ["jellyfin-tv"]
focus   = "jellyfin-tv"  # in front (started if needed)

[display]
power = "on"

# Actions of any module, built-in or external, sent last, in this order
[[do]]
action = "lights.scene.set"
args   = { name = "movie_mode" }
```

Each section (`[audio]`, `[apps]`, `[display]`) belongs to the module that plans it; automation only collects what they ask for. A scene file whose section its module refuses is not used, with the module's reason (`hostctl scenes` shows it), and so is one with a section no module plans, naming the sections there are. A module of your own can add a section the same way (see [writing a module](modules.md)).

An output's name stays the same while it is plugged in or out, so a scene can name it. A desktop board's analog card is one output, `speakers`, whichever of its jacks is in use; the `PORT` column of `hostctl get audio` says which one (`Headphones`, `Line Out`).

The parts above (`audio`, `apps`, `display`) are compared with the current state, and only what differs is sent. A `[[do]]` action is sent every time the scene is applied: a scene cannot know what another module's action changes, so it does not try to tell whether it is needed. Use `[[do]]` for actions that are safe to repeat. An action that no loaded module handles fails its own step, and the rest still run. A scene cannot apply another scene.

```sh
hostctl scenes
hostctl scene apply movie
```

Applying a scene reads the current state and sends only the actions still needed, in a fixed order: stop apps, audio output, volume, mute, start apps, focus, display power. Applied a second time, it sends nothing. The result lists each step as `applied`, `skipped` (held by a higher-priority change) or `failed`. A failed step does not stop the later ones, and nothing is undone.

## Rules

A rule is a file in `~/.config/hostd/rules/`; its name is the file's (`rules/gaming-audio.toml` is the rule `gaming-audio`). Rules are read again whenever a file changes:

```toml
# rules/gaming-audio.toml
on   = "instance.started"           # an event type, a namespace (instance.*) or *
if   = { app = "dota*" }
do   = { scene = "gaming" }
```

```toml
# rules/screen-off-when-idle.toml
on       = "display.idle"
if       = { minutes = 30, between = "22:00-07:00" }
do       = { scene = "screen-off" }
cooldown = "10m"
```

```toml
# rules/evening.toml
on = "display.active"
do = { script = "evening.sh" }
```

```toml
# rules/quiet-when-nobody-is-there.toml
on = "display.idle"
do = { action = "audio.mute.set", args = { muted = true } }
```

A rule does one of three things: applies a `scene`, runs a `script`, or sends one `action` with its `args` (any action a loaded module handles, built-in or external; [the API description](api.md) lists them). An action needs no scene or script file. It runs as the rule, at automation priority, with the event as its cause, so loop limits, holds and the audit trail apply as they do to a scene. The core checks the action's name and arguments when the rule fires, so a rule for a module that starts later is fine: until then it fails, and `hostctl rules` shows the failure.

| Condition | Holds when |
| --- | --- |
| `app` | The event's app matches the glob (an instance event's app, or the app of the event's instance) |
| `between` | The local time is in the range, which may cross midnight |
| `minutes` | Nobody has used the screen for that long. The rule waits for it, and fires then if the screen is still unused. |

`hostctl rules` shows each rule's last firing and result. `hostctl rule pause <name>` and `hostctl rule resume <name>` stop and restart one. A rule file that is wrong is reported there (the PROBLEM column), and the version before it, if there was one, stays in use. Rules and scenes are [resources](resources.md): `hostctl apply -f`, `edit` and `delete` work on them as on every kind.

**Loops.** A rule's work carries the event as its cause. When rules set each other off more than 5 steps deep, the core stops the chain with `loop_detected` and pauses the rule for 10 minutes. A rule that fires more than 10 times in a minute is paused too.

## Scripts

A script is a file in `~/.config/hostd/scripts/` that says what to run and which scopes it needs. Its name is the file's (`scripts/evening.toml` is the script `evening`, which a rule names in `do = { script = "evening" }`). It is either a command, started in the scripts folder, or shell commands written in place:

```toml
# scripts/evening.toml
command = ["python3", "evening.py"]   # or ["./evening.sh"]; a file beside this one
scopes  = ["audio", "display"]
timeout = "2m"                        # default 60s
```

```toml
# scripts/quiet.toml
run    = """
hostctl audio volume set 40
hostctl start jellyfin-tv
"""
scopes = ["audio", "apps"]
```

`command` and `run` are two ways to say the same thing: give one. `run` is a shell script (`sh`). `hostctl scripts` lists them, and `hostctl script run <name>` runs one now, as a rule would, with an empty event, which is how to try one.

Each run gets a token of its own, with only those scopes; `admin` is never allowed (the file is refused). The token expires when the run ends. The run has these environment variables:

| Variable | Value |
| --- | --- |
| `HOSTD_URL` | The API's unix socket (hostctl uses it) |
| `HOSTD_TOKEN` | The run's token |
| `HOSTD_EVENT` | The event that set the rule off, as JSON |
| `HOSTD_RULE` | The rule's name |

A script that runs out of time is stopped, together with every process it started. hostd's log records each run's exit code, its output and the actions it sent.
