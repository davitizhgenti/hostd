# hostd

hostd turns a Linux mini PC into a controllable screen and home server. A small
core routes actions to modules (apps and windows, audio, background services,
deploys, automation) and events back to listeners. You drive it from the
`hostctl` CLI, an HTTP API, or scripts.

Status: in development. The core, the API and hostctl, apps and windows
(with an on-screen menu), and master volume run on a real machine;
background services, deploys and automation are next. See the plan.

- Design: [design/hostd Display and Services Daemon Design.md](design/hostd%20Display%20and%20Services%20Daemon%20Design.md)
- Build and test plan: [design/plan.md](design/plan.md)
- Architecture report (quality, stability, modularity): [design/architecture-report.md](design/architecture-report.md)

## Install on a machine

Needs a fresh **Debian 13 (trixie)** install: minimal, with only "SSH server"
and "standard system utilities" selected, and an admin user with sudo. If you
log in to that admin user with an SSH key, the installer copies the key to the
`screen` user too.

On the machine, as the admin user:

```sh
wget -qO- https://github.com/davitizhgenti/hostd/archive/refs/heads/main.tar.gz | tar xz
sudo ./hostd-main/deploy/install.sh
sudo reboot
```

The installer sets up everything, and is safe to run again (it only changes
what is missing):

- the NVIDIA driver when an NVIDIA GPU is present, Sway, PipeWire, Podman,
  greetd, and the `screen` user that is logged into Sway at boot;
- **hostd**, downloaded from the latest tested build (the `edge` release,
  checked against its checksums), running as a service that starts at boot;
- **hostctl** in `/usr/local/bin`, already logged in for the `screen` user.

After the reboot the screen shows a plain dark background with no login
prompt, and hostd is running. Use it on the machine:

```sh
ssh screen@<machine> hostctl apps --all
```

Any other device or script talks to the API at `http://<machine>:7300` with
its own token (home network only):

```sh
ssh screen@<machine> hostctl token create phone --scopes read,apps,audio
```

**Update** to the latest tested build, and **uninstall**, on the machine:

```sh
sudo hostd-setup update                       # fetches and runs the latest installer
sudo hostd-setup uninstall                    # removes hostd; keeps its data
sudo hostd-setup uninstall --purge            # ...and deletes tokens, audit trail, app files
sudo hostd-setup uninstall --purge --all      # ...and undoes the base setup (autologin, configs)
```

Uninstall never removes system packages, the NVIDIA driver, or the `screen`
user. From a laptop with a checkout, `hostctl update push` installs your own
build instead; if it does not start, the machine rolls back on its own.

**Add-ons** install optional things on top, and every update keeps them
current:

```sh
sudo hostd-setup addons              # list them
sudo hostd-setup add steam dolphin   # Steam in Big Picture with an app per game; Dolphin
sudo hostd-setup add controllers     # game controller access (the app add-ons bring it too)
sudo hostd-setup remove dolphin
```

Connected game controllers: `ssh screen@<machine> hostctl controllers`.

Check the whole machine with `ssh screen@<machine> 'bash -s' < deploy/check.sh`
(or `make check-server SERVER=screen@<machine>` from a clone).

## Development

Requires Go 1.27 or later.

```sh
make            # lint, test, build
make test       # unit tests with the race detector
make build      # binaries in bin/
make cover      # coverage report
```

No server yet? `make devbox` runs a stand-in mini PC in a Podman container
(Sway over VNC, PipeWire, systemd, Podman); see
[test/devbox/README.md](test/devbox/README.md).

Lint tools are pinned in `tools/go.mod` and run with `go tool`, so nothing
needs installing beyond Go.

## License

Apache-2.0, see [LICENSE](LICENSE).
