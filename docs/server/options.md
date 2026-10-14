---
description: Every collaboration server flag and environment variable, with its default.
---

# Options

Every option exists as a command-line flag and as an environment variable.

| Flag                    | Variable                            | Default                                      | Purpose                                                                                          |
| ----------------------- | ----------------------------------- | -------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `-addr`                 | `COLLAB_HOST`, `COLLAB_PORT`        | `:7878` (all interfaces)                     | Listen address and port.                                                                         |
| `-password`             | `COLLAB_PASSWORD`                   | empty                                        | Password required from clients. Set one.                                                         |
| `-data`                 | `COLLAB_DATA_DIR`                   | `data`, relative to the current directory    | Database `collab.db`, logs, and the control file. Always pass an absolute path.                  |
| `-log`                  | `COLLAB_LOG_FILE`                   | empty, or `<data>/server.log` with `-detach` | Also write logs to a file.                                                                       |
| `-tls-cert`, `-tls-key` | `COLLAB_TLS_CERT`, `COLLAB_TLS_KEY` | empty                                        | Serve `wss://` directly. See [Internet](internet.md).                                            |
| `-max-room-clients`     | `COLLAB_MAX_ROOM_CLIENTS`           | `64`                                         | Maximum people in one room.                                                                      |
| `-no-tray`              | `COLLAB_NO_TRAY=1`                  | icon shown on Windows                        | Hide the taskbar notification icon.                                                              |
|                         | `COLLAB_LANG`                       | operating system language                    | Language of the server UI and logs, for example `fr`, `de`, or `ja`.                             |
| `-detach`               |                                     |                                              | Start in the background with no window, then return.                                             |
| `-stop`                 |                                     |                                              | Stop the server that uses the same `-data`.                                                      |
| `-healthcheck`          |                                     |                                              | Exit code 0 when the local server answers. For monitoring.                                       |
| `-check-update`         |                                     |                                              | Print whether a newer GitHub release exists, then exit. `0` is up to date, `2` means one exists. |
| `-update`               |                                     |                                              | Download the latest GitHub release and replace this executable.                                  |
| `-auto-update`          | `COLLAB_AUTO_UPDATE=1`              | off                                          | Install a newer release when the periodic check finds one.                                       |
|                         | `COLLAB_UPDATE_CHECK=0`             | on                                           | Skip the periodic check and its log line.                                                        |
|                         | `COLLAB_UPDATE_INTERVAL`            | `24h`                                        | How often to check GitHub, for example `12h`.                                                    |
|                         | `COLLAB_GITHUB_REPO`                | `400Interactive/EditorCollaboration_Server`  | Release repository, written as `owner/name`.                                                     |

`-data` is relative to the directory the process is started from. A double-click, a shortcut, and a scheduled task do not necessarily use the same one. Pass an absolute path, and pass that same path to `-detach` and to `-stop`.

`GET /health` returns JSON including `status`, `protocolVersion`, and, when known, `pluginVersion`. After a successful update check it also includes `updateAvailable`, `latestVersion`, and `releaseUrl`. `marketplaceUrl` is included when the release was built with one. The plugin refuses to stay connected when the protocol number does not match its own.
