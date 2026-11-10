# Extending hostd: which way

hostd has a few nouns, and everything you add is one of them.

| Noun | What it is | Where it lives |
| --- | --- | --- |
| **Resource** | Something you declare: an app, a scene, a rule, a script, a module | a TOML file in `~/.config/hostd/<folder>/` ([resources](resources.md)) |
| **Instance** | A running app, a command or a container | started with the verbs below |
| **Action** | A change, sent through one pipeline (locks, priority, permissions, audit) | by a module, by a rule, by you |
| **Event** | A fact: something changed | emitted by a module, followed by rules and clients |

## The verbs

The same words everywhere: the CLI, the API, the Python client, the menu, scenes and rules.

- Resources: `hostctl get`, `apply -f`, `edit`, `delete` (API: `GET`, `PUT`, `DELETE /v1/<folder>/<name>`).
- Instances: `open`, `switch`, `close`, `stop`, `run` ([the API](api.md#opening-switching-closing-stopping)).

## Which way do I add something?

| I want to... | Use | How much work |
| --- | --- | --- |
| Put a program on the menu, or run a service | an **app** file | one TOML file ([apps](apps.md)) |
| Set the volume, apps and screens in one go | a **scene** | one file; its sections belong to the modules that plan them |
| Do something when something happens | a **rule** (`on`, `if`, `do`) | one file ([automation](automation.md)) |
| Run real logic when a rule fires, or by hand | a **script** | one file naming a command or inline shell, and its scopes |
| Control hardware or a service hostd does not know | an **external module**, in any language | a program speaking JSON-RPC, plus `modules/<name>.toml` ([writing a module](modules.md)) |
| The same, inside hostd, with typed actions | a **built-in module** in Go with `sdk/kit` | a package with one `Builtin` value; start from `modules/demo` |
| A new kind of declared thing | a **kind** in a module (`kit.Kind`) | a Go type for its spec and an Apply function; it gets the folder, API routes, CLI and status for free |
| A module to start from | `hostctl new module NAME --python` (or `--go`) | writes a working module, with a test or a config file |
| A new section for scenes (`[lights]`) | a **facet** (`kit.Facet`) | a Go type for the section and a plan function that returns the steps still needed |
| Another kind of app runner | a **runner** (`spec.Backend`) | a package that passes `spec/conformance` |
| Install software in the user's home (a Flatpak, a helper, hostd resources) | a **user add-on** (`root = false`) | a directory with `addon.toml` and `resources = [...]` ([add-ons](install.md)) |
| Install something that needs root (a driver, a package, a udev rule) | a **root add-on**: `root = true`, or implied by `packages`, `kernel_modules`, `on_change`, files outside `~` or a hook | the same `addon.toml`; only an admin installs it: `sudo hostd-admin add NAME` |
| Control hostd from my own program | a **client**: HTTP, or `clients/python/hostd.py` | `h.audio.volume.set(percent=40)`; any module's actions work with no list in the client |

## Rules of the interface

1. Things are resources or instances. A module's other actions are verbs on something it owns, named `<noun>.<verb>`.
2. Every action names its target; routes follow from that: `POST /v1/<folder>/{name}/<verb>`.
3. Events are `<noun>.<past tense>`.
4. A module declares settings, kinds, facets, reads and events from Go types; the manifest is generated, and so are the routes, the CLI, the OpenAPI document and the reference pages.
5. One owner per verb. Other modules take part through facets (a scene section), kinds, or the actions they send: never by reading another module's files or private JSON (`contract` holds the shared names and types).
