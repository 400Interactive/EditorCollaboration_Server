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
server versions are incompatible: update one of them." After updating the plugin:

1. rebuild the server (`build.ps1` / `build.sh`, or `docker compose up -d --build`);
2. stop the old server, replace the executable, start it again with the same options.

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
