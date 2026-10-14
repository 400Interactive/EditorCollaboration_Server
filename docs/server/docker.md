---
description: >-
  Run the collaboration server with Docker Compose on Linux, a NAS, or Docker
  Desktop.
---

# Docker

Works wherever Docker runs: a Linux server, a NAS (Synology, Unraid, and similar), or Docker Desktop on Windows or Mac. The image is the executable plus a minimal base. `restart: unless-stopped` brings it back with the machine.

{% stepper %}
{% step %}
In `docker-compose.yml`, replace `change-me` with your password, then:

```sh
docker compose up -d
```

The image builds the first time. That takes a minute or two. Data lives in the `collab-data` volume and survives image updates.

The compose file publishes `7878`, sets `COLLAB_PASSWORD`, `COLLAB_PORT`, and `COLLAB_MAX_ROOM_CLIENTS`, and mounts the volume at `/data` inside the container.
{% endstep %}

{% step %}
| Action                                 | Command                                        |
| -------------------------------------- | ---------------------------------------------- |
| Status (`healthy` after a few seconds) | `docker compose ps`                            |
| Live logs                              | `docker compose logs -f`                       |
| Stop / start                           | `docker compose stop` / `docker compose start` |
| After editing `docker-compose.yml`     | `docker compose up -d`                         |
| Update after a new plugin version      | `docker compose up -d --build`                 |

The container logs when a newer GitHub release exists. It does not replace its own executable. Rebuild the image with the last command in the table.

`docker compose stop` stops cleanly. Pending writes are saved.
{% endstep %}
{% endstepper %}

## Network

Port `7878` on the machine is published into the container. To use another host port, change only the left side (`"9000:7878"`) and the plugin **Server URL** (`ws://<machine>:9000/ws`). Open that port in the host firewall. The Windows and Linux pages cover the firewall commands.

Behind a reverse proxy on the same machine, publish the port on localhost only (`"127.0.0.1:7878:7878"`) and let the proxy terminate HTTPS. See [Internet](internet.md).

## Back up the database

```sh
docker compose stop
docker run --rm -v "$(docker volume ls -q | grep collab-data)":/data -v "$PWD":/backup alpine cp /data/collab.db /backup/collab-backup.db
docker compose start
```

To restore, stop the container and copy the file back (`cp /backup/collab-backup.db /data/collab.db`).
