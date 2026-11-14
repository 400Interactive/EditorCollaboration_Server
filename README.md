# Editor Collaboration Rust Server

The server implements protocol 9 of the Unreal Engine plugin. It supports presence, chat, comments, pings, work markers, package publishing and deletion, graph and level synchronization, a replay journal, and file transfers. It uses the same SQLite schema and blobs directory layout as the Go server.

[Editor Collaboration documentation](https://docs.400interactive.com/editor-collaboration/)

## Getting started on Windows

Install stable Rust and the Visual Studio C++ build tools, then run the following from the root of this repository:

```powershell
cargo build --locked --release
./target/release/collab-server.exe -addr 127.0.0.1:7878 -data ./data
```

In the plugin, enter the room name and connect to `ws://127.0.0.1:7878/ws`. If the Go server is already listening on port 7878, use a different port and a separate data directory for this test.

The `-detach`, `-stop`, and `-healthcheck` options accept the same `-data` path as the server. On Windows, `-no-tray` hides the notification area icon. Use `-log` to also write output to a log file.

## Hosting the server

- For a password-protected room, set `COLLAB_ROOM` and `COLLAB_PASSWORD`. For multiple rooms, set `COLLAB_ROOM_PASSWORDS_FILE` to the path of a JSON file that maps each room name to its own password.
- Use `COLLAB_HOST`, `COLLAB_PORT`, `COLLAB_DATA_DIR`, and `COLLAB_MAX_ROOM_CLIENTS` to configure the bind address, port, data directory, and per-room client limit. The server requires a password to listen outside the loopback interface.
- To serve WSS directly, supply PEM files with `-tls-cert` and `-tls-key`. See the Caddy example in `deploy` for TLS termination at a reverse proxy.
- On Linux or macOS, run `cargo build --locked --release` to build `target/release/collab-server`. When this repository is checked out as the Unreal plugin's `Server` directory, the `build.sh` and `build.ps1` scripts can also read the parent plugin descriptor and write named binaries and SHA-256 checksums to `dist`. To build for a different target with those scripts, install the Rust target and its C compiler.
- For Docker, set `COLLAB_ROOM` and `COLLAB_PASSWORD`, then run `docker compose up -d --build` from this directory. Set `COLLAB_PUBLISHED_PORT` to change the host port.
- On Debian with systemd, run `sudo sh deploy/install-debian.sh <linux-binary>`. The Rust and Go servers use different service and data directory names. To allow remote clients, edit `/etc/editor-collab-rust.env` and restart the service.

The `-check-update` and `-update` commands look for the `collab-server-rust-<os>-<arch>` executable and its entry in `SHA256SUMS` in the GitHub releases repository configured by `COLLAB_GITHUB_REPO`. Windows binaries use the `.exe` extension. Set `COLLAB_AUTO_UPDATE=1` to enable automatic updates when the server process can write to its executable. For systemd installations, run `sudo sh deploy/install-debian.sh --update`. Set `COLLAB_UPDATE_CHECK=0` to disable periodic checks.

## Migrating data from Go

Stop the Go server, back up its entire data directory, then pass the copy to the Rust server with `-data`. The Rust server opens the SQLite database, adds missing columns, and imports legacy `room-*.json` files. Keep `collab.db` and the `blobs` directory together.

## Testing the protocol

After building the Windows executable, run:

```powershell
python -m pip install websockets==17.4
python tests/protocol.py target/release/collab-server.exe
```

On Linux or macOS, use `target/release/collab-server` as the executable path. To check migration from Go, pass the path to a compatible Go server executable as the second argument. The test uses Go to populate a temporary data directory, then uses Rust to read the data and replay the journal. Both scenarios use live HTTP and WebSocket connections.

## GitHub Actions

GitHub Actions builds and tests the Rust server on every pull request and push to `main`. The workflow run provides binary artifacts for Windows x64, Linux x64 and ARM64, and macOS Intel and Apple Silicon.

To publish a release, push a tag matching the version in `Cargo.toml`, such as `1.4.0` or `v1.4.0`. After all tests pass, the workflow publishes all five binaries and the `SHA256SUMS` file.
