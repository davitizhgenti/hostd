# hostd

hostd turns a Linux mini PC into a controllable screen and home server. A small
core routes actions to modules (apps and windows, audio, background services,
deploys, automation) and events back to listeners. You drive it from the
`hostctl` CLI, an HTTP API, or scripts.

Status: early development, nothing usable yet.

- Design: [design/hostd Display and Services Daemon Design.md](design/hostd%20Display%20and%20Services%20Daemon%20Design.md)
- Build and test plan: [design/plan.md](design/plan.md)

## Install on a machine

Needs a fresh **Debian 13 (trixie)** install: minimal, with only "SSH server"
and "standard system utilities" selected, and an admin user with sudo. Put
your laptop's key on that admin user first (`ssh-copy-id admin@machine`); the
installer copies it to the `screen` user.

On the machine, as the admin user:

```sh
wget -qO- https://github.com/davitizhgenti/hostd/archive/refs/heads/main.tar.gz | tar xz
sudo ./hostd-main/deploy/install.sh
sudo reboot
```

The installer detects an NVIDIA GPU and installs the driver (enabling
Debian's `contrib` and `non-free`), installs Sway, PipeWire, Podman and
greetd, creates the `screen` user, and logs it into Sway at boot. It is safe
to run again: it only changes what is missing. After the reboot the screen
shows a plain dark background with no login prompt.

Check the machine from the laptop, from a clone of this repository:

```sh
make check-server SERVER=screen@<machine>
```

hostd itself is not released yet (milestone M1). Once it is, the same
installer installs it.

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
