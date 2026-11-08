# Resources

Most of what you tell hostd is a file: an app, a scene, a rule. hostd calls such a thing a **resource**. Every kind of resource works the same way, so what you learn for one holds for all:

- It is a TOML file in a folder named after the kind, under `~/.config/hostd/`: `rules/night.toml` is the rule `night`, `scenes/movie.toml` the scene `movie`. The name is the file name without `.toml`: lowercase letters, digits, `.`, `_` and `-`.
- hostd reads the folders when it starts and again whenever a file changes. You never restart it, and you never tell it to rescan.
- A file that is wrong is **not used**, and the version before it, if there was one, stays in use. A typo in an editor takes nothing down. The problem is shown on the resource (`hostctl get <kind>`), in the log, and as a `resource.rejected` event.
- The same commands, the same API routes, for every kind.

## The commands

```sh
hostctl get rules                  # every rule, with its state and any problem
hostctl get rules night            # one: its file, then its status as comments
hostctl apply -f night.toml --kind rule    # create or replace
hostctl apply -f ~/config-repo             # a folder of kind folders: all of them
hostctl apply -f night.toml --kind rule --check   # only say whether it would be accepted
hostctl edit rule night            # your $EDITOR; applied when you save and quit
hostctl delete rule night
```

`hostctl rules` is short for `hostctl get rules`. A kind's own actions are commands too: `hostctl scene apply movie`, `hostctl rule pause night`.

`apply` takes the kind from the folder a file is in (`rules/night.toml`) or from `--kind`, and the name from the file name or `--name`. A file the owner rejects is reported and not written, the others are still applied, and `hostctl` exits with an error. Standard input works too (`-f -`, with `--kind` and `--name`), and a JSON object is accepted instead of TOML.

## The API

For a kind with the plural `rules`:

| Route | Does |
| --- | --- |
| `GET /v1/rules` | Every rule, with its status |
| `GET /v1/rules/{name}` | One: its spec as JSON, its status, its generation, and the error if its file is not in use |
| `GET /v1/rules/{name}/file` | The file as written |
| `PUT /v1/rules/{name}` | Create or replace; the body is TOML or a JSON object; `?check=1` only checks |
| `DELETE /v1/rules/{name}` | Delete |
| `POST /v1/rules/{name}/{verb}` | One of the kind's verbs (`pause`, `resume`), an action with the name filled in |

Reading needs the `read` scope. Writing needs `admin`, unless the kind says otherwise. A verb needs the scope of the action it sends. [The API description](api.md) lists every kind with its schema.

## The status

A resource's status says whether it is in use, in the form Kubernetes uses: a list of **conditions**, each with a type (`Ready`, `Paused`...), a status (`True`, `False`, `Unknown`), and a reason. A kind may add its own data: a rule reports how often it fired and its last result. `hostctl get <kind>` shows what matters in a table.

## The kinds

| Kind | Folder | Owner | What |
| --- | --- | --- | --- |
| `scene` | `scenes/` | automation | [A state to reach](automation.md#scenes) |
| `rule` | `rules/` | automation | [When something happens, do something](automation.md#rules) |
| `preset` | `presets/` | demo (with `-demo`) | A saved lamp setting, the example of a kind |

Modules add kinds ([writing a module](modules.md)); apps, scripts and external modules follow in later versions.

## Moving from before 0.2

Until 0.2 all rules were in one file, `rules.toml`. hostd now refuses to start with that file, and says to run `hostd -migrate`: it writes one file per rule in `rules/` (the file name is the rule's name made into a file name, `gaming audio` becoming `gaming-audio`) and keeps the old file in `~/.config/hostd/.pre-0.2/`. Running it again changes nothing. `sudo hostd-setup update` runs it for you before it restarts hostd.
