# Services and deploys

A **service** is a background app (`process` or `docker`) with a `[source]`: it gets new versions from git or a registry. hostd switches to a new version with no downtime, and keeps the running one if anything fails.

```toml
# ~/.config/hostd/apps/blog.toml
runner = { type = "process", command = ["./server"] }

[source]
type  = "push"
build = ["go", "build", "-o", "server", "."]
port  = 8080              # the public port; each release gets PORT

[health]
http = "http://127.0.0.1:{port}/healthz"
```

## How a deploy runs

1. The commit's files go to `~/hostd/releases/<app>/<rev>-<time>`.
2. The build command runs there. Its output is in `.hostd-build.log`, and its end is in the error message.
3. The release starts next to the running one, on an internal port (`PORT`).
4. The health check passes.
5. hostd's proxy on the public port sends new requests to the new release. Requests already on the old release finish there, and then the old release stops.

A failure at any step stops the new release, and the old one keeps serving. hostd keeps 5 releases. `hostctl deploy rollback blog` goes back to the release before the live one.

```sh
hostctl deploy init blog        # the repository to push to
hostctl deploy run blog         # deploy the branch's head now
hostctl deploy rollback blog
curl .../v1/deploys/blog        # the releases and their results
```

## Sources

| `type` | New versions come from | Keys |
| --- | --- | --- |
| `push` | `git push` to the machine (`ssh://screen@<machine>/home/screen/hostd/git/<app>.git`) | `branch` (default main) |
| `remote` | A git remote, checked every `poll` (default 60s) | `url`, `branch`, `poll` |
| `webhook` | A push notification from GitHub or Gitea, to `POST /v1/hooks/git/<app>` | `url`, `branch`, `secret` |
| `image` | A registry tag's digest, checked every `poll` (runner `docker`) | `image`, `tag`, `container_port`, `auth`, `poll` |

The automatic sources (`remote`, `webhook`, `image`) never deploy a commit or digest that is already in the history. So a broken commit is tried once, and a rollback stays in place until something new arrives. `hostctl deploy run` deploys anything.

**Webhooks** are signed with the `secret` (HMAC-SHA256). hostd's API is on the home network only, so a git host reaches it through a tunnel or relay.

**Container images** run as `image@digest`, with `container_port` published on the release's internal port.

## Keys

- **Deploy keys** for private repositories: `hostctl deploy key <app>` creates a key and prints its public half. Add that to the repository as a read-only deploy key. `hostctl config key` does the same for a followed config.
- **Push-only SSH keys**: `hostctl deploy authorize laptop "$(cat ~/.ssh/hostd-push.pub)"` lets a key `git push` to services and do nothing else on the machine. `hostctl deploy revoke laptop` removes it.

## Secrets

```sh
hostctl secret set db-pass          # reads the value from standard input
```

A secret is a file in `~/.config/hostd/secrets/`, readable only by the screen user. App files name it as `{secret:db-pass}` in `env`. A webhook names it in `secret`, and an image source in `auth` (as `user:token`). Values never appear in the audit trail, in events or in app listings.

Secrets are not encrypted. This protects them against other users on the machine, and against committing them with your config. It does not protect them against anything that runs as the screen user, or against someone who has the disk.

## Following a config repository

```sh
hostctl config follow git@github.com:me/hostd-config.git
```

The repository's `apps/` folder becomes the app files. Each new commit is checked first. A commit whose files all pass replaces the app files in one step. One with problems is rejected with a `config.rejected` event, and the previous config stays. The app files that were there before are kept as `apps.before-follow`. `hostctl config unfollow` turns the files in use back into an ordinary folder.
