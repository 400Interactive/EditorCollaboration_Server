---
description: >-
  What the server stores, how to back up collab.db, how to update, and what each
  error means.
---

# Maintenance

## What is stored

Everything is in the `-data` directory.

| File                                                                | Content                                                                                                    |
| ------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `collab.db`, plus `collab.db-wal` and `collab.db-shm` while running | The last 10,000 chat messages of each room, up to 2,000 comment threads, and modified-file marks (30 days) |
| `server.log` and `server.log.1`                                     | Logs, when you use `-detach` or `-log`. Rotated at 10 MB.                                                  |
| `server.json`                                                       | PID and the token used by `-stop`. Deleted on shutdown.                                                    |

Presence (who is connected, where, what they select) is never stored.

## Back up

With the server stopped (`-stop`, `systemctl stop`, or `docker compose stop`), copy `collab.db`.

To back up without stopping, use the `sqlite3` tool:

```sh
sqlite3 /path/to/data/collab.db ".backup /path/to/backups/collab-$(date +%F).db"
```

To restore: stop the server, replace `collab.db` with the copy, and delete `collab.db-wal` and `collab.db-shm` if they are still there.

## Update

The plugin and the server exchange a protocol number. If they differ, the plugin shows "The plugin and server versions are incompatible: update one of them."

{% hint style="warning" %}
Plugin 1.3 and server 1.3 use protocol 6. Neither connects to a 1.2 version of the other, so update the server and every editor at the same time. `collab.db` is kept. Modified-file marks from 1.2 are taken over by their author on the next save or Git change. Comment messages written with 1.2 can no longer be edited.
{% endhint %}

On startup, and then once a day, the server asks GitHub whether a [newer release](https://github.com/400Interactive/EditorCollaboration_Server/releases) exists. It writes a log line. On Windows it also shows a notification. Nothing is downloaded until you ask.

```sh
collab-server -update
collab-server -check-update
```

`-update` checks the `SHA256SUMS` file published with the release, replaces the executable, and restarts the systemd service when that service is already running. The database is kept. `-check-update` only prints the result and exits: `0` when the server is up to date, `2` when a newer release exists.

On Debian, `sudo sh install-debian.sh --update` does the same replacement. Set `COLLAB_AUTO_UPDATE=1` while installing to download and restart once a day. Leave that variable out, or set it to `0`, on a later run and the daily install is turned off. `COLLAB_UPDATE_CHECK=0` turns the check off entirely.

In the Unreal editor, the Collaborators panel and a toast report a server update and a plugin update. The plugin link opens `MarketplaceURL` from the `.uplugin` that was current when the release was built. **Notify Server Update** and **Notify Plugin Update** in the personal plugin settings turn each toast off.

A Docker container only logs that a release exists. Update it with `docker compose up -d --build`.

`collab.db` is kept. `room-*.json` files from version 1.1 and earlier are imported on the first start, then renamed to `.imported`.

## Troubleshooting

| What you see                                                              | Likely cause                                                                                               | Fix                                                                                              |
| ------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| The Collaborators panel stays on "Connecting to ..."                      | Address, port, or firewall                                                                                 | Open `http://<server>:7878/health` from that person's PC. Check **Server URL** and the firewall. |
| Wrong password.                                                           | The two passwords differ                                                                                   | **Password** in Editor Preferences must be exactly `COLLAB_PASSWORD`.                            |
| The plugin and server versions are incompatible                           | Plugin and server at different versions, for example a 1.2 editor and a 1.3 server                         | Update both. See [Update](maintenance.md#update) above.                                          |
| The room is full.                                                         | `-max-room-clients` reached                                                                                | Raise the limit. The default is 64.                                                              |
| `bind: Only one usage of each socket address` or `address already in use` | Another program, or another copy of the server, holds the port                                             | Stop the other one with `-stop`, or change the port with `-addr :7879`.                          |
| `-detach` says a server is already running                                | Something already answers on that port                                                                     | `-stop` with the same `-data`, or keep the server that is already up.                            |
| `-stop` says no server is running with that data directory                | `-data` does not match the running process                                                                 | Pass the same absolute `-data` path.                                                             |
| Messages or comments have disappeared                                     | The server was started with another `-data` directory. A relative `data` depends on the current directory. | Find the `collab.db` files on the machine and restart with the right absolute `-data`.           |
| Two copies of the project show the same person's marks                    | `Saved/EditorCollaboration/ClientId.txt` was copied with the project                                       | Delete that file on one copy. The plugin creates a new one on the next connection.               |
| The server cannot read its database.                                      | Disk full, permissions, or a damaged file                                                                  | Read the logs. Restore a backup of `collab.db` if needed.                                        |
| Windows: no icon in the taskbar                                           | Started without a session (`SYSTEM` task) or with `-no-tray`                                               | Expected. Start it at sign-in if you want the icon. See [Windows](windows.md).                   |
