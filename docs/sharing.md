# Sharing screens

Core's screen can show another device, and another device can show core's screen. Every piece is an [add-on](install.md#more-users) made of free software, and none of it is built into hostd: they are apps and a service like any other.

| I want to... | Use | Add-on |
| --- | --- | --- |
| See and control **core** from a PC or phone, with low delay | Sunshine on core, **Moonlight** on the device (Linux, Windows, Mac, Android, iOS) | `sunshine` |
| Show a **PC** on core, controlled from core's keyboard and mouse | Sunshine on the PC, Moonlight on core | `moonlight` |
| Show an **iPhone or iPad** (or a Mac) on core | AirPlay mirroring (UxPlay) | `phones` |
| Show an **Android phone** on core and control it from core | scrcpy over Wi-Fi | `phones` |

Core is a source (Sunshine) or a receiver (the others); using both at once is possible but pointless.

```sh
sudo hostd-admin add sunshine --user screen     # root, once: the package and the service
sudo hostd-admin add phones --user screen       # root, once: UxPlay, scrcpy, adb
hostd-setup add moonlight                       # a Flatpak, no root
```

## Core on a PC or phone (Sunshine and Moonlight)

1. Install Moonlight on the device: <https://moonlight-stream.org> (Android and iOS stores; Flathub `com.moonlight_stream.Moonlight` on Linux).
2. Open `https://core.local:47990` once. It asks you to make a user name and a password. That page and the PIN page answer the home network only.
3. In Moonlight add the host `core` (or its address). It shows a 4-digit PIN: enter it under **PIN** on that page.
4. Start **Desktop**. The stream is core's screen; the device's keyboard, mouse or touch, and controllers, go to core.

Tips:
- Moonlight's resolution should match core's screen shape. The projector is 16:10 (1280x800): set a custom 1280x800 in Moonlight's video settings, or the picture is stretched.
- The Super key stays on your PC unless Moonlight is told to capture system shortcuts (Settings, Input). Core's own keys (Super+Q to close an app) then reach core. Without that, add a binding that your PC does not take, for example `"F1" = "display.menu"` in `[display.keys]` ([config](config.md)).
- Sunshine keeps its settings in `~/.config/sunshine`. Its web address is allowed by `csrf_allowed_origins` there: add another name or address if you open it by one that is not listed.

## A PC on core's screen

Install Sunshine on the PC (Windows, Mac and Linux packages, <https://github.com/LizardByte/Sunshine>), then on core open **Moonlight** from the menu, add the PC, pair with its PIN page, and start its desktop. Core's keyboard, mouse and controllers then drive the PC.

**Using core's screen as an extra monitor of the PC** is the same, with a virtual display on the PC, which depends on its system: on Windows a virtual display driver; on Linux a headless output (in Sway `swaymsg create_output`, or a dummy connector); on a Mac a dummy display tool. hostd does not set these up.

## iPhone and iPad (AirPlay)

Open **iPhone / iPad (AirPlay)** on core (menu, phone or `hostctl open airplay`). On the phone: Control Center, Screen Mirroring, **core**. The mirror opens full screen on core and ends when you stop it on the phone. Close the app on core to stop receiving. Both must be on the same network (mDNS must pass).

## Android (scrcpy)

Pair the phone once, with Android 11 or newer: Settings, Developer options, **Wireless debugging**, on. Then on core:

```sh
/usr/local/lib/hostd/phones/hostd-android pair HOST:PORT CODE    # Pair device with pairing code: its address and code
/usr/local/lib/hostd/phones/hostd-android connect HOST:PORT      # the address on the Wireless debugging screen itself
```

After that, open **Android phone** on core. The phone's screen is shown and core's keyboard and mouse control it (audio too, on Android 11 or newer). The phone's connection address changes when Wireless debugging is switched off and on; run `connect` again then. Over USB, no pairing is needed.

## Security

- Sunshine's web page and PIN page answer the home network only; a device must be paired by PIN, and the stream is encrypted. Anyone who can pair controls core, so keep the login to its web page private.
- `hostctl`/API access is separate (tokens). VNC (`wayvnc`) stays on localhost, for an SSH tunnel.
- UxPlay accepts any device on the network that asks (AirPlay mirroring has no PIN by default).
