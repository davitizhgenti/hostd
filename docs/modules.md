# Writing a module

A module owns a namespace (`lights.*`). It declares its actions, events, reads and scopes in a manifest, and carries out its actions when the core hands them over. Everything else is the core's work: validation, permissions, ordering per resource, priority holds and the audit trail.

## In any language: an external module

An external module is a program that hostd starts and talks to over a unix socket (or, with `transport = "stdio"`, over its standard input and output), with JSON-RPC 2.0 and one message per line. It needs no library: [`examples/python-module/lights.py`](https://github.com/davitizhgenti/hostd/tree/main/examples/python-module) is a complete module in Python's standard library.

Configure it in `hostd.toml`:

```toml
[external.lights]
command = ["python3", "/home/screen/hostd/modules/lights.py"]
```

Its actions then become commands and routes like the built-in ones:

```sh
hostctl lights set porch --brightness 40
curl ... -d '{"brightness": 40}' http://x/v1/lights/porch
```

A module that fails to start, or that claims a namespace another module owns, is left out, and hostd runs without it. One that exits is started again, after a wait that grows from 1 second to a minute. Meanwhile its actions answer `module_unavailable`.

The protocol, message by message: [the module protocol](protocol.md).

## In Go: a built-in module

A built-in module implements `sdk.Module` (`Manifest`, `Start`, `Validate`, `Handle`, `Stop`), and optionally `sdk.Reader`, `sdk.StateReporter` and `sdk.Hooker`. It is compiled into hostd and listed in `internal/daemon`. Modules never import each other. When one uses another's actions, events or reads, it declares that in its manifest (`requires`), and both take the names and data shapes from the `contract` package.

## The manifest

```json
{
  "name": "lights", "version": "0.1.0",
  "owns": ["lights.*"],
  "scopes": [{"name": "lights", "description": "Turn the lights up and down"}],
  "actions": [{
    "type": "lights.set",
    "schema": {"type": "object", "properties": {"id": {"type": "string"},
               "brightness": {"type": "integer", "minimum": 0, "maximum": 100}},
               "required": ["id", "brightness"]},
    "keys": ["light:{id}"],
    "scope": "lights",
    "route": {"method": "POST", "path": "/v1/lights/{id}"}
  }],
  "events": [{"type": "lights.changed"}],
  "reads": [{"name": "lights", "path": "/v1/lights"}]
}
```

- `keys` are the resources an action locks, as templates over its arguments. Actions on the same key run one at a time. A person's change holds a key against automation.
- `scope` is what a token needs. `arg_scopes` adds a scope that is needed only when an argument is given, such as `{"front": "display.front"}`.
- `secret` names arguments whose values are never recorded. hostctl reads them from standard input.
- `slow` actions answer `202` at once. Their result follows as an event.
- `hooks` are POST routes under `/v1/hooks/` with no token. The module checks each request itself, by its signature.
- `cli` on an action or a read (optional) says how hostctl shows it. Without it hostctl still has a command for every action (`hostctl lights set porch 40`) and for every read (`hostctl get lights lights`, which prints the JSON). With it:

  ```json
  "reads": [{"name": "lights", "path": "/v1/lights",
             "cli": {"alias": ["lights-list"], "empty": "no lights",
                     "columns": [{"title": "LIGHT", "path": "id"},
                                 {"title": "BRIGHTNESS", "path": "brightness"},
                                 {"title": "SINCE", "path": "changed", "format": "time"}]}}]
  ```

  `columns` turn a read that returns a list of objects into a table: each has a `title` and a `path` to the field of every row (`a.b` for a nested one, `a|b` for the first of two that is not empty), an optional `format` (`list` joins a list with commas, `time` prints an RFC 3339 time as the local time of day) and an optional `max` length. A missing or empty value is shown as `-`. `empty` is printed instead of a table with no rows. `alias` gives the command a short top-level name (`hostctl lights-list`); a name that a built-in command or an earlier module has is not used. Only `alias` applies to an action. A read with `{parameters}` in its path takes them as arguments (`hostctl get lights light porch`). hostctl ignores `cli` entries it does not know, and the core ignores `cli` altogether, so a module can add hints without caring which hostctl runs.
