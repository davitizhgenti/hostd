# Design

Everything hostd shows on a screen follows one design: the menu, notices, the volume and screen OSD, the background behind the apps, and this docs site. Anything new that draws, such as a remote control, uses the same tokens, so it looks like the rest.

## Principles

- **Simple and quiet.** Flat and square: no rounded corners, no filled pills, no shadows. Type, space and brightness carry the rest, on a calm background. Color in the content means something (running, a warning, an error) and is never decoration.
- **One grid.** Every position on the menu comes from one place (`Place.qml`) and a few rules ([the grid](#the-grid)); tests hold every screen to them.
- **Readable from across the room.** Body text is 26 px on a 1080-pixel-high screen, nothing smaller than 20 px; every size scales with the screen's height (never below 85 %). Text keeps a contrast of at least 4.5:1 (3:1 for muted text) on every background the shell can draw; a test checks every month and hour.
- **One focus, always visible.** The current item sits at the top of the list, under the current category, with a larger title and full brightness; nothing moves sideways and nothing is scaled. Nothing moves the focus by itself.
- **Every input does the same.** A keyboard, a mouse and a game controller work the same everywhere ([input](#input)). The hints at the bottom right name the buttons of whatever was used last.
- **Ask before what cannot be undone at once.** Switching the screen to another user and turning the screen off ask first, with the safe answer (No) chosen.
- **Calm.** Moves take 220 ms, fades 120 ms, nothing blinks. Notices never take the focus from a game.
- **Plain words.** "Bring to front", "Give the screen to guest?". Ids (`steam-570`) appear only as a second line, in the monospace font.

## The shell

`hostd-shell` (Python with PySide6, Qt Quick) is one process that draws everything: the menu, the background behind the apps (a layer below them), notices and the OSD (a layer above them, games included, taking no input). It runs as the user service `hostd-shell.service`, started by Sway.

- **Python holds the state and the behaviour** (`hostd_shell/shell.py`): what is current, what each verb does, the data from hostd's API and events, the background setting. It is tested without a screen.
- **QML only draws it** (`qml/Hostd/`). Every surface gets the shell as a property; there are no global context properties, so qmllint checks every use.
- **The menu stays hostd's app `hostd-menu`.** Its command, `hostd-shell menu`, asks the shell to show the menu (D-Bus, `org.hostd.Shell`) and runs until it is closed; the shell's menu window has the app_id `hostd-menu`, which the app's `[match]` rule claims. So hostd sees the menu as running exactly while it is on screen, holds the controllers for it and handles Back, as for any app.
- **Notices:** the shell is the session's notification server (`org.freedesktop.Notifications`), so hostd's own notices and every app's are drawn in this design. What an app sends is untrusted: text is plain, control characters are dropped, the summary is cut at 200 characters and the body at 1000, at most 50 notices wait, and a program adding more than 20 in 10 seconds replaces its latest instead.
- **A missing package** (Qt, the layer-shell module) makes `hostd-shell` exit with status 78 and one clear line; its unit does not retry that. Add the packages with `sudo hostd-admin prepare`.

## Tokens

The look is defined once, in `deploy/files/shell/tokens.toml`. `go generate ./deploy` writes it out:

| File | Used by |
| --- | --- |
| `deploy/files/shell/qml/Hostd/Theme.qml` | the shell: one typed property per token (`Theme.colorText`, `Theme.layoutSafeMargin`), sizes scaled with the screen |
| `deploy/files/shell/tokens.json` | anything else (a remote) |
| `docs/stylesheets/tokens.css` | this site |
| `deploy/files/sway/config` | the screen's color before the shell draws (between its token markers) |

CI fails when one of them is out of date. The groups:

| Group | Tokens | Unit |
| --- | --- | --- |
| `color` | `bg`, `surface`, `surface-raised`, `border`, `text`, `text-muted`, `accent`, `positive`, `warning`, `danger` | |
| `font` | `text` (Inter), `mono` (DejaVu Sans Mono) | |
| `type` | `display`, `title`, `body`, `label` | px |
| `weight` | `light` (display and title), `regular`, `strong` | |
| `space` | `xs` 4, `s` 8, `m` 16, `l` 24, `xl` 32, `xxl` 48, `xxxl` 64 | px |
| `line` | `hair`, `focus` | px |
| `motion` | `fast`, `base` | ms |
| `layout` | `safe-margin`; the cross: `cross-y`, `column-width`, `category-step`, `icon-category`, `icon-item`, `icon-small`, `item-height`, `item-line`, `section`; `panel-width`, `notice-width`, `osd-width` | px |
| `month` | `jan` … `dec`: the background's hue in its month mode | |

QML takes every color from Theme; a test fails on a color written anywhere else.

## The grid

The menu is laid out like the PSP's XMB: the categories (Apps, any [tabs](#tabs), Users, Settings, Status) on one row, the current one's items in a column under it. `Place.qml` computes every position from the tokens:

```
                                                          13:30
                                                          Sat 14 February
   ▦        ⚇        ☰        ∿                           2 running   9 apps
  Apps                                                    ← the row band: nothing from the list enters it
   │                                                      ← the section: a folder's path, or a Settings group
   ▷ Steam                                      ■        ← the current item, at the list's top; ■ it runs
   │                                                        (a second line under its title line, if it has one)
   C Chromium                                   ■
   ▭ Games                                   4 apps
   K Kodi
   │
 axis  text column                       trail edge                 Enter open   Menu options   Del close
```

- **The axis** is the screen's vertical centre line. The current category's icon sits on it and the other categories fan out either side. **The list is a block** of fixed width (`column-width`) centred on the same axis: item icons centred in the icon column at its left edge, states and values ending at its right edge.
- **The text column:** every title, second line and section label starts at the same x, on every row, current or not.
- **Category names** are centred under their icons; the current one's name shows, the others' space is kept. A category that would cross the safe margin is not shown, never cut by the screen's edge.
- **The row band** (the icons and the name) and the **list** below it never overlap: passed items scroll up and fade before the list's top.
- **Rows** are all the same height; the title line is at the top of each, and the current row's second line sits under it. Icon, title and the trailing state or value are centred on the title line; the trail ends at the column's edge.
- **Corners:** the clock and summary at the top right, the hints at the bottom right, the filter at the top left, the message at the bottom left, all inside the safe margins and clear of the list.
- **The options panel** comes in at the right edge; its heading and options line up with the menu's section and list. **A question** is a band across the middle of the screen.

Placement is arithmetic, not left to Qt's list views: the rows are plain positioners, and moving is one animated offset computed from what is current.

## Components

| Component | What it is |
| --- | --- |
| `Txt` | the only text: one of the four roles, in Theme's sizes and weights |
| `Icon` | an app's own icon, else one of hostd's line icons, else a letter in a hairline square |
| `ListRow` | the one list item: icon column, title line, second line when current, trailing state or value |
| `Badge` | a state in a word, in its role's color, no box |
| `Level` | a hairline track and its filled part, across or upright |
| `Button` | a choice in a word; the current one bright and underlined |
| `Cross` | the category row and the item list |
| `Panel` | the options panel, or a level (the volume) changed with up and down |
| `Dialog` | the question band; No is chosen first |
| `Clock`, `Hints` | the corners |
| `Notice`, `Osd` | a notice (a thin edge in its urgency's color), the OSD |
| `Backdrop` | the background setting: a gradient or a darkened image |

`Gallery.qml` shows every token and component on one screen (`hostd-shell --gallery`).

## Icons

Categories, settings and actions use line icons from [Lucide](https://lucide.dev) (ISC license, `deploy/files/shell/icons/LICENSE`), drawn in the text color with thin strokes by the shell (an image provider, so every renderer shows the same). An app shows its own icon (its desktop entry's `Icon`, from the icon theme or Flatpak's exports), or its first letter in a hairline square.

## The background

What is behind the menu and every app is the screen's own setting, `~/.config/hostd/ui.toml`, changed under Settings → Background:

```toml
[background]
mode = "month"      # month (the default), color or image
color = "#473d1f"   # mode = "color"
image = "/path/to/picture.jpg"  # mode = "image"
```

In the month mode, as on the PSP, the month gives the hue (the `month` tokens) and the time of day the lightness: darkest at night, lightest in the afternoon, never so light that text stops reading. An image is darkened for the same reason.

## Input

Every input becomes one of a few verbs (`hostd_shell/input.py`), and the shell acts on verbs only:

| Verb | Keyboard | Controller | Mouse |
| --- | --- | --- | --- |
| item | Up/Down, Page Up/Down | D-pad or left stick up/down; LT/RT or right stick for a page | glide up or down, wheel |
| category | Left/Right, Tab | D-pad or left stick left/right; LB/RB | glide sideways, tilt the wheel, forward button |
| choose | Enter, Space | A (✕; on a Nintendo pad its A) | click (anywhere) |
| back | Esc, Backspace | B (○) | back button |
| options | Menu, Shift+F10 | X (□) or Start | right click |
| close an app | Delete | Y (△) | |
| first category | | Select | middle click |

**The mouse has no pointer in the menu.** It is felt rather than aimed, like a trackpad on a TV: a short push up or down (about 40 px at 1080p) moves one item, a firmer push sideways (about 100 px) changes category, and a click acts on the current item wherever the hidden pointer is. Slow movement (a hand resting on the mouse) does nothing, a diagonal push moves along one axis only, a half-made step is forgotten after a quarter of a second, and one fling moves at most three items. The pointer is put back in the middle of the screen near an edge, so it never runs out of room. The `input` tokens set these. Outside the menu, Sway hides the pointer after 2 seconds as before.

Left and right always change the category; a level (the volume) is changed in its panel with up and down. Typing on Apps filters it. Controllers (Xbox, PlayStation, Nintendo and Wii; see [Controllers](config.md)) reach the menu as hostd's `display.nav` events, which say the controller's kind, so the hints show the buttons printed on it: while the menu is in front, hostd holds the controller that opened it, so the app behind does not act on its buttons too.

## The menu

Super (or holding a controller's Guide button) opens it; the same goes back. Its categories:

| Category | Shows |
| --- | --- |
| Apps | the catalog in folders (`folder` in an app's file: Games / Steam, Games / Dolphin / Wii): the running apps first, then the folders, then the other apps. Each app is listed once; a running one has a small square in its state's color (running, starting, failed) and is brought forward when chosen, Delete closes it. A folder is a row that opens like a list of its own; Back goes up one level, Home returns to the top, and typing searches every folder (and every tab). Its actions (and the apps under it, such as Steam's games) are in its options; a game started from them is listed next to its launcher while it runs |
| Services | a folder (or a [tab](#tabs)) of the background apps: those with `surface = "background"` and no `folder` of their own. Each shows its state in a word: running, starting, stopping, stopped or failed. Choosing one opens its options, Start, or Stop and Restart: a service never starts or stops on one press |
| Users | who may use the screen and who has it now. Choose another user to give them the screen, keyboard and controllers (the [seats add-on](install.md)) |
| Settings | the volume, mute and sound output; each screen's resolution; turning the screen off; the background |
| Status | the hostd version, video memory, and the latest actions with who sent them |

The hints at the bottom right name only what the current row does (open, options, close) and back where there is somewhere to go back to (an open folder, a filter, a panel).

### Tabs

A top-level folder can be a category of its own, after Apps, by listing it in `~/.config/hostd/menu.toml`:

```toml
[[tab]]
folder = "Games"
icon = "gamepad-2"   # one of the menu's icons (deploy/files/shell/icons); the folder icon otherwise

[[tab]]
folder = "Services"
icon = "server"
```

A tab lists its folder like Apps lists the top: the running apps, the subfolders, then the apps. Back goes up within it and, at its top, to Apps. Apps no longer shows a folder that is a tab. The menu reads the file again when it changes; a file it cannot use gives no tabs, and the menu says why. hostd ships no tabs: the file is the user's.

The menu acts with the machine's local token (`~/.config/hostd/local.token`, scopes `read,apps,audio,display`, and `seat` with the seats add-on), so what is chosen on the screen counts as someone at the screen.

## Tests

`make test-shell` runs, in a Debian 13 container with the shell's Qt stack (`test/shell/Containerfile`, so rendering is the same everywhere):

| Test | Holds |
| --- | --- |
| `test_layout.py` | [the grid](#the-grid) on the real QML, at 1280×800, 1920×1080 and 3840×2160, in every category and while moving: alignment within 1 px, no overlap, the rhythm, the corners, text that fits |
| `test_golden.py` | every screen state against images in `test/shell/golden/`; what it rendered goes to `test/shell/out/index.html` for review. `make shell-golden` accepts a new look |
| `test_behaviour.py` | what each verb does, the requests sent to hostd, and the background's contrast at every month and hour |
| `test_lint.py` | qmllint: no warnings |
| `live.sh` | a real Wayland session: the launcher, the menu's app_id, the layers, the notification server |
