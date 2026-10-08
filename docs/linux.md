# Server on Linux

Any recent distribution, on a PC (`linux-amd64`) or 64-bit ARM such as a Raspberry Pi 4/5 (`linux-arm64`).
Nothing to install besides the executable.

## 1. Install

Build on any machine (`./build.sh` or `./build.ps1`), then copy the executable to the server:

```sh
scp dist/collab-server-linux-amd64 collab-server.service me@server:~
ssh me@server
sudo install -D -m 755 ~/collab-server-linux-amd64 /opt/editor-collab/collab-server
```

## 2. Try it

```sh
COLLAB_PASSWORD=my-password /opt/editor-collab/collab-server -data ~/editor-collab-data
```

`Ctrl+C` or closing the terminal stops the server cleanly. To keep it running after you log out of SSH, without
a systemd service:

```sh
COLLAB_PASSWORD=my-password /opt/editor-collab/collab-server -detach -data ~/editor-collab-data
/opt/editor-collab/collab-server -stop -data ~/editor-collab-data
```

## 3. systemd service (recommended)

Starts with the machine, restarts after a crash, and runs as an unprivileged user created by systemd.
The data goes to `/var/lib/editor-collab`.

```sh
# Password, readable by root only
sudo install -m 600 /dev/null /etc/editor-collab.env
echo 'COLLAB_PASSWORD=my-password' | sudo tee /etc/editor-collab.env > /dev/null

sudo cp ~/collab-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now collab-server
```

| Action | Command |
| --- | --- |
| Status | `systemctl status collab-server` |
| Live logs | `journalctl -u collab-server -f` |
| Stop / start | `sudo systemctl stop collab-server` / `sudo systemctl start collab-server` |
| After changing the password | `sudo systemctl restart collab-server` |

With systemd, use neither `-detach` nor `-stop`: `systemctl stop` already stops the server cleanly (the data is
saved). Other options (`COLLAB_PORT`, `COLLAB_MAX_ROOM_CLIENTS`...) go into `/etc/editor-collab.env`.

## 4. Open the firewall

```sh
sudo ufw allow 7878/tcp                                                           # Ubuntu, Debian (ufw)
sudo firewall-cmd --permanent --add-port=7878/tcp && sudo firewall-cmd --reload   # Fedora, RHEL (firewalld)
```

Address to give: `ip -4 addr` (for example `192.168.1.20`), so **Server Url** = `ws://192.168.1.20:7878/ws`.
On a server reachable from the Internet, prefer an HTTPS reverse proxy: see [internet.md](internet.md).
