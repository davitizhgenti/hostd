# Sharing and remote control

Four apps, one table. Every one is an [app](apps.md) with an id that starts `share-`, so `hostctl apps` and the menu list them together. Each comes from one add-on of the same name. They work with **Linux PCs** and **Android phones**.

| | **Show on core** (core receives) | **Reach core** (core is the source) |
| --- | --- | --- |
| screen and input | `share-pc`: a Linux PC's screen on core, controlled from core.<br>`share-phone`: an Android phone's screen on core, controlled from core. | `share-remote`: a Linux PC or an Android phone shows and controls core. |
| input only | | `share-keys`: a PC's keyboard and mouse control core (no video). |

"Show" apps are windows: open them, close them like any app. "Reach" apps run in the background all the time (`hostctl ps`, `hostctl logs share-remote`, `hostctl stop share-keys`).

```sh
sudo hostd-admin add share-remote share-phone share-keys --user screen   # root, once: the packages
hostd-setup add share-pc                                                 # a Flatpak, no root
```

Each needs a one-time pairing, then nothing:

| App | Pair once with |
| --- | --- |
| `share-remote` | a PIN: open `https://core.local:47990` (make a login on the first visit), add core in Moonlight, enter the 4-digit PIN there |
| `share-pc` | a PIN: in the Moonlight app on core add the PC (it runs Sunshine) and enter the PIN on the PC |
| `share-phone` | Android's Wireless debugging (below) |
| `share-keys` | each side authorizes the other's certificate fingerprint (below) |

## share-remote: reach core from a PC or phone

Sunshine runs on core all the time. On the PC install Moonlight (Flathub `com.moonlight_stream.Moonlight`); on Android install Moonlight (Google Play or F-Droid). Pair once (above), then start **Desktop**: it is core's screen, and the device's keyboard, mouse or touch, and controllers, control core.

- Moonlight's resolution should match core's screen shape. The projector is 16:10 (1280x800): use a custom 1280x800 in Moonlight, or the picture is stretched.
- During a stream Sunshine sends the sound to the device. To hear it on the projector, turn on Moonlight's "play audio on host" setting.
- The Super key stays on your PC unless Moonlight captures system shortcuts (Settings, Input). Otherwise bind a key the PC does not take, for example `"F1" = "display.menu"` in `[display.keys]` ([config](config.md)).
- Sunshine's web page and PIN page answer the home network only. Its settings are in `~/.config/sunshine`; `csrf_allowed_origins` there lists the addresses its web page may be opened at.

## share-pc: a PC on core's screen

Install Sunshine on the PC (<https://github.com/LizardByte/Sunshine>). In the Moonlight app on core, add the PC and pair. Then name the PC once in the app's own file and open **Share: PC**:

```sh
hostctl edit app share-pc      # change "my-pc" to the PC's name or address
hostctl open share-pc          # full screen on core; core's keyboard, mouse and controllers drive the PC
```

A second PC: copy the file to another name (`share-pc2.toml`) and change the name in it.

**Using core as an extra monitor** of the PC is the same, with a second monitor on the PC to stream: a small HDMI dummy plug in the PC's HDMI port gives it one that Sunshine can stream, on any desktop. On Sway or Hyprland a headless output (`swaymsg create_output`) does the same without hardware. hostd does not set this up.

## share-phone: an Android phone on core's screen

Pair once (Android 11 or newer): Settings, Developer options, **Wireless debugging**, on. Then on core:

```sh
/usr/local/lib/hostd/share-phone/hostd-share-phone pair HOST:PORT CODE    # Pair device with pairing code: its address and code
/usr/local/lib/hostd/share-phone/hostd-share-phone connect HOST:PORT      # the address on the Wireless debugging screen itself
```

After that open **Share: phone** on core: the phone's screen is shown and core's keyboard and mouse control it (audio too, on Android 11 or newer). The connection address changes when Wireless debugging is switched off and on; run `connect` again then. Over USB no pairing is needed.

## share-keys: one keyboard and mouse for the PC and core

lan-mouse (v0.11.0). Push the mouse off the PC's screen edge and it appears on core's screen, with the keyboard following; a release key combination brings it back. Input only: no video, no clipboard, no audio, no controllers.

- On core `share-keys` runs and waits. Run the same program on the PC (the [release build](https://github.com/feschber/lan-mouse/releases/tag/v0.11.0) or its GUI), add core by its address on the side you want, and authorize each side's certificate fingerprint in the other's `~/.config/lan-mouse/config.toml` (`[authorized_fingerprints]`). Restart core's with `hostctl stop share-keys && hostctl start share-keys`.
- **A limit with a Sway core:** lan-mouse's notes say a wlroots receiver (Sway) does not handle modifier keys (Ctrl, Shift, Alt, Super) from a sender that does not use its layer-shell backend, which includes GNOME PCs. So from a GNOME PC the keys type but the modifiers may not work; hostd's Super bindings would not either. Try it before relying on it; `share-remote` has no such limit.
- On GNOME the PC may show a permission prompt for input capture.

## Security

- Sunshine's web page and PIN page answer the home network only; a device must be paired by PIN, and streams are encrypted. Anyone who can pair controls core, so keep the login to that page private.
- lan-mouse accepts only the fingerprints you authorize.
- `hostctl` and the API are separate (tokens). VNC (`wayvnc`) stays on localhost, for an SSH tunnel.
