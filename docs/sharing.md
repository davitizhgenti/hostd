# Sharing screens

Two ways, both over the home network (LAN), and nothing to install on the PC or the phone. Each is an add-on with an app that appears only for a PC or phone you have set up, so the menu never lists one that cannot work.

| I want to... | Use | Add-on |
| --- | --- | --- |
| Show a **Linux (GNOME) PC** on core's screen, controlled from core | the PC's own Remote Desktop (RDP) and a small RDP client on core | `share-pc` |
| Show an **Android phone** on core's screen, controlled from core | Android's Wireless debugging and scrcpy on core | `share-phone` |

```sh
sudo hostd-admin add share-pc share-phone --user screen    # root, once: the packages (freerdp, scrcpy, adb)
```

## share-pc: a Linux PC on core's screen

On the PC (GNOME): **Settings, System, Remote Desktop**, switch on **Desktop Sharing** and **Remote Control**, and note the user name and password shown there. Then on core:

```sh
/usr/local/lib/hostd/share-pc/hostd-share-pc add office 192.168.18.60 mylogin    # name, the PC's address, the Remote Desktop user; asks for the password
hostctl open share-office                                                     # the PC's screen, full screen on core
```

Core's keyboard and mouse control the PC. A PC has an app only after `add`; one app per PC. `hostd-share-pc list` and `hostd-share-pc remove office` manage them. The password is kept in `~/.config/hostd/share-pc` readable by you only; the PC's certificate is trusted the first time and checked after. Close the app on core to stop.

What you see is the PC's current screen. To use core as an extra monitor of the PC, the PC needs a second monitor to share: a small HDMI dummy plug in its HDMI port does that on any desktop; hostd does not set it up.

## share-phone: an Android phone on core's screen

Pair once (Android 11 or newer): Settings, Developer options, **Wireless debugging**, on. Then on core:

```sh
/usr/local/lib/hostd/share-phone/hostd-share-phone pair HOST:PORT CODE    # Pair device with pairing code: its address and code
/usr/local/lib/hostd/share-phone/hostd-share-phone connect HOST:PORT      # the address on the Wireless debugging screen itself
```

`connect` makes the app **Share: phone** (only once the phone really answers). Open it: the phone's screen is shown and core's keyboard and mouse control it (audio too, on Android 11 or newer). The connection address changes when Wireless debugging is switched off and on; run `connect` again then. `hostd-share-phone forget` removes the app. Over USB no pairing is needed.

## Not covered

Showing core on a PC or phone, other desktops than GNOME, Mac, Windows and iPhone, and Wi-Fi Direct casting (Smart View, Miracast): core has no Wi-Fi, and nothing free receives those reliably on Linux. A wireless display dongle on the projector's HDMI port does that without core.

## Security

The PC's Remote Desktop password protects its screen: use a strong one and switch Desktop Sharing off when you do not use it. Both ways stay on the home network. `hostctl` and the API are separate (tokens); VNC (`wayvnc`) on core stays on localhost, for an SSH tunnel.
