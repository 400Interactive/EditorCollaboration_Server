---
description: >-
  Install and run the Editor Collaboration server. One executable, SQLite
  included, one room per project.
---

# Server

Every collaborator connects to one small server. It is a single executable. SQLite is built in, so there is nothing else to install. One server can host several projects. Each project gets its own room.

![One small server. The editor only needs a Server URL and a room name.](https://3124660912-files.gitbook.io/~/files/v0/b/gitbook-x-prod.appspot.com/o/spaces%2FCzO6BrBWHeqbWYPwcAp9%2Fuploads%2FpT0AWQmfDBiohpMwQ38H%2Fself-hosted.jpg?alt=media)

<table data-view="cards"><thead><tr><th></th><th></th><th></th><th data-hidden data-card-target data-type="content-ref"></th></tr></thead><tbody><tr><td><h3><i class="fa-windows" style="color:$primary;">:windows:</i></h3></td><td><strong>Windows</strong></td><td>Run it on a PC, with a taskbar icon or a scheduled task.</td><td><a href="windows.md">windows.md</a></td></tr><tr><td><h3><i class="fa-linux" style="color:$primary;">:linux:</i></h3></td><td><strong>Linux</strong></td><td>A PC, a server, or a Raspberry Pi, with systemd.</td><td><a href="linux.md">linux.md</a></td></tr><tr><td><h3><i class="fa-apple" style="color:$primary;">:apple:</i></h3></td><td><strong>macOS</strong></td><td>Apple Silicon or Intel, including launchd at sign-in.</td><td><a href="macos.md">macos.md</a></td></tr><tr><td><h3><i class="fa-docker" style="color:$primary;">:docker:</i></h3></td><td><strong>Docker</strong></td><td>Any machine with Docker, including a NAS.</td><td><a href="docker.md">docker.md</a></td></tr><tr><td><h3><i class="fa-globe" style="color:$primary;">:globe:</i></h3></td><td><strong>Internet</strong></td><td>VPN, HTTPS with Caddy, or a certificate on the server.</td><td><a href="internet.md">internet.md</a></td></tr><tr><td><h3><i class="fa-life-ring" style="color:$primary;">:life-ring:</i></h3></td><td><strong>Maintenance</strong></td><td>Backups, updates, and the errors you are likely to see.</td><td><a href="maintenance.md">maintenance.md</a></td></tr><tr><td><h3><i class="fa-list" style="color:$primary;">:list:</i></h3></td><td><strong>Options</strong></td><td>Flags and environment variables.</td><td><a href="options.md">options.md</a></td></tr></tbody></table>

## Short path

{% stepper %}
{% step %}
From the server repository, run `./build.ps1` on Windows or `./build.sh` on Linux and Mac. You need [Go](https://go.dev/dl/) or Docker. The binaries land in `dist/`.

| File                              | Machine                                          |
| --------------------------------- | ------------------------------------------------ |
| `collab-server-windows-amd64.exe` | 64-bit Windows                                   |
| `collab-server-linux-amd64`       | Linux on Intel or AMD                            |
| `collab-server-linux-arm64`       | 64-bit ARM Linux, including Raspberry Pi 4 and 5 |
| `collab-server-darwin-arm64`      | Apple Silicon (M1 and later)                     |
| `collab-server-darwin-amd64`      | Intel Mac                                        |

You can build on one machine and copy the executable to another.
{% endstep %}

{% step %}
Set `COLLAB_PASSWORD`, or pass `-password`. Share that password with the people who will connect. An empty password is accepted, and it is a bad idea as soon as the port is reachable.
{% endstep %}

{% step %}
Pass `-data` as an absolute path. A double-click, a shortcut, and a scheduled task do not share a working directory, so a relative `data` folder can silently become a second, empty database.
{% endstep %}

{% step %}
Allow it in the machine firewall. The plugin **Server URL** is `ws://<address>:7878/ws`.
{% endstep %}

{% step %}
Open `http://<address>:7878/health` in a browser. A healthy server returns JSON with `"status":"ok"` and a `protocolVersion`. The plugin and the server must report the same protocol version.
{% endstep %}
{% endstepper %}

{% hint style="warning" %}
After you update the plugin, rebuild and restart the server. If the protocol numbers differ, the editor shows that the plugin and server versions are incompatible.
{% endhint %}

{% hint style="info" %}
A running server checks [GitHub Releases](https://github.com/400Interactive/EditorCollaboration_Server/releases) and can replace itself. The steps are on [Maintenance](maintenance.md).
{% endhint %}
