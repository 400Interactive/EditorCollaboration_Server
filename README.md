# Setting up the Editor Collaboration server

The plugin needs a small server that every collaborator can reach. It is a single executable with no dependencies:
the database (SQLite) is built in. One server can host several projects, each project gets its own "room".

## Which guide?

| The server runs on... | Guide |
| --- | --- |
| A Windows PC (yours or a colleague's) | [windows.md](docs/windows.md) |
| A Linux server or PC, a Raspberry Pi | [linux.md](docs/linux.md) |
| A Mac | [macos.md](docs/macos.md) |
| Any machine with Docker | [docker.md](docs/docker.md) |

Collaborators outside your local network? Then read [internet.md](docs/internet.md) (HTTPS, VPN, port forwarding).
Backups, updates and common problems: [maintenance.md](docs/maintenance.md).

## In short

1. **Get the executable.** From this repository, run `./build.ps1` (Windows) or `./build.sh`
   (Linux, Mac). It needs [Go](https://go.dev/dl/) or Docker. The executables land in `dist/`:

   | File | For |
   | --- | --- |
   | `collab-server-windows-amd64.exe` | 64-bit Windows |
   | `collab-server-linux-amd64` | Linux on a PC or server (Intel, AMD) |
   | `collab-server-linux-arm64` | 64-bit ARM Linux (Raspberry Pi 4/5, ARM servers) |
   | `collab-server-darwin-arm64` | Apple Silicon Mac (M1 and later) |
   | `collab-server-darwin-amd64` | Intel Mac |

   You can build on one machine and copy the executable to another: there is nothing else to install.

2. **Configure a room and password** with `COLLAB_ROOM` and `COLLAB_PASSWORD` (or `-room` and `-password`). Use the effective room name from the Unreal project settings.
3. **Start the server** with an absolute data directory (`-data`), see the guide for your platform.
4. **Open port 7878** (TCP) in the machine's firewall.
5. **Configure the plugin** in Unreal:
   - `Project Settings > Plugins > Editor Collaboration`: **Server Url** = `ws://<server address>:7878/ws`
     (committed in `Config/DefaultEditor.ini`, done once for the whole team);
   - `Editor Preferences > Plugins > Editor Collaboration (Personal)`: **Password**, name and color (per person).

To check that the server answers, open `http://<server address>:7878/health` in a browser: `{"status":"ok",...}`.

For multiple independent rooms, set `COLLAB_ROOM_PASSWORDS_FILE` to a JSON file readable by the server.
For example, `{"ProjectA":"secret-for-a","ProjectB":"secret-for-b"}`. Use a distinct, strong password for
each room. Only listed rooms can join, and this file takes precedence over the single-room password.
Docker Compose uses `COLLAB_ROOM` and `COLLAB_PASSWORD` for its single-room example.

Protocol 9 requires a coordinated plugin and server update. Save live changes in Unreal and back up
SQLite and the blobs directory before migrating. Saved packages and old journal rows are retained on migration. The server withholds old content
without verified engine and Git metadata until someone publishes a fresh baseline.
Publishing a fresh baseline replaces that target’s pre-upgrade journal.

## Server options

Every option exists as a command-line argument and as an environment variable.

| Argument | Variable | Default | Purpose |
| --- | --- | --- | --- |
| `-addr` | `COLLAB_HOST`, `COLLAB_PORT` | `:7878` (all interfaces) | Listen address and port |
| `-password` | `COLLAB_PASSWORD` | empty | Password required from clients. Startup fails if it is empty unless `-addr` is loopback only (`127.0.0.1` or `::1`). Use `COLLAB_HOST=127.0.0.1` behind a TLS proxy. |
| `-room` | `COLLAB_ROOM` | empty | Required with `-password` unless the room-password file is configured. |
| `-room-passwords` | `COLLAB_ROOM_PASSWORDS_FILE` | empty | Path to a JSON object mapping allowed rooms to distinct passwords. |
| `-data` | `COLLAB_DATA_DIR` | `data` (relative to the current directory) | Database `collab.db`, logs, control file |
| `-log` | `COLLAB_LOG_FILE` | empty (`<data>/server.log` with `-detach`) | Also write the logs to a file |
| `-tls-cert`, `-tls-key` | `COLLAB_TLS_CERT`, `COLLAB_TLS_KEY` | empty | Direct HTTPS (`wss://`), see [internet.md](docs/internet.md) |
| `-max-room-clients` | `COLLAB_MAX_ROOM_CLIENTS` | `64` | Maximum people per room |
| `-no-tray` | `COLLAB_NO_TRAY=1` | icon shown | Windows: no icon in the taskbar notification area |
| | `COLLAB_LANG` | OS language | Server UI and logs language (e.g. `fr`, `de`, `ja`) |
| `-detach` | | | Start in the background without a window, then return |
| `-stop` | | | Cleanly stop the server that uses the same `-data` |
| `-healthcheck` | | | Exit code 0 if the local server answers (monitoring) |
| `-check-update` | | | Print whether a newer GitHub release exists, then exit (0 up to date, 2 if one is available) |
| `-update` | | | Download the latest GitHub release and replace this executable |
| `-auto-update` | `COLLAB_AUTO_UPDATE=1` | off | Install a newer release when the periodic check finds one |
| | `COLLAB_UPDATE_CHECK=0` | on | Skip the periodic check and its log line |
| | `COLLAB_UPDATE_INTERVAL` | `24h` | How often to check GitHub (for example `12h`) |
| | `COLLAB_GITHUB_REPO` | `400Interactive/EditorCollaboration_Server` | Release repository (`owner/name`) |

`-data` is relative to the directory the server is started from: a double-click, a shortcut or a scheduled task
do not necessarily use the same one. Always give an absolute path, and the same one to `-detach` and `-stop`.

The plugin and the server must use the same protocol version (`protocolVersion` in `/health`). After updating the
plugin, rebuild and restart the server.
