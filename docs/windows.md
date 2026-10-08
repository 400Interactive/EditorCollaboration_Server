# Server on Windows

64-bit Windows 10 or 11. The examples use the `C:\EditorCollab` folder; adjust the paths.

## 1. Install

From this repository:

```powershell
./build.ps1
New-Item -ItemType Directory -Force C:\EditorCollab | Out-Null
Copy-Item dist\collab-server-windows-amd64.exe C:\EditorCollab\collab-server.exe
```

If PowerShell refuses to run the script: `powershell -ExecutionPolicy Bypass -File build.ps1`.

## 2. Start

**In a window** (to try it out): the logs are shown, and closing the window stops the server cleanly.

```powershell
C:\EditorCollab\collab-server.exe -data C:\EditorCollab\data -password "my-password"
```

**In the background** (normal use): no window, and the command returns as soon as the server answers.

```powershell
C:\EditorCollab\collab-server.exe -detach -data C:\EditorCollab\data -password "my-password"
C:\EditorCollab\collab-server.exe -stop -data C:\EditorCollab\data
```

The logs go to `C:\EditorCollab\data\server.log`.

The server shows up among the **hidden icons** of the taskbar (the arrow next to the clock). Hover: number of
connected collaborators. Click: **Open logs**, **Open data folder**, **Stop server**. To keep it always visible,
drag the icon from the hidden area onto the taskbar.

## 3. Open the firewall

On the first start, Windows asks whether to allow network access: check **Private networks** (and **Public
networks** only if needed). Otherwise, in an administrator PowerShell:

```powershell
New-NetFirewallRule -DisplayName "Editor Collaboration" -Direction Inbound -Protocol TCP -LocalPort 7878 -Action Allow -Profile Private
```

Address to give to the collaborators: `ipconfig` → **IPv4 Address** (for example `192.168.1.10`), so
**Server Url** = `ws://192.168.1.10:7878/ws`. A fixed address (DHCP reservation on the router) keeps it from
changing.

## 4. Automatic start

**At sign-in (with the icon)**: the simplest. `Win + R` → `shell:startup` → new shortcut:

```
C:\EditorCollab\collab-server.exe -detach -data C:\EditorCollab\data -password "my-password"
```

**At boot (without anyone signed in)**: a scheduled task, from an administrator PowerShell. It runs in the
background without an icon (there is no desktop at that point).

```powershell
schtasks /Create /TN "Editor Collaboration" /SC ONSTART /RU SYSTEM /TR "\"C:\EditorCollab\collab-server.exe\" -data C:\EditorCollab\data -log C:\EditorCollab\data\server.log -password my-password"
schtasks /Run /TN "Editor Collaboration"       # start it now
schtasks /Delete /TN "Editor Collaboration" /F # remove the task
```

To stop it: `C:\EditorCollab\collab-server.exe -stop -data C:\EditorCollab\data` (administrator PowerShell, since
the task runs as `SYSTEM`).

The password is visible in the shortcut or the task: keep these machines to trusted people, or set a system
environment variable `COLLAB_PASSWORD` instead (Advanced system settings > Environment Variables).
