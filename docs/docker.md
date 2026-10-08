# Server with Docker

Works wherever Docker runs: a Linux server, a NAS (Synology, Unraid...), Docker Desktop on Windows or Mac.
The image is minimal (just the executable) and starts with the machine.

## 1. Start

From this repository, in `docker-compose.yml`, replace `change-me` with your password, then:

```sh
docker compose up -d
```

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

`docker compose stop` stops cleanly: pending data is saved.

## 3. Network

Port `7878` of the machine is published to the container. To use another one, change only the left side
(`"9000:7878"`) and the plugin's **Server Url** (`ws://<machine>:9000/ws`). Open that port in the host's firewall
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
