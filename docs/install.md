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

For a server without a screen, or to manage hostd with apt, install the package from a release:

```sh
sudo apt install ./hostd_<version>_amd64.deb
systemctl --user enable --now hostd
sudo loginctl enable-linger $USER     # start at boot, without logging in
```

Then list the modules you want in `~/.config/hostd/hostd.toml`, such as `modules = ["apps", "deploy", "automation"]`. With the package, apt updates hostd; `hostctl update` is off.

## Other devices

Every other device or script uses the API at `http://<machine>:7300`, which is open to the home network only. Each one gets its own token, limited to what it needs:

```sh
ssh screen@<machine> hostctl token create phone --scopes read,apps,audio
hostctl login <machine>:7300            # on a laptop, with that token
```

## Updates

Every build is signed. The installer checks the signature with the key it carries (`deploy/release-key.pem`), and refuses a build that does not match.

`hostd-update.timer` looks for a new build every few hours, as `/etc/hostd/update.conf` says:

```sh
CHANNEL=edge    # edge: every build of main that passed CI; stable: tagged releases
APPLY=auto      # auto | notify (a notice on the screen) | manual
```

With `auto`, the update starts once no deploy or scene is in progress and nobody is using the screen. If the machine is busy, the next check tries again. Updates restart hostd and the menu, but never the apps.

```sh
sudo hostd-setup update                       # the newest signed build, now
sudo hostd-setup addons                       # optional extras: Steam, Dolphin, compose, pterodactyl...
sudo hostd-setup add steam
sudo hostd-setup uninstall                    # removes hostd; keeps its data
sudo hostd-setup uninstall --purge --all      # ...and its data, and the base setup
```

If a new hostd fails to start three times, systemd rolls back to the previous version. The rollback also restores the state and the config from before the update.

From a laptop with a checkout, `hostctl update push <binary>` installs your own build. The rollback applies to it too.
