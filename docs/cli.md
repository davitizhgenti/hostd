# The CLI

<!-- Generated from hostctl's commands by go test ./cmd/hostctl -run CLIDoc -update. -->

hostctl talks to a hostd machine: over its unix socket on the machine itself, or over the network with a token (`hostctl login <machine>:7300`). `HOSTD_URL` and `HOSTD_TOKEN` override the saved login.

**Module commands** come from the machine: every action becomes `hostctl <namespace> <action> [args]`, such as `hostctl audio volume set 40`, `hostctl deploy run blog` or `hostctl scene apply movie`. An action's required arguments are positional, in order, and every argument also has a `--flag`. Arguments marked secret (`hostctl secret set <name>`) are read from standard input. `hostctl --help` on a connected machine lists them all, including those of external modules. hostd's own features are a module too, named hostd: `hostctl token create phone --scopes read,apps`, `hostctl token list`, `hostctl log`, `hostctl tls` and `hostctl wait-quiet` come from it like the others.

**Reads** (what a module reports) come from the machine too: `hostctl get <module> <read>`, such as `hostctl get display windows`. A read that returns a list prints as a table when its module says how, and as JSON otherwise, or with `--json`. A read with `{parameters}` in its path takes them as arguments (`hostctl get apps app firefox`), and query parameters are given as `--param key=value`. A read named after its module is also the module's own command: `hostctl get audio` is `hostctl get audio audio`. A module can also ask for a short top-level name for one of its commands: `hostctl windows` is `hostctl get display windows`. A name that a built-in command or another module has is not taken.

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

## hostctl login

Save the address and token of a hostd machine

```
hostctl login <address> [flags]
```

Save the address and token of a hostd machine in ~/.config/hostctl.

The address is host or host:port (HTTPS, port 7300) or unix:///path/to/hostd.sock.
Over HTTPS, the machine's certificate authority is shown first: compare its
fingerprint with what hostctl tls prints on the machine (ssh <machine>
hostctl tls), then confirm. hostctl trusts that CA, and only it, from then
on. --ca-fingerprint gives it instead, for scripts.

The token is read from --token-file, or from standard input: paste the
admin token that hostd printed on its first start, or one made with
hostctl token create.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--ca-fingerprint` |  | the machine's CA fingerprint (hostctl tls on the machine), instead of confirming it |
| `--token-file` |  | read the token from this file |

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
