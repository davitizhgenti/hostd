# Security

What hostd protects, from whom, and what it leaves to you. hostd runs a home machine: its screen, its apps and the services on it. It is built for a home network you control. It is not meant to face the internet.

## Who can do what

| Who | Can | Because |
| --- | --- | --- |
| Anyone on the internet | Nothing | No port is open to the internet, except WireGuard's if you add it, which answers only the keys you added. The API refuses addresses outside the home network and `[api] allow`, even if a port were forwarded. |
| A device on the home network, without a token | Read the docs | Everything else needs a token. |
| A device with a token | What the token's scopes allow, nothing more | Each action declares its scope; the core checks it before the module sees the action. Every action is in the audit trail (`hostctl log`). |
| A token with `run` or `admin` | Anything the `screen` user can: run any command, read its files | That is what these scopes are for. Give them only to devices you trust like a login, and revoke them when a device is lost (`hostctl token revoke`). |
| The `screen` user | Not root | `screen` has no sudo, no password (`passwd -S screen`: L), and is not in the `docker` group. |
| The admin user (`serve` on core) | Root, with sudo | SSH accepts keys only, once the admin has one (the installer turns passwords off; `--ssh-passwords keep` opts out). |

**WireGuard** (the `wireguard` add-on) adds a tunnel of your own: one forwarded UDP port, silent to anyone without a key you added (`hostd-wg add`). Its devices reach the machine and nothing behind it (a firewall table of the tunnel's drops anything forwarded from or to it), and hostd still asks them for a token. Remove a lost device with `hostd-wg remove`; its token too (`hostctl token revoke`).

**Tailscale** (the `tailscale` add-on) adds your tailnet to the home network: its devices can reach hostd as home devices can, with a token. Nothing is opened to the internet.

## On the network

- **The API on port 7300 is HTTPS.** The machine made its own certificate authority at its first start. Clients pin that CA: `hostctl login` shows its fingerprint before sending anything, and the Python client takes `ca_fingerprint`. So a device in between (on the same Wi-Fi) cannot read or change what passes, even though no outside CA is involved. Plain HTTP on that port gets the docs and an error, never the API.
- **The CA can only vouch for this machine**: its X.509 name constraints allow the host name, `<host>.local`, the names it was made with, and private addresses. If you install `ca.pem` in a browser, it cannot be used to fake any other site, even if its key were stolen.
- **The unix socket** (`$XDG_RUNTIME_DIR/hostd.sock`, mode 0600) is plain HTTP; it never leaves the machine. The menu, hostctl on the machine, scripts and git hooks use it.
- **Webhooks** (`POST /v1/hooks/...`) need no token, but each checks its own signature (an HMAC from GitHub or Gitea).
- **The Pterodactyl panel** (an add-on) is served over HTTPS with a certificate from the same CA; its port 80 only redirects. Wings, which runs the game servers, is HTTPS too. The panel and Wings run as root and control Docker: keep the panel's admin password strong, and its version current (the add-on pins it, and `sudo hostd-setup update` upgrades it).
- **VNC** listens on localhost only: reach it through SSH (`ssh -L 5900:127.0.0.1:5900 screen@<machine>`).

## On the machine

- **Tokens** are stored only as hashes (SHA-256 of 32 random bytes). A token is shown once, when it is made.
- **Secrets** (`hostctl secret set`) are files readable by `screen` only (0600). They are protected from other users and from accidental commits of a config repository. They are not protected from anything that runs as `screen`.
- **Keyboards**: `screen` is in the `input` group, which hostd needs to know whether someone is at the screen and for controller buttons. Anything that runs as `screen` (every app, game and `run` command) can therefore read the machine's keyboards and controllers. Do not run untrusted programs as `screen`.
- **App files and `app.run`** run commands as `screen`. An app's arguments (`args`) are checked (a URL must be http or https, a path absolute, no value may start with `-`), so starting an app with arguments needs only the `apps` scope.

## Updates

- **Every build is signed** (Ed25519). The installer and the update timer check the signature with the key in the installer, and the checksums of every file they use, before installing anything.
- **The edge channel** installs every build of `main` that passed CI, within hours. Whoever can push to `main` can therefore change the machine. Protect that: two-factor authentication on the GitHub account, the signing key in the `release` environment (usable from `main` and `v*` tags only), and CI actions pinned by commit (dependabot proposes updates, which CI tests). The **stable** channel (`CHANNEL=stable` in `/etc/hostd/update.conf`) installs tagged releases only.
- **Every update can be rolled back**: hostd keeps the previous version and a copy of its state, and a version that does not start is rolled back by itself.

## Reporting a problem

Report a vulnerability privately through GitHub (the repository's Security tab, "Report a vulnerability"), not in a public issue.
