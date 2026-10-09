# Backups, updates and troubleshooting

## What the server stores

Everything is in the `-data` directory:

| File | Content |
| --- | --- |
| `collab.db` (+ `collab.db-wal`, `collab.db-shm` while running) | Full chat history, comments, modified files |
| `server.log` (+ `server.log.1`) | Logs, with `-detach` or `-log` (rotated at 10 MB) |
| `server.json` | PID and token used by `-stop`, deleted on shutdown |

Presence (who is connected, where, what they select) is never stored.

## Backing up

With the server stopped (`-stop`, `systemctl stop`, `docker compose stop`), copy `collab.db`. To back up without
stopping it, use the [`sqlite3`](https://sqlite.org/download.html) tool:

```sh
sqlite3 /path/to/data/collab.db ".backup /path/to/backups/collab-$(date +%F).db"
```

To restore: with the server stopped, replace `collab.db` with the copy and delete `collab.db-wal` and
`collab.db-shm` if they exist.

## Updating

The plugin and the server exchange a protocol number. If they do not match, the plugin shows "The plugin and
server versions are incompatible: update one of them."

On startup, and then once a day, the server asks GitHub whether [a newer release](https://github.com/400Interactive/EditorCollaboration_Server/releases) exists. It only writes a log line (and, on Windows, a notification). Nothing is downloaded until you ask:

```sh
collab-server -update
collab-server -check-update
```

`-update` checks the `SHA256SUMS` file published with the release, replaces the executable, and restarts the systemd service when that service is already running. The database is kept. On Debian, from this repository, `sudo sh deploy/install-debian.sh --update` does the same thing. Once the script has been downloaded on its own, the command stays `sudo sh install-debian.sh --update`. Set `COLLAB_AUTO_UPDATE=1` while installing to download and restart once a day. `COLLAB_UPDATE_CHECK=0` turns the check off.

In the Unreal editor, the Collaborators panel and a toast report a server update and a plugin update. The plugin link opens `MarketplaceURL` from the `.uplugin` that was current when the release was built. **Notify Server Update** and **Notify Plugin Update** in the personal plugin settings turn each toast off.

A Docker container only logs that a release exists. Update it with `docker compose up -d --build`.

The `collab.db` database is kept as is. The `room-*.json` files of version 1.1 and earlier are imported on the
first start, then renamed to `.imported`.

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| The Collaborators panel stays on "Connecting to ..." | Address, port or firewall | Open `http://<server>:7878/health` from the collaborator's PC; check the **Server Url** and the firewall |
| "Wrong password." | Different password | The **Password** in Editor Preferences must be exactly `COLLAB_PASSWORD` |
| "The plugin and server versions are incompatible" | Plugin and server at different versions | See [Updating](#updating) |
| "The room is full." | `-max-room-clients` reached | Raise the limit (default 64) |
| At startup: `bind: Only one usage of each socket address` or `address already in use` | Another program (or another server) uses the port | Stop the other server (`-stop`), or change the port (`-addr :7879`) |
| `-detach`: "A server is already running" | A server already answers on that port | `-stop` with the same `-data`, or use the existing server |
| `-stop`: "No server is running with the data directory" | `-data` differs from the running server's | Pass exactly the same `-data` path (absolute) |
| Messages or comments have "disappeared" | Server started with another `-data` directory (a relative `data` depends on the current directory) | Look for `collab.db` files on the machine and restart with the right `-data` |
| "The server cannot read its database." | Disk full, permissions, damaged file | Check the logs; restore a backup of `collab.db` if needed |
| Windows: no icon in the taskbar | Started without a session (`SYSTEM` task) or with `-no-tray` | Expected; see [windows.md](windows.md#4-automatic-start) to start it at sign-in instead |
