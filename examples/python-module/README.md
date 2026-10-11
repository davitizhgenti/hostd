# An external module in Python

`lights.py` is a complete hostd module, written in Python with only the standard library. It manages two pretend lights. It shows each part of the module protocol (`sdk/PROTOCOL.md`):

- **Actions:** `lights.set` and `lights.dim_all`. `lights.dim_all` sends one `lights.set` per light as child actions.
- **A refusal:** `validate` answers `not_found` for an unknown light.
- **Reads:** a read (`/v1/lights`) and the module's state.
- **Events:** each `lights.changed` event is tied to the action that caused it.
- **A subscription:** the module counts the apps hostd starts.

To try it, add this to `~/.config/hostd/hostd.toml` and restart hostd:

```toml
[external.lights]
command = ["python3", "/path/to/examples/python-module/lights.py"]
```

Then:

```sh
hostctl lights set porch --brightness 40
hostctl lights dim_all
hostctl log
```

Its actions go through the same pipeline as the built-in modules: a token needs the `lights` scope, a manual change holds a light against rules, and everything is in the audit trail.
