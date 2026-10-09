# Server with Docker

Works wherever Docker runs: a Linux server, a NAS (Synology, Unraid...), Docker Desktop on Windows or Mac.
The image is minimal (just the executable) and starts with the machine.

## 1. Start

From this repository, set a password and start. Compose refuses to start without `COLLAB_PASSWORD` (there is no default):

```sh
COLLAB_PASSWORD='a-long-secret' docker compose up -d
```

The published port is `127.0.0.1:7878` (this machine only). To reach it from other computers on a trusted LAN, change the port to `"7878:7878"` in `docker-compose.yml`. Do not publish `ws://` on the Internet: keep the port on loopback and put TLS in front, see [internet.md](internet.md).

The image is built on the spot the first time (one or two minutes). The data lives in the `collab-data` Docker
volume and survives container updates.

## 2. Day to day

| Action | Command |
| --- | --- |
| Status (`healthy` after a few seconds) | `docker compose ps` |
| Live logs | `docker compose logs -f` |
| Stop / start again | `docker compose stop` / `docker compose start` |
| After editing `docker-compose.yml` | `docker compose up -d` |
| Update the server (new plugin version) | `docker compose up -d --build` |

The container logs when a newer GitHub release exists. It does not replace its own executable: rebuild the image.

`docker compose stop` stops cleanly: pending data is saved.

## 3. Network

By default only this machine can connect (`127.0.0.1:7878`). To use another port, change the left side
(`"127.0.0.1:9000:7878"`) and the plugin's **Server Url** (`ws://127.0.0.1:9000/ws`). If you publish it beyond
loopback, open that port in the host's firewall
(see the [Linux](linux.md#4-open-the-firewall) or [Windows](windows.md#3-open-the-firewall) guide).

Behind a reverse proxy on the same machine, publish the port locally only (`"127.0.0.1:7878:7878"`) and let the
proxy handle HTTPS: see [internet.md](internet.md).

## 4. Back up the database

```sh
docker compose stop
docker run --rm -v "$(docker volume ls -q | grep collab-data)":/data -v "$PWD":/backup alpine cp /data/collab.db /backup/collab-backup.db
docker compose start
```

To restore: same command with the copy reversed (`cp /backup/collab-backup.db /data/collab.db`), container
stopped.
