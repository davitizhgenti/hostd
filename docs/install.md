# Install

## A screen machine (the full setup)

You need a fresh **Debian 13 (trixie)** install: minimal, with only "SSH server" and "standard system utilities" selected, and an admin user with sudo. If you log in to the admin user with an SSH key, the installer copies the key to the `screen` user too.

On the machine, as the admin user:

```sh
wget -qO- https://github.com/davitizhgenti/hostd/archive/refs/heads/main.tar.gz | tar xz
sudo ./hostd-main/deploy/install.sh
sudo reboot
```

The installer is safe to run again; it changes only what is missing. It sets up:

- the NVIDIA driver when an NVIDIA GPU is present; Sway, PipeWire, Podman and greetd; and the `screen` user, logged into Sway at boot;
- **hostd**, from the newest signed build, running as a service that starts at boot;
- **hostctl** in `/usr/local/bin`, already logged in for the `screen` user;
- `hostd-setup`, to update, add and remove add-ons, and uninstall;
- the update timer (see below).

These docs come with every build. The machine serves them at `http://<machine>:7300/docs/`, on the home network and without a token.

After the reboot the screen shows a dark background, and hostd is running:

```sh
ssh screen@<machine> hostctl apps --all
```

## A headless machine (the .deb)

The two are different layers, not two ways to do the same thing. `install.sh` (which fetches and runs `hostd-setup`, the installer) turns a fresh Debian into a whole hostd machine: the driver, Sway, PipeWire, the `screen` user, and hostd with its updates. The `.deb` is only hostd: the two programs and the user service, for a machine you set up yourself. Use one or the other on a machine, not both.

For a server without a screen, or to manage hostd with apt, install the package from a release:

```sh
sudo apt install ./hostd_<version>_amd64.deb
systemctl --user enable --now hostd
sudo loginctl enable-linger $USER     # start at boot, without logging in
```

Then list the modules you want in `~/.config/hostd/hostd.toml`, such as `modules = ["apps", "deploy", "automation"]`. With the package, apt updates hostd; `hostctl update` is off.

## Other devices

Every other device or script uses the API at `https://<machine>:7300`, which is open to the home network only. It is HTTPS with a certificate authority the machine made for itself at its first start, so no domain or outside CA is needed. Each device gets its own token, limited to what it needs:

```sh
ssh screen@<machine> hostctl token create laptop --scopes read,apps,audio
ssh screen@<machine> hostctl tls        # the CA's fingerprint
hostctl login <machine>                 # on the laptop: shows the same fingerprint, then asks for the token
```

`hostctl login` shows the machine's CA before anything is sent. Compare it with what `hostctl tls` printed, and confirm: hostctl then trusts that CA, and only it. A script gives it instead with `--ca-fingerprint`, and the Python client with `ca_fingerprint=`.

