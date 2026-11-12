# hostd: status and what is left

Updated 2026-11-13. The history of every earlier plan (the 1.0 work items, the installer in Go, the root-free replan, the screen-sharing attempts) is in git: `git log -- plan.md`.

## Where it is

- hostd's core is generic: modules (display, apps, audio, automation, deploy, external ones in any language) over one resource model (apps, scenes, rules, scripts, modules in `~/.config/hostd/`), a manifest-driven `hostctl`, signed automatic updates.
- **hostd never runs as root.** One OS user per system; `hostd-setup` run as that user installs and updates in the home folder. Root is only for `hostd-admin` (prepare, root add-ons, users, seats), kept separate on purpose.
- **Seats:** the screen, keyboard and controllers belong to one user's session at a time (logind, one VT per user); the `seats` add-on has the root helper that switches (`hostctl seat switch USER`). Other users' sessions start on demand, never at boot. greetd retries its automatic login every 5 seconds, so a Sway that died at boot does not leave a dark screen.
- Add-ons: flatpak, controllers, nvidia, compose, wireguard, tailscale, seats, steam, chromium, dolphin.
- **Released:** v0.2.0 (2026-11-12). Seat switching tested on core, both ways, from core and from the laptop.

## The design system (2026-11-13)

One design for everything hostd draws (docs/design.md). The tokens (`deploy/files/ui/tokens.toml`) are generated (`scripts/uitokens`, `go generate ./deploy`) into CSS custom properties for GTK and the docs site, JSON, and the Sway background; CI fails when they are stale. The look is flat and square (no rounded corners, pills or shadows). The menu is a cross like the PSP's XMB: categories (Apps, Users, Settings, Status) on a row, the current one's items in a column through it, options in a panel at the right edge, questions in a band across the screen. Line icons from Lucide (ISC); apps show their own icons. The background is the screen's own setting (`~/.config/hostd/ui.toml`: the month and time of day by default, a color, or an image), drawn by the menu and set behind the apps by `hostd-notices`. `hostd_ui` (Python, GTK 4) holds the API client, the input verbs, the components, the cross geometry, the background and the views (plain data, unit-tested). `hostd-notices` is the notification server (top right, at most three, critical first) and the OSD; mako is gone. The menu's token gets `read,apps,audio,display` (and `seat` with the seats add-on).

## Screen sharing: tried, dropped

Sunshine/Moonlight, UxPlay, scrcpy, lan-mouse and RDP were all tried on core. Every route was heavy or slow: GNOME Remote Desktop encodes on the CPU (its VA-API check wants the regular encode entrypoint, the laptop's Intel chip only offers the low-power one), Sunshine is fast but needs software on the PC, core has no Wi-Fi so Miracast is impossible. Left from that work: the menu scales with the screen height, `[display.outputs.NAME] mode`, the mouse pointer hiding after 2 seconds, the seats add-on. Wireless without software on core would need a wireless-display dongle on the projector.

## Needs the user

- **The cross menu on core:** checked in a headless Sway container at 1920x1080 and 1280x800 (every category, options, the volume panel, the confirm band, the background choices and Sway's background following them, notices), not yet on the projector. Core has the packages it needs (layer shell, Inter, librsvg).
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
