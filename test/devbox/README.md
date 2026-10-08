# devbox: a mini PC in a container

A Podman container that stands in for the mini PC until real hardware is
available. systemd runs as PID 1 and the `screen` user has lingering on, as on
the real machine. Its session runs:

- **Sway** on a virtual 1920×1080 output (`HEADLESS-1`), viewable over VNC
- **PipeWire + WirePlumber** with two fake sinks, `fake-hdmi` (default) and
  `fake-speakers`; volume, mute and output switching work, but there is no sound
- **rootless Podman** with its API socket, for the `docker` runner and deploys
- **mako** for on-screen notices

Not covered: game controllers, real games, HDMI/TV, Bluetooth, greetd
autologin and reboots. Those need the real machine or a VM (plan §9.4).

## Use

From the repository root (needs Podman):

```sh
make devbox          # build the image and hostd, (re)start the container
make devbox-check    # smoke test; writes a screenshot to bin/devbox-screen.png
make devbox-shell    # shell as the screen user (WAYLAND_DISPLAY, SWAYSOCK set)
make devbox-logs     # system journal
make devbox-stop     # remove the container
```

**Watch the screen:** connect a VNC viewer (Remmina or GNOME Connections) to
`127.0.0.1:5900`. There is no VNC password, so the port is published on
localhost only.

**Try things inside** (`make devbox-shell`):

```sh
systemd-run --user --unit=hostd-test -p Type=exec foot   # open an app the way hostd will
swaymsg -t get_tree | less                                # window tree
wpctl set-volume @DEFAULT_AUDIO_SINK@ 0.4                 # change volume
podman run --rm docker.io/library/alpine echo hi          # run a container
```

**hostd itself:** `bin/` from the repository is mounted read-only at
`/opt/hostd/bin` and is on the screen user's `PATH`, so `make build` on the
host updates the binaries inside without a rebuild. Port 7300 (the API) is
published on `127.0.0.1:7300` for `hostctl` on the host.

**Run hostd as on the real machine:** `make devbox-hostd` runs it as a user
service from `bin/`, logs `hostctl` in, sets up the switcher (straight from
`deploy/files/overlay`, with its local token) and adds three apps to try. In
VNC, press F1 (or Super, with the viewer's keyboard grab on) for the switcher;
Super+Tab and Super+Q work as on the real machine.
Run it again after `make build` to restart hostd on the new binary. The
devbox cannot read input devices, so nobody is ever "at the screen" there:
every launch comes to the front.

**Try hostd with the demo module** (a pretend lamp, until the real modules
exist):

```sh
make build
podman exec -u screen hostd-devbox bash -lc 'systemd-run --user --unit=hostd-demo /opt/hostd/bin/hostd --demo'
podman exec -u screen hostd-devbox cat /run/user/1000/hostd-admin-token > /tmp/hostd-token
./bin/hostctl login 127.0.0.1:7300 --token-file /tmp/hostd-token
./bin/hostctl demo lamp set desk 40
./bin/hostctl log
```

## Notes

- The container runs with `--privileged`, which for rootless Podman means
  privileged *inside your user's namespace*, not root on the host. Nested
  Podman needs it.
- Images pulled inside the devbox live in the `hostd-devbox-containers`
  volume, so they survive `make devbox`. Remove with
  `podman volume rm hostd-devbox-containers`.
- Fake sinks are kept running (`node.always-process`), because idle null sinks
  ignore volume changes; real sound cards don't have this problem.
