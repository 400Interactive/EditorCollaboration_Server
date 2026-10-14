---
description: >-
  Install the collaboration server on Linux or a Raspberry Pi, and run it with
  systemd.
---

# Linux

Any recent distribution, on a PC (`linux-amd64`) or 64-bit ARM such as a Raspberry Pi 4 or 5 (`linux-arm64`). Nothing to install besides the executable.

## Install

On Debian 11, 12, or 13, one command downloads the executable and registers a systemd service:

```sh
curl -fsSL https://raw.githubusercontent.com/400Interactive/EditorCollaboration_Server/main/deploy/install-debian.sh | sudo bash
```

The script asks for the password. Or pass it up front: `sudo COLLAB_PASSWORD='my-password' bash`. Running it again updates the executable and keeps the existing password. `sudo sh install-debian.sh --update` only replaces the executable and restarts the service. `sudo COLLAB_AUTO_UPDATE=1 sh install-debian.sh` also checks GitHub once a day and installs a newer release. Omit the variable, or set it to `0`, on a later run and that daily install is turned off. Options are listed by `install-debian.sh --help`.

{% hint style="info" %}
The script expects the release assets `collab-server-linux-amd64` and `collab-server-linux-arm64`. Until those assets are published, build the binary yourself and copy it, as below.
{% endhint %}

To install by hand, build on any machine with `./build.sh` or `./build.ps1`, then copy the executable:

```sh
scp dist/collab-server-linux-amd64 deploy/collab-server.service me@server:~
ssh me@server
sudo install -D -m 755 ~/collab-server-linux-amd64 /opt/editor-collab/collab-server
```

Use `collab-server-linux-arm64` on a Raspberry Pi or another 64-bit ARM machine.

## Try it

```sh
COLLAB_PASSWORD=my-password /opt/editor-collab/collab-server -data ~/editor-collab-data
```

`Ctrl+C`, or closing the terminal, stops the server. To keep it running after you log out of SSH, without systemd:

```sh
COLLAB_PASSWORD=my-password /opt/editor-collab/collab-server -detach -data ~/editor-collab-data
/opt/editor-collab/collab-server -stop -data ~/editor-collab-data
```

## systemd

This is the setup to keep. The service starts with the machine, restarts after a crash, and runs as an unprivileged user created by systemd. Data goes to `/var/lib/editor-collab`.

```sh
sudo install -m 600 /dev/null /etc/editor-collab.env
echo 'COLLAB_PASSWORD=my-password' | sudo tee /etc/editor-collab.env > /dev/null
sudo cp ~/collab-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now collab-server
```

| Action                      | Command                                                                    |
| --------------------------- | -------------------------------------------------------------------------- |
| Status                      | `systemctl status collab-server`                                           |
| Live logs                   | `journalctl -u collab-server -f`                                           |
| Stop / start                | `sudo systemctl stop collab-server` / `sudo systemctl start collab-server` |
| After changing the password | `sudo systemctl restart collab-server`                                     |

With systemd, do not pass `-detach` or `-stop`. `systemctl stop` already stops the server and flushes the database. Other options (`COLLAB_PORT`, `COLLAB_MAX_ROOM_CLIENTS`, and the rest) go in `/etc/editor-collab.env`.

## Firewall

```sh
sudo ufw allow 7878/tcp
sudo iptables -I INPUT -p tcp --dport 7878 -j ACCEPT
sudo firewall-cmd --permanent --add-port=7878/tcp && sudo firewall-cmd --reload
```

Use the line that matches the firewall you actually run: ufw on Debian and Ubuntu, iptables on Debian without ufw, firewalld on Fedora and RHEL.

`install-debian.sh` opens the port itself: ufw when that firewall is active, otherwise iptables and ip6tables. iptables rules are saved with `netfilter-persistent` when it is installed, otherwise with `iptables-save`, and restored on boot.

The address to give is from `ip -4 addr`, for example `192.168.1.20`, so **Server URL** is `ws://192.168.1.20:7878/ws`.

On a machine reachable from the Internet, put HTTPS in front of it. See [Internet](internet.md).
