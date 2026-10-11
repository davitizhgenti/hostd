# hostd module protocol, version 1

An external module is a separate program that hostd starts and talks to over a unix socket. It can be written in any language. To hostd it is a module like the built-in ones: it owns namespaces, declares actions, events, reads and scopes in a manifest, and every change still goes through the core's action pipeline, with its locks, priority holds, permissions and audit trail. The calls are the Go `sdk.Module` and `sdk.Core` interfaces, at `sdk.CoreVersion` 1.

`examples/python-module` is a complete module that uses only Python's standard library.

## Configuration and start

A module is configured in `~/.config/hostd/hostd.toml`:

```toml
[external.lights]
command = ["python3", "/home/screen/hostd/modules/lights.py"]
env     = { LIGHTS_BRIDGE = "192.168.1.20" }   # optional
```

hostd starts `command` with these environment variables:

| Variable | Value |
| --- | --- |
| `HOSTD_MODULE_SOCKET` | The unix socket to connect to |
| `HOSTD_MODULE_NAME` | The configured name; the manifest's `name` must equal it |
| `HOSTD_PROTOCOL` | `1` |

The module connects within 10 seconds. hostd writes what the module prints on standard output and standard error to its own log, tagged with the module's name.

## Framing

Messages are [JSON-RPC 2.0](https://www.jsonrpc.org/specification). Each message is one JSON object on one line, ending in `\n`. Both sides send requests, both sides answer them, and requests may overlap: hostd can send `handle` for one action while an earlier one is still running. Answer each request with the same `id`. A request without an `id` is a notification and gets no answer.

### Errors

A failed request is answered with a JSON-RPC error. Its `data.code` is one of hostd's stable codes: `invalid_args`, `not_found`, `instance_not_running`, `precondition_failed`, `forbidden`, `timeout`, `module_unavailable`, `loop_detected`, `internal`. These codes reach API clients and scripts as they are.

```json
{"jsonrpc":"2.0","id":7,"error":{"code":-32000,"message":"no light \"porch\"","data":{"code":"not_found"}}}
```

An error without `data.code` counts as `internal`.

## hostd → module

| Method | Params | Result | When |
| --- | --- | --- | --- |
| `hostd.handshake` | `{"protocol": 1, "core_version": 1, "name": "lights"}` | `{"protocol": 1}` | First, right after the module connects. A module that cannot speak protocol 1 answers an error. |
| `manifest` | `{}` | A manifest (as `GET /v1/manifests` shows one) | After the handshake |
| `start` | `{}` | `{}` | Once the modules it requires have started. Subscribe here. |
| `validate` | `{"action": Action}` | `{}`, or an error such as `not_found` | When an action reaches the front of its queue, with its keys held. Optional: answer `{}`. |
| `handle` | `{"action": Action}` | A Result, or an error | To carry out an action. The action's `timeout` from the manifest applies. |
| `read` | `{"name": "lights", "params": {"id": "porch"}}` | Any JSON | For a read declared in the manifest |
| `state` | `{}` | Any JSON | For `GET /v1/state/<module>` |
| `hook` | `{"name": "...", "params": {...}, "header": {...}, "body": "<base64>"}` | Any JSON | For a hook declared in the manifest. The module authenticates the request itself. |
| `stop` | `{}` | `{}` | At shutdown. After the answer, or 10 seconds, hostd ends the process. |

An Action is `{"id", "type", "args", "source", "parent", "cause"}`. A Result is `{"status": "applied", "data": ...}`; `status` may be left out, and `data` is optional.

## module → hostd

| Method | Params | Result |
| --- | --- | --- |
| `emit` | `{"event": {"type", "data", "action", "resource"}}` | `{}` |
| `do` | `{"action": {"type", "args"}, "within": "<action id>"}` | A Result, or an error |
| `subscribe` | `{"filter": "instance.*"}` | `{"subscription": "s1"}` |
| `unsubscribe` | `{"subscription": "s1"}` | `{}` |
| `read` | `{"module": "apps", "read": "instances", "params": {}}` | Any JSON |
| `handles` | `{"type": "window.focus"}` | `{"handles": true}` |

- `emit`: the type must be in a namespace the module owns and declared in its manifest. To tie an event to the action being handled, set `action` to that action's ID. Set `resource` for a change that happened outside hostd, such as a light switched by hand: hostd bumps that resource's version and records the change in the audit trail.
- `do`: sends an action through the full pipeline. Set `within` to the ID of the action being handled to make the new action its child. A child may reuse the keys its parent holds, and keeps the parent's source and priority. Without `within`, the action's source is `automation` with the module's name.
- `subscribe`: events then arrive as notifications, `{"jsonrpc":"2.0","method":"event","params":{"subscription":"s1","event":{...}}}`. An event of type `bus.lagged` means some events were lost; read the state again.

## Lifetime

- A module that fails its handshake or its manifest is left out, and hostd runs without it.
- A module that exits after it started is started again, 1 s after the exit, then 2, 4, up to 60 s between tries. Its manifest must not change. While it is down, its actions fail with `module_unavailable`. A restarted module subscribes again in `start`.
- The manifest must not change while hostd runs. To change it, restart hostd.
