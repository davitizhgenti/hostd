# hostd

hostd turns a Linux mini PC into a controllable screen and home server. A small
core routes actions to modules (apps and windows, audio, background services,
deploys, automation) and events back to listeners. You drive it from the
`hostctl` CLI, an HTTP API, or scripts.

Status: early development, nothing usable yet.

- Design: [design/hostd Display and Services Daemon Design.md](design/hostd%20Display%20and%20Services%20Daemon%20Design.md)
- Build and test plan: [design/plan.md](design/plan.md)

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
