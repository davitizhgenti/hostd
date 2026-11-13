# hostd: status and what is left

Updated 2026-11-13. The history of every earlier plan (the 1.0 work items, the installer in Go, the root-free replan, the screen-sharing attempts) is in git: `git log -- plan.md`.

## Where it is

- hostd's core is generic: modules (display, apps, audio, automation, deploy, external ones in any language) over one resource model (apps, scenes, rules, scripts, modules in `~/.config/hostd/`), a manifest-driven `hostctl`, signed automatic updates.
- **hostd never runs as root.** One OS user per system; `hostd-setup` run as that user installs and updates in the home folder. Root is only for `hostd-admin` (prepare, root add-ons, users, seats), kept separate on purpose.
- **Seats:** the screen, keyboard and controllers belong to one user's session at a time (logind, one VT per user); the `seats` add-on has the root helper that switches (`hostctl seat switch USER`). Other users' sessions start on demand, never at boot. greetd retries its automatic login every 5 seconds, so a Sway that died at boot does not leave a dark screen.
- Add-ons: flatpak, controllers, nvidia, compose, wireguard, tailscale, seats, steam, chromium, dolphin.
- **Released:** v0.2.0 (2026-11-12). Seat switching tested on core, both ways, from core and from the laptop.

## The shell and the design system (2026-11-13)

One design for everything hostd draws (docs/design.md). `hostd-shell` (PySide6, Qt Quick) is one process for the menu, the background behind the apps, notices and the OSD; Python holds the state and the behaviour, QML only draws it. The menu stays hostd's app `hostd-menu`: its command `hostd-shell menu` asks the shell to show the menu and runs until it closes, and the window's app_id (`[match]`) makes it the instance's. The layout follows one grid (`Place.qml`) like the PSP's XMB; tokens (`deploy/files/shell/tokens.toml`) generate a typed `Theme.qml`. `make test-shell` holds it: the layout contract at three sizes, golden images, behaviour, contrast on every background, qmllint, and a live Wayland check. Review renders locally in `test/shell/out/index.html`.

## Screen sharing: tried, dropped

Sunshine/Moonlight, UxPlay, scrcpy, lan-mouse and RDP were all tried on core. Every route was heavy or slow: GNOME Remote Desktop encodes on the CPU (its VA-API check wants the regular encode entrypoint, the laptop's Intel chip only offers the low-power one), Sunshine is fast but needs software on the PC, core has no Wi-Fi so Miracast is impossible. Left from that work: the menu scales with the screen height, `[display.outputs.NAME] mode`, the mouse pointer hiding after 2 seconds, the seats add-on. Wireless without software on core would need a wireless-display dongle on the projector.

## Needs the user

- **The shell on core:** it needs the Qt packages (`sudo hostd-admin prepare`); not yet seen on the projector.
- **Two controllers on core:** hold Guide on each to confirm only that pad is grabbed.
- `make test-e2e` (the VM test, about 10 minutes) and `mkdocs build --strict` after these changes.

## Code not done

- `config.follow` is in `deploy`; it belongs in the core as `resources.follow`.
- Instance lifecycle: one state machine with timeouts on every edge; split `modules/apps/instances.go` and display's large `Module`; runners as plug-ins.
- Protocol: manifest `Hooks` to `Webhooks`; external modules' `do`/`read` carry no caller deadline.
- Devices: per-seat presence; no `input` module split out of display.
- Add-ons: Steam's game list is a Python script with systemd path/timer units (should be an external module); `hostd-admin user add` still runs the whole install from root; Jupyter and Pterodactyl were removed (git history).

## Known limits

- Key bindings are the same for every keyboard (Sway does not say which keyboard).
- A changed `modules/<name>.toml` takes effect at the next hostd start.
- Player slots are saved, but a controller away from hostd's start can lose its slot to a newcomer.
- Coverage-floor exceptions: `internal/fsx` (55%) and the conformance suite (75%), with reasons in `scripts/coverage-floors.txt`.
