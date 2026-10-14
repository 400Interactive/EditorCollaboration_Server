---
description: Install the collaboration server on macOS 12 or later, Apple Silicon or Intel.
---

# macOS

macOS 12 or later. Use `collab-server-darwin-arm64` on Apple Silicon (M1 and later), and `collab-server-darwin-amd64` on an Intel Mac. Apple menu, then About This Mac, tells you which one you have.

{% stepper %}
{% step %}
From the server repository:

```sh
./build.sh
mkdir -p ~/EditorCollab
cp dist/collab-server-darwin-arm64 ~/EditorCollab/collab-server
chmod +x ~/EditorCollab/collab-server
```

The executable is not signed. Built on that Mac, it runs directly. Copied from another machine (download, AirDrop, or a messaging app), macOS blocks it. Remove the quarantine flag once:

```sh
xattr -d com.apple.quarantine ~/EditorCollab/collab-server
```
{% endstep %}

{% step %}
```sh
COLLAB_PASSWORD=my-password ~/EditorCollab/collab-server -data ~/EditorCollab/data
```

On the first start, macOS asks to allow incoming connections. Accept. Otherwise open System Settings, then Network, then Firewall, then Options.

In the background, without keeping Terminal open:

```sh
COLLAB_PASSWORD=my-password ~/EditorCollab/collab-server -detach -data ~/EditorCollab/data
~/EditorCollab/collab-server -stop -data ~/EditorCollab/data
```

The address is under System Settings, Network, then your Wi-Fi or Ethernet, then Details. Or run `ipconfig getifaddr en0`. **Server URL** is `ws://192.168.1.30:7878/ws`, with your address in place of the example.
{% endstep %}

{% step %}
Create `~/Library/LaunchAgents/com.editorcollab.server.plist`. Replace `me` with your user name.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.editorcollab.server</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/me/EditorCollab/collab-server</string>
    <string>-data</string>
    <string>/Users/me/EditorCollab/data</string>
    <string>-log</string>
    <string>/Users/me/EditorCollab/data/server.log</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>COLLAB_PASSWORD</key>
    <string>my-password</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
</dict>
</plist>
```

`KeepAlive` with `SuccessfulExit` false restarts the server after a crash, and does not restart it after a stop requested with `-stop`.

```sh
chmod 600 ~/Library/LaunchAgents/com.editorcollab.server.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.editorcollab.server.plist
launchctl bootout gui/$(id -u)/com.editorcollab.server
```

The first `launchctl` line enables it. The second disables it. Do not pass `-detach`: launchd already runs it in the background.
{% endstep %}
{% endstepper %}

{% hint style="warning" %}
The Mac must stay on and awake (System Settings, Lock Screen and Energy) or the other people lose the connection. The plist contains the password, which is why it is mode `600`.
{% endhint %}
