# The CLI

<!-- Generated from hostctl's commands by go test ./cmd/hostctl -run CLIDoc -update. -->

hostctl talks to a hostd machine: over its unix socket on the machine itself, or over the network with a token (`hostctl login <machine>:7300`). `HOSTD_URL` and `HOSTD_TOKEN` override the saved login.

**Module commands** come from the machine: every action becomes `hostctl <namespace> <action> [args]`, such as `hostctl audio volume set 40`, `hostctl deploy run blog` or `hostctl scene apply movie`. An action's required arguments are positional, in order, and every argument also has a `--flag`. Arguments marked secret (`hostctl secret set <name>`) are read from standard input. `hostctl --help` on a connected machine lists them all, including those of external modules.

**Reads** (what a module reports) come from the machine too: `hostctl get <module> <read>`, such as `hostctl get display windows`. A read that returns a list prints as a table when its module says how, and as JSON otherwise, or with `--json`. A read with `{parameters}` in its path takes them as arguments (`hostctl get apps app firefox`), and query parameters are given as `--param key=value`. A module can also ask for a short top-level name for one of its commands: `hostctl windows` is `hostctl get display windows`. A name that a built-in command or another module has is not taken.

Global flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--json` |  | print JSON instead of text |
| `--url` |  | hostd address, overriding the config (unix:///path or host:port) |

Exit codes, for scripts:

| Code | Meaning |
| --- | --- |
| 0 | done (a skipped action included: skipping is not an error) |
| 1 | anything else: cannot connect, an internal error |
| 2 | hostctl was called wrong |
| 3 | `unauthorized` or `forbidden` |
| 4 | `not_found` |
| 5 | `instance_not_running` |
| 6 | `precondition_failed` |
| 7 | `timeout` |
| 8 | `module_unavailable` |
| 9 | `loop_detected` |
| 10 | `invalid_args` |

## hostctl action

Send any action by type, with arguments as JSON

```
hostctl action <type> [json-args] [flags]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--expect-version` | `-1` | refuse if the resource is not at this version |

## hostctl apps

List the app catalog, or show one app

```
hostctl apps [id] [flags]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--all` |  | include hidden apps |

## hostctl controllers

List connected game controllers and the buttons hostd knows on them

```
hostctl controllers
```

List connected game controllers: their profile (which family hostd takes
them for) and the buttons that can be bound in hostd.toml ([display.buttons]).
Profiles are built in, and ~/.config/hostd/controllers/*.toml adds more.
A raw device (a DolphinBar) is read only by its app.

## hostctl events

Stream live events (Ctrl-C to stop)

```
hostctl events [flags]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--count` | `0` | exit after this many events |
| `--recent` |  | first show the recent events the server still has (up to 1,000) |
| `--type` | `*` | only these event types, e.g. 'instance.*' |

## hostctl focus

Bring an instance's window to the front

```
hostctl focus <instance> [flags]
```

Bring an instance's window to the front. While someone is using the
screen it stays put and they get a notice instead; --front switches anyway
(needs the display.front scope).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--front` |  | switch even while someone is using the screen |

## hostctl log

Show recent actions and their sources (the audit trail)

```
hostctl log [flags]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--action` |  | only this action ID |
| `--limit` | `50` | how many entries |
| `--type` |  | only these types, e.g. 'audio.*' |

## hostctl login

Save the address and token of a hostd machine

```
hostctl login <address> [flags]
```

Save the address and token of a hostd machine in ~/.config/hostctl.

The address is host:port (port 7300) or unix:///path/to/hostd.sock. The token
is read from --token-file, or from standard input: paste the admin token
that hostd printed on its first start, or one made with hostctl token create.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--token-file` |  | read the token from this file |

## hostctl mute

Mute or unmute (toggle by default)

```
hostctl mute [on | off | toggle]
```

## hostctl ps

List running instances

```
hostctl ps [flags]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--all` |  | also show instances that ended recently |

## hostctl rules

List the rules, when they fired and whether they are paused

```
hostctl rules
```

## hostctl scenes

List the scenes (hostctl scene apply <id> reaches one)

```
hostctl scenes
```

## hostctl start

Start an app (if it already runs: focus it, start another copy, or restart it, per its settings)

```
hostctl start <app> [flags]
```

Start an app. While someone is using the screen, an app started from
elsewhere opens in the background and a notice says it is ready; --front
brings it to the front anyway (needs the display.front scope). --action
starts one of the app's actions (see hostctl apps <app>) as a new instance.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--action` |  | start one of the app's actions, as a new instance |
| `--front` |  | open in front even while someone is using the screen |

## hostctl stop

Stop a running instance (see hostctl ps)

```
hostctl stop <instance>
```

## hostctl token

Create, list and revoke API tokens

```
hostctl token
```

## hostctl token create

Create a token; its secret is shown only once

```
hostctl token create <name> [flags]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--kind` | `manual` | manual (a person's device), automation (scripts) or local (the on-screen menu) |
| `--scopes` |  | scopes, comma-separated (required) |
| `--ttl` |  | lifetime such as 1h; default never expires |

## hostctl token list

List tokens

```
hostctl token list
```

## hostctl token revoke

Revoke a token

```
hostctl token revoke <name or id>
```

## hostctl update

Update hostd on the machine

```
hostctl update
```

## hostctl update push

Install a hostd build on the machine and restart into it (rolled back if it does not start)

```
hostctl update push [binary] [flags]
```

Install a hostd build on the machine and restart into it.

Without a binary, builds hostd from the source checkout you are in, for the
machine's architecture (needs Go). The machine keeps the previous version:
if the new one does not start, systemd switches back to it on its own.

--ssh user@machine copies the binary over SSH and installs it there, for
when the running hostd is too broken to take an upload.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--ssh` |  | install over SSH instead of the API, e.g. screen@192.168.1.20 |

## hostctl version

Show the versions of hostctl and the connected hostd

```
hostctl version
```

## hostctl volume

Show the volume, or set it (0-150) or change it (+5, -5)

```
hostctl volume [percent | +n | -n]
```

## hostctl wait-quiet

Wait until no deploy or scene is in progress (exit 1 if one still is after --max)

```
hostctl wait-quiet [flags]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--every` | `2s` | how often to look |
| `--idle` |  | also wait until nobody is using the screen |
| `--max` | `5m0s` | give up after this long |
| `--types` | `deploy.*,scene.*` | action types to wait for (comma-separated; a namespace with .*) |
