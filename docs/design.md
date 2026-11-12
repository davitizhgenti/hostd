# Design

Everything hostd shows on a screen follows one design: the menu, notices, the volume and screen OSD, the Sway background, and this docs site. Anything new that draws, such as a remote control, uses the same tokens and the same components, so it looks and behaves like the rest.

## Principles

- **Simple and quiet.** Flat and square: no rounded corners, no filled pills, no shadows. Type, space and brightness carry the rest, on a calm background. Color in the content means something (running, a warning, an error) and is never decoration.
- **Readable from across the room.** Body text is 26 px on a 1080-pixel-high screen; nothing is smaller than 20 px. Every size scales with the screen's height (a 4K TV gets twice as much, a 1280×800 projector a little less, never below 85 %).
- **One focus, always visible.** The current item sits at the cross point, larger and brighter than the rest, never only a color change. Nothing moves the focus by itself.
- **Every input does the same.** A keyboard, a mouse and a game controller work the same everywhere (see [input](#input)). The hints at the bottom right name the buttons of whatever was used last.
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
| `color` | `bg`, `surface`, `surface-raised`, `border`, `text`, `text-muted`, `accent`, `positive`, `warning`, `danger` | |
| `font` | `text` (Inter), `mono` (DejaVu Sans Mono) | |
| `type` | `display`, `title`, `body`, `label` | px |
| `weight` | `light` (large type), `regular`, `strong` | |
| `space` | `1`, `2`, `3`, `4`, `6`, `8`, `12`, `16` (4 px steps) | px |
| `line` | `border`, `focus` | px |
| `motion` | `fast`, `base` | ms |
| `layout` | `safe-margin`; the cross: `cross-x`, `cross-y`, `category-step`, `icon-category`, `icon-item`, `item-height`, `item-current`, `header-height`; `panel-width`, `notice-width`, `osd-width` | px |
| `month` | `jan` … `dec`: the background's hue in its month mode | |

Colors have roles, not names: `accent` is the focus and the selection (the text's own color), `positive` is running or done, `warning` is starting or skipped, `danger` is an error, closing or a critical notice. `safe-margin` keeps content off the screen's edges, which projectors and TVs crop.

## The layout: a cross

The menu is laid out like the PSP's XMB. The categories (Apps, Users, Settings, Status) are icons on one row at about a third of the screen's height; the current one sits at the cross point (`cross-x`, `cross-y`) with its name under it. Its items run up and down through the same point: the current one just under the row, larger, with its second line, its state and its value; the ones after it below; the ones before it above the row, fainter. Left and right slide the row, up and down the column (`motion.base`, an ease-out). The clock and the category's summary are at the top right, the button hints at the bottom right, the filter (Apps only) at the top left.

```
                                                         Thu 13 Nov   21:40
   RUNNING                                               2 running
   ▭  Steam                                running
  [▦]       ⚇        ☰        ∿
  Apps
   ▭  Chromium                            starting
      chromium
   APPS
   ▭  Dolphin
   ▭  Kodi                                     ◀ ▶ category   Esc back
```

## Components

`deploy/files/ui/theme.css` styles the components with tokens only (a test fails if it has a color of its own); `hostd_ui/components.py` builds them, `hostd_ui/cross.py` places them. A screen is made of these and nothing else:

| Component | What it is |
| --- | --- |
| Cross | the category row and the current category's item column, slid into place |
| Category | an icon; its name under it when it is current |
| Item | an icon and a title; when current, larger, with its second line, state and value |
| Section label | a small, spaced, upper-case label in the column ("RUNNING") |
| Badge | a short word in a role color, no box: `running`, `starting`, `on screen`, `skipped`, `failed` |
| Options panel | an item's options, at the right edge over a dimmed screen (the PSP's triangle menu); for a level (the volume), the value and a vertical bar, changed with up and down |
| Confirm band | a question across the middle of the screen, with No and Yes as words, the chosen one underlined; No is chosen first |
| Notice | a flat panel at the top right, under the clock: the app, a summary, a body; a thin colored edge for its urgency |
| OSD | a short value at the bottom centre (the volume, whose screen it is), gone after 1.6 s |

What a category lists is plain data, apart from GTK (`hostd_ui/views.py`): a list of rows, and a row says what choosing it does (a request to hostd's API, or a change on this screen alone) and which icon it has. That is the part to reuse for another screen.

## Icons

Categories, settings and actions use a few line icons from [Lucide](https://lucide.dev) (ISC license, `deploy/files/ui/icons/LICENSE`), drawn in the text color with thin strokes (`hostd_ui/icons.py`). An app shows its own icon (its desktop entry's `Icon`, from the icon theme or Flatpak's exports), or its first letter in a hairline square when it has none. A new icon is one more SVG from the same set in `icons/`.

## The background

What is behind the menu, and behind every app, is the screen's own setting, `~/.config/hostd/ui.toml`, changed under Settings → Background:

```toml
[background]
mode = "month"      # month (the default), color or image
color = "#473d1f"   # mode = "color"
image = "/path/to/picture.jpg"  # mode = "image"
```

In the month mode, as on the PSP, the month gives the hue (the `month` tokens) and the time of day the lightness: darkest at night, lightest in the afternoon. A color is never so light that white text stops reading; an image is darkened for the same reason. The menu draws it as a gradient; `hostd-notices` sets the same color (or the image) behind the apps, when the setting changes and every few minutes.

## Input

Every input is turned into one of a few verbs (`hostd_ui/input.py`), and screens act on verbs only:

| Verb | Keyboard | Controller | Mouse |
| --- | --- | --- | --- |
| item | Up/Down, Page Up/Down | D-pad up/down | scroll, click |
| category | Left/Right, Tab | D-pad left/right | click a category |
| choose | Enter, Space | A | click the current item |
| back | Esc, Backspace | B | |
| options | Menu, Shift+F10 | X | right click |
| close an app | Delete | | |

Left and right always change the category; a level (the volume) is changed in its panel with up and down. Typing on Apps filters it. Controllers reach the menu as hostd's `display.nav` events: while the menu is in front, hostd holds the controller that opened it, so the app behind does not act on its buttons too.

## The menu

Super (or holding a controller's Guide button) opens it; the same goes back. Its categories:

| Category | Shows |
| --- | --- |
| Apps | the running apps, then the catalog. Choose one to bring it forward or start it; its actions (and the apps under it, such as Steam's games) are in its options |
| Users | who may use the screen and who has it now. Choose another user to give them the screen, keyboard and controllers (the [seats add-on](install.md)) |
| Settings | the volume, mute and sound output; each screen's resolution; turning the screen off; the background |
| Status | the hostd version, video memory, and the latest actions with who sent them |

The menu acts with the machine's local token (`~/.config/hostd/local.token`, scopes `read,apps,audio,display`, and `seat` with the seats add-on), so what is chosen on the screen counts as someone at the screen.

## Notices

`hostd-notices` is the session's notification server (`org.freedesktop.Notifications`), so hostd's own notices (`hostctl display notice`, "Steam wants the screen") and every app's notifications are drawn in this design, at the top right, above fullscreen games, at most three at once (critical ones first and longer). It also shows the OSD when the volume changes or the screen goes to another user. It runs as the user service `hostd-notices.service`, started by Sway; without gtk4-layer-shell it runs mako instead.