**Browsers.** To open `https://<machine>:7300/docs/` (and add-ons' sites, such as the Pterodactyl panel) without warnings, install the CA on the laptop or phone: download `https://<machine>:7300/v1/tls/ca.pem`, check its fingerprint against `hostctl tls`, and add it as a trusted certificate authority (Firefox: Settings, Certificates, Import; GNOME: `sudo cp ca.pem /usr/local/share/ca-certificates/hostd.crt && sudo update-ca-certificates`; Android and iOS: open the file and install it as a CA certificate). The CA can only vouch for this machine's names and private addresses: it cannot be used to fake any other site. The docs also stay readable over plain `http://`; the API does not.

## From outside the house

Use a VPN, never a forwarded web port. There are two add-ons for it.

**WireGuard** (`wireguard`): a tunnel of your own, with no outside service. Forward one UDP port on your router to the machine; WireGuard answers only devices whose key you added, so to anyone else the port is silent.

```sh
sudo HOSTD_WIREGUARD_ENDPOINT=home.example.org hostd-setup add wireguard   # your public name or IP
sudo hostd-wg add phone                    # its config and a QR code: scan it in the WireGuard app
sudo hostd-wg add laptop --public-key KEY  # or a device that made its own key (wg genkey | wg pubkey)
sudo hostd-wg list                         # the devices and when each was last seen
sudo hostd-wg remove phone                 # a lost device can no longer connect
hostctl login 10.44.0.1                    # on the laptop, through the tunnel
```

The router forwards UDP 51820 (`HOSTD_WIREGUARD_PORT` changes it) to the machine. The tunnel is `10.44.0.0/24` (`HOSTD_WIREGUARD_NET`, a private /24, chosen at the first add), with the machine at `.1`; a private network, so hostd accepts it as it does the home network, and the machine's tunnel address goes into its certificate. A device's private key is made on the machine, shown once and not kept; with `--public-key`, it never leaves the device. The machine's own key is readable by root only. Devices reach the machine and nothing behind it: while the tunnel is up, a firewall table of its own (nftables, `inet hostd_wg`) drops anything forwarded from or to it, even with Docker's forwarding on. The configs it prints send only the tunnel's network through it, so a device's other traffic does not go through your home. `hostd-wg endpoint NAME` changes the address in the configs it prints later. Your internet connection needs an address the router can be reached at (not carrier-grade NAT).

**Tailscale** (`tailscale`): nothing to forward, but Tailscale's service introduces your devices to each other, and you log in with an account.

```sh
sudo hostd-setup add tailscale                               # prints a URL to approve the machine at
sudo HOSTD_TAILSCALE_AUTHKEY=tskey-auth-... hostd-setup add tailscale   # or join with an auth key
hostctl login 100.x.y.z                                      # on the laptop: the machine's tailnet address
```

The add-on adds the tailnet's addresses to `[api] allow` (in a drop-in file it removes again), and the machine's tailnet address goes into its certificate, so the same pinned login works at home and away. A router with WireGuard of its own works too, with nothing to change in hostd: its clients get home-network addresses.

## Updates

Every build is signed. The installer checks the signature with the key it carries (`deploy/release-key.pem`), and refuses a build that does not match.

`hostd-update.timer` looks for a new build every few hours, as `/etc/hostd/update.conf` says:

```sh
CHANNEL=edge    # edge: every build of main that passed CI; stable: tagged releases
APPLY=auto      # auto | notify (a notice on the screen) | manual
```

With `auto`, the update starts once no deploy or scene is in progress and nobody is using the screen. If the machine is busy, the next check tries again. Updates restart hostd and the menu, but never the apps.

```sh
sudo hostd-setup update                       # the newest signed build, now; prints only what changed
sudo hostd-setup update --verbose             # ...every step, changed or not
sudo hostd-setup update steam                 # ...and run that add-on again now
sudo hostd-setup update --all                 # ...and run every installed add-on again now
sudo hostd-setup addons                       # optional extras: Steam, Dolphin, compose, pterodactyl...
sudo hostd-setup add steam
sudo hostd-setup uninstall                    # removes hostd; keeps its data
sudo hostd-setup uninstall --purge --all      # ...and its data, and the base setup
```

An update checks the whole machine setup (packages, the screen user, the Sway and mako configs, hostd, the menu, the docs), which takes a moment, and prints only the steps that changed something or need attention, with a count of the rest. It runs the add-ons again only when the new build changed the installer or the add-ons themselves; otherwise it says how many it left alone. An add-on's own upgrades (Flatpak apps and runtimes, the Pterodactyl panel) happen when you run it with `update NAME` or `update --all`. The update timer runs every add-on, except that it never upgrades the Pterodactyl panel (its migrations change data).

If a new hostd fails to start three times, systemd rolls back to the previous version. The rollback also restores the state and the config from before the update.

From a laptop with a checkout, `hostctl update push <binary>` installs your own build. The rollback applies to it too.
