---
description: >-
  Install the collaboration server on 64-bit Windows 10 or 11, then start it at
  sign-in or at boot.
---

# Windows

64-bit Windows 10 or 11. The examples use `C:\EditorCollab`. Change the paths if you prefer another folder.

{% stepper %}
{% step %}
From the server repository:

```powershell
./build.ps1
New-Item -ItemType Directory -Force C:\EditorCollab | Out-Null
Copy-Item dist\collab-server-windows-amd64.exe C:\EditorCollab\collab-server.exe
```

If PowerShell refuses the script: `powershell -ExecutionPolicy Bypass -File build.ps1`.
{% endstep %}

{% step %}
In a window, while you try it. Logs stay on screen, and closing the window stops the server.

```powershell
C:\EditorCollab\collab-server.exe -data C:\EditorCollab\data -password "my-password"
```

In the background, for normal use. The command returns as soon as the server answers.

```powershell
C:\EditorCollab\collab-server.exe -detach -data C:\EditorCollab\data -password "my-password"
C:\EditorCollab\collab-server.exe -stop -data C:\EditorCollab\data
```

Logs go to `C:\EditorCollab\data\server.log`.
{% endstep %}

{% step %}
On the first start, Windows asks whether to allow network access. Check **Private networks**. Check **Public networks** only if you actually need them.

Otherwise, in an administrator PowerShell:

```powershell
New-NetFirewallRule -DisplayName "Editor Collaboration" -Direction Inbound -Protocol TCP -LocalPort 7878 -Action Allow -Profile Private
```

The address to give the team is the **IPv4 Address** from `ipconfig`, for example `192.168.1.10`. **Server URL** is then `ws://192.168.1.10:7878/ws`. A DHCP reservation on the router keeps that address from changing.
{% endstep %}

{% step %}
**At sign-in, with the icon.** Press `Win + R`, run `shell:startup`, and create a shortcut whose target is:

```
C:\EditorCollab\collab-server.exe -detach -data C:\EditorCollab\data -password "my-password"
```

**At boot, before anyone signs in.** A scheduled task, from an administrator PowerShell. It runs in the background with no icon, because there is no desktop yet.

```powershell
schtasks /Create /TN "Editor Collaboration" /SC ONSTART /RU SYSTEM /TR "\"C:\EditorCollab\collab-server.exe\" -data C:\EditorCollab\data -log C:\EditorCollab\data\server.log -password my-password"
schtasks /Run /TN "Editor Collaboration"
schtasks /Delete /TN "Editor Collaboration" /F
```

Stop that task with an administrator PowerShell, because it runs as `SYSTEM`:

```powershell
C:\EditorCollab\collab-server.exe -stop -data C:\EditorCollab\data
```
{% endstep %}
{% endstepper %}

## Taskbar icon

The server appears among the hidden icons of the taskbar (the arrow next to the clock). Hover shows how many people are connected. Click for **Open logs**, **Open data folder**, and **Stop server**. When a newer release exists, the menu adds **Install update** and a balloon appears. Choosing it replaces the executable and starts the server again. Drag the icon onto the taskbar if you want it always visible.

A server started as `SYSTEM`, or with `-no-tray`, has no icon. That is expected.

{% hint style="warning" %}
The password is visible in the shortcut or the scheduled task. Keep that PC to people you trust, or set a system environment variable `COLLAB_PASSWORD` instead (Advanced system settings, then Environment Variables) and leave `-password` off the command.
{% endhint %}
