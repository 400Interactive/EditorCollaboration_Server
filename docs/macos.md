# Server on macOS

macOS 12 or later. `collab-server-darwin-arm64` for an Apple Silicon Mac (M1 and later),
`collab-server-darwin-amd64` for an Intel Mac (Apple menu > About This Mac).

## 1. Install

From this repository:

```sh
./build.sh
mkdir -p ~/EditorCollab
cp dist/collab-server-darwin-arm64 ~/EditorCollab/collab-server
chmod +x ~/EditorCollab/collab-server
```

The executable is not signed. Built on the Mac itself, it runs directly. Copied from another machine (download,
AirDrop, messaging app), macOS blocks it: remove the quarantine flag once.

```sh
xattr -d com.apple.quarantine ~/EditorCollab/collab-server
```

## 2. Start

```sh
COLLAB_PASSWORD=my-password ~/EditorCollab/collab-server -data ~/EditorCollab/data
```

On the first start, macOS asks whether to **allow incoming connections**: accept. Otherwise:
System Settings > Network > Firewall > Options.

In the background, without keeping the Terminal open:

```sh
COLLAB_PASSWORD=my-password ~/EditorCollab/collab-server -detach -data ~/EditorCollab/data
~/EditorCollab/collab-server -stop -data ~/EditorCollab/data
```

Address to give: System Settings > Network > (Wi-Fi or Ethernet) > Details, or `ipconfig getifaddr en0`, so
**Server Url** = `ws://192.168.1.30:7878/ws`.

## 3. Automatic start (launchd)

Create `~/Library/LaunchAgents/com.editorcollab.server.plist`, replacing `me` with your user name:

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
	<!-- Restarts after a crash, but not after a stop requested with -stop. -->
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
</dict>
</plist>
```

```sh
chmod 600 ~/Library/LaunchAgents/com.editorcollab.server.plist   # contains the password
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.editorcollab.server.plist   # enable
launchctl bootout gui/$(id -u)/com.editorcollab.server                                   # disable
```

The server then starts at every sign-in. No `-detach` here: launchd already runs it in the background.
The Mac must stay on and awake (System Settings > Lock Screen / Energy) for the others to stay connected.
