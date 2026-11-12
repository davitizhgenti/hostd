# Design

Everything hostd shows on a screen follows one design: the menu, notices, the volume and screen OSD, the Sway background, and this docs site. Anything new that draws, such as a remote control, uses the same tokens and the same components, so it looks and behaves like the rest.

## Principles

- **Readable from across the room.** Body text is 27 px on a 1080-pixel-high screen; nothing is smaller than 22 px. Every size scales with the screen's height (a 4K TV gets twice as much, a 1280×800 projector a little less, never below 85 %).
- **One focus, always visible.** The chosen row has a ring in the accent color and a lighter surface, never only a color change. Nothing moves the focus by itself.
- **Every input does the same.** A keyboard, a mouse and a game controller work the same everywhere (see [input](#input)). The hint bar at the bottom names the buttons of whatever was used last.
- **Ask before what cannot be undone at once.** Switching the screen to another user and turning the screen off ask first, with the safe answer (No) chosen.
- **Calm.** Changes take at most 150 ms; nothing blinks or slides for long. Notices never take the focus from a game.
- **Plain words.** "Bring to front", "Give the screen to guest?", "Skipped: someone is at the screen". Ids (`steam-570`) appear only as a second line, in the monospace font.

## Tokens

The look is defined once, in `deploy/files/ui/tokens.toml`. `go generate ./deploy` writes it out for everything that draws:

| File | Used by |
| --- | --- |
| `deploy/files/ui/tokens.css` | the menu and notices (GTK 4 reads CSS custom properties) |
| `deploy/files/ui/tokens.json` | anything that is not CSS |
| `docs/stylesheets/tokens.css` | this site |
| `deploy/files/sway/config` | the screen's background (between its token markers) |

CI fails when one of them is out of date. Each token becomes a CSS custom property named `--<group>-<name>`:

| Group | Tokens | Unit |
| --- | --- | --- |
| `color` | `bg`, `surface`, `surface-raised`, `border`, `text`, `text-muted`, `accent`, `on-accent`, `positive`, `warning`, `danger` | |
| `font` | `text` (Inter), `mono` (DejaVu Sans Mono) | |
| `type` | `display`, `title`, `body`, `label` | px |
| `weight` | `regular`, `strong` | |
| `space` | `1`, `2`, `3`, `4`, `6`, `8`, `12`, `16` (4 px steps) | px |
| `radius` | `small`, `medium`, `large` | px |
| `line` | `border`, `focus` | px |
| `motion` | `fast`, `base` | ms |
| `layout` | `safe-margin`, `content-max`, `notice-width`, `osd-width`, `sheet-width`, `row-height` | px |

Colors have roles, not names: `accent` is the focus and the selection, `positive` is running or done, `warning` is starting or skipped, `danger` is an error, closing or a critical notice. `safe-margin` keeps content off the screen's edges, which projectors and TVs crop.

## Components

`deploy/files/ui/theme.css` styles the components with tokens only (a test fails if it has a color of its own); `hostd_ui/components.py` builds them. A screen is made of these and nothing else:

| Component | What it is |
| --- | --- |
| Page | tabs at the top, an optional filter, a list, a message line and the hint bar |
| Tab bar | the pages' names; the current one filled with the accent color |
| Row | a title, an optional second line, then a badge, a slider or a value on the right |
| Section header | a small upper-case label above a group of rows |
| Badge | a short word in a role color: `running`, `starting`, `on screen`, `skipped`, `failed` |
| Slider row | a level and its value; left and right change it |
| Sheet | a row's actions, floating over the page; the page dims behind it |
| Dialog | a question with No and Yes; No is chosen first |
| Notice | a card at the top centre: the app, a summary, a body; a colored edge for its urgency |
| OSD | a short value at the bottom centre (the volume, whose screen it is), gone after 1.6 s |

What a page lists is plain data, apart from GTK (`hostd_ui/views.py`): a page is a list of rows, and a row says what choosing it does (a request to hostd's API). That is the part to reuse for another screen.

## Input

Every input is turned into one of a few verbs (`hostd_ui/input.py`), and screens act on verbs only:

| Verb | Keyboard | Controller | Mouse |
| --- | --- | --- | --- |
| move | arrows, Page Up/Down | D-pad | hover, scroll |
| change page | Left/Right, Tab | D-pad left/right | |
| choose | Enter, Space | A | click |
| back | Esc, Backspace | B | |
| actions | Menu, Shift+F10 | X | right click |
| close an app | Delete | | |

Left and right change a slider when one is chosen, and the page otherwise. Typing on the Apps page filters it. Controllers reach the menu as hostd's `display.nav` events: while the menu is in front, hostd holds the controller that opened it, so the app behind does not act on its buttons too.

## The menu

Super (or holding a controller's Guide button) opens it; the same goes back. Its pages:

| Page | Shows |
| --- | --- |
| Apps | the running apps, then the catalog. Choose one to bring it forward or start it; its actions (and the apps under it, such as Steam's games) are in its sheet |
| Users | who may use the screen and who has it now. Choose another user to give them the screen, keyboard and controllers (the [seats add-on](install.md)) |
| Settings | the volume, mute and sound output; each screen's resolution; turning the screen off |
| Status | the hostd version, video memory, and the latest actions with who sent them |

The menu acts with the machine's local token (`~/.config/hostd/local.token`, scopes `read,apps,audio,display`, and `seat` with the seats add-on), so what is chosen on the screen counts as someone at the screen.

## Notices

`hostd-notices` is the session's notification server (`org.freedesktop.Notifications`), so hostd's own notices (`hostctl display notice`, "Steam wants the screen") and every app's notifications are drawn in this design, above fullscreen games, at most three at once (critical ones first and longer). It also shows the OSD when the volume changes or the screen goes to another user. It runs as the user service `hostd-notices.service`, started by Sway; without gtk4-layer-shell it runs mako instead.
