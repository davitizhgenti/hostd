# The API

One HTTP API under `/v1`, with JSON bodies, on the unix socket `$XDG_RUNTIME_DIR/hostd.sock`. It is also served on `:7300`, which answers the home network only. Reads are plain `GET`s. Every change is a `POST` that becomes an action and answers with its result, or with `202` and the action's ID for slow actions such as deploys. Events stream over a WebSocket.

**The full description**: [`openapi.json`](openapi.json), OpenAPI 3.1, generated from the modules' manifests, so it always matches the running modules. A running hostd serves its own at `GET /v1/openapi.json`, including any external modules.

## Tokens and scopes

Every request carries a token: `Authorization: Bearer hostd_...`. Each device or script gets its own token, which can be revoked on its own:

```sh
hostctl token create phone --scopes read,apps,audio
hostctl token list
hostctl token revoke phone
```

| Scope | Allows |
| --- | --- |
| `read` | Every read and the event stream |
| `apps` | Start and stop apps, focus and close windows |
| `display.front` | Bring an app to the front while someone is using the screen |
| `display` | Display power and modes, window placement, notices |
| `audio` | Volume, outputs, each app's volume, media keys |
| `deploy` | Deploys, rollbacks, deploy keys |
| `scenes`, `rules` | Apply scenes; pause and resume rules |
| `admin` | Everything, including tokens, secrets, updates and push keys |

Modules add their own scopes. External modules do the same, such as `lights` for the example module.

## Actions

```sh
curl --unix-socket $XDG_RUNTIME_DIR/hostd.sock -H "Authorization: Bearer $T" \
     -d '{"percent": 40}' http://x/v1/audio/volume
```

Each action that declares a route has one, such as `POST /v1/audio/volume` or `POST /v1/apps/{id}/start`. Any action can also be sent to `POST /v1/actions` as `{"type": "audio.volume.set", "args": {"percent": 40}}`.

The result is `{"action": "act_...", "status": "applied", "version": 18, "data": ...}`. A change held by a higher-priority source is `"status": "skipped"`, with `held_by` and `until`. A skip is not an error, so scripts carry on.

**Errors** have stable codes, for scripts to branch on: `invalid_args` (400), `unauthorized` (401), `forbidden` (403), `not_found` (404), `instance_not_running` and `loop_detected` (409), `precondition_failed` (412), `module_unavailable` (503), `timeout` (504).

**Versions.** Every resource has a version that goes up on each change. A strict caller passes `?expect_version=N`, and the action is refused with `precondition_failed` if the resource has changed since then.

## Events

`GET /v1/events` upgrades to a WebSocket. Pass `?type=audio.*` for one namespace, or `?since=<event id>` to replay what was missed. `hostctl events` prints them.

## The audit trail

`GET /v1/actions` (`hostctl log`) lists every action with its source, result and resource version. It also records changes made outside hostd, as `observed`. `GET /v1/actions/running` lists the actions in progress.
