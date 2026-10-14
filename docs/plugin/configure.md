---
description: >-
  Project settings shared by the team, and the personal name, color, and
  password each person sets.
---

# Configure

Two places in Unreal. One is committed with the project. The other stays on each machine.

{% hint style="info" %}
The server has to be running before anyone can connect. Setup for each platform is in the [Server](../server/) section.
{% endhint %}

## Project settings

Shared by the whole team and saved in `Config/DefaultEditor.ini`.

`Project Settings > Plugins > Editor Collaboration`

![Server URL and room in Project Settings. The room is shared by the team.](https://3124660912-files.gitbook.io/~/files/v0/b/gitbook-x-prod.appspot.com/o/spaces%2FCzO6BrBWHeqbWYPwcAp9%2Fuploads%2FCD6MAWu3iITtJcJbLTY3%2Fproject-settings.jpg?alt=media)

| Setting        | What to put                                                                                                                                   |
| -------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| **Server URL** | `ws://192.168.1.10:7878/ws` on a local network, or `wss://collab.example.com/ws` behind HTTPS. The value must start with `ws://` or `wss://`. |
| **Room**       | Leave empty to use the project name. Set it when several projects should share a room, or when one project should be split.                   |

Commit that change once. Everyone who pulls the project then points at the same server.

## Personal settings

Stored under `Saved/` and never committed.

`Editor Preferences > Plugins > Editor Collaboration (Personal)`

On the first connection the plugin also creates `Saved/EditorCollaboration/ClientId.txt`. It keeps your messages, comments, and file marks yours across restarts. Do not copy it to another machine.

![Each person sets a display name and a color, and chooses which indicators to show.](https://3124660912-files.gitbook.io/~/files/v0/b/gitbook-x-prod.appspot.com/o/spaces%2FCzO6BrBWHeqbWYPwcAp9%2Fuploads%2FDfWKAnU98d15Xt97Qlxe%2Fpersonal-rules.jpg?alt=media)

### Identity and connection

![Display name, color, password, and the toggles for what you show and share.](https://3124660912-files.gitbook.io/~/files/v0/b/gitbook-x-prod.appspot.com/o/spaces%2FCzO6BrBWHeqbWYPwcAp9%2Fuploads%2FGI2xMdmJNTPlrto2loQE%2Fpersonal-settings.jpg?alt=media)

| Setting          | Default                    | Purpose                                                                                             |
| ---------------- | -------------------------- | --------------------------------------------------------------------------------------------------- |
| **Display Name** | Operating system user name | Name shown on badges, cameras, and chat.                                                            |
| **Color**        |                            | Avatar, camera, selection tint, and comments. The editor can warn you when someone already uses it. |
| **Password**     |                            | Must match `COLLAB_PASSWORD` on the server.                                                         |
| **Auto Connect** | On                         | Connect when the editor starts.                                                                     |

### What you show and share

| Setting                         | Default    | Purpose                                                                                       |
| ------------------------------- | ---------- | --------------------------------------------------------------------------------------------- |
| **Show In Viewport**            | On         | Cameras, names, and selection tint on the level.                                              |
| **Show In Graphs**              | On         | Selection frames and look-position in Blueprint, Material, and other graphs.                  |
| **Share Selection**             | On         | Send the actors and nodes you select.                                                         |
| **Selection Opacity**           | 0.3        | Strength of the tint on actors selected by someone else. `0` hides it.                        |
| **Show Cameras**                | On         | Camera frustum and name in the viewport.                                                      |
| **Show Graph Positions**        | On         | The dot that shows where someone is looking in a graph.                                       |
| **Show Comments**               | On         | Pins in the viewport and in graphs.                                                           |
| **Show Pings**                  | On         | Temporary look-here markers.                                                                  |
| **Ping Duration**               | 15 seconds | How long your own marker stays, from 10 to 20 seconds.                                        |
| **Show Sequencer Positions**    | On         | Marker on a Sequencer timeline.                                                               |
| **Show In-Game Badge**          | On         | Badge while a collaborator is in Play.                                                        |
| **Show Modified Assets**        | On         | Pencil on assets someone else changed.                                                        |
| **Show Locks**                  | On         | Padlock in the Content Browser and the lock button on an open asset.                          |
| **Show Git LFS Owners**         | On         | Name of the person who locked the file with Git LFS.                                          |
| **Show Git Branch**             | On         | Git branch each person is on.                                                                 |
| **Show Who Has It Open**        | On         | Avatars of people who have the current asset open.                                            |
| **Show Their Files**            | On         | Under each person in the panel, the files they modified or marked.                            |
| **Show Asset Filters**          | On         | Content Browser filters for files modified by others and for locks.                           |
| **Show Connection State**       | On         | Connected, reconnecting, or offline on the Collab button.                                     |
| **Show Hidden Selection Count** | On         | How many selected actors or nodes were not shared, past 64.                                   |
| **Away After**                  | 5 minutes  | Idle time before someone is shown as away.                                                    |
| **Warn If the Color Is Taken**  | On         | Offer another color when yours matches someone who is connected.                              |
| **Warn Before Save**            | On         | Ask before saving when someone else has the same file open and modified, or marked as theirs. |
| **Warn When Opening**           | On         | Notify when you open a file someone else is modifying or has marked.                          |
| **Release Soft Lock When Away** | On         | Drop your soft lock after you go away. Closing the editor always drops it.                    |
| **Load the Actor Cell**         | On         | When going to a comment, load the World Partition cell of its actor.                          |

### Comments list

| Setting                   | Default | Purpose                                       |
| ------------------------- | ------- | --------------------------------------------- |
| **Show Resolved**         | Off     | Include threads that are already resolved.    |
| **This Map**              | Off     | Only threads pinned on the open map.          |
| **This Graph**            | Off     | Only threads pinned on the focused graph.     |
| **Only Mine**             | Off     | Only threads you started.                     |
| **Comments on Assets**    | On      | Right-click an asset to start a thread on it. |
| **Comment Links in Chat** | On      | Turn a comment link in chat into a button.    |

### Notifications

| Setting                            | Default | Purpose                                                                               |
| ---------------------------------- | ------- | ------------------------------------------------------------------------------------- |
| **Notify Chat**                    | On      | Chat messages from other people. The unread counter stays active even if this is off. |
| **Notify Comments**                | On      | New comments and replies.                                                             |
| **Notify Pings**                   | On      | A look-here marker from someone else.                                                 |
| **Notify When a File Is Saved**    | On      | Someone saves a file you have open.                                                   |
| **Notify Mentions**                | On      | A comment mentions you.                                                               |
| **Notify Arrivals and Departures** | Off     | Someone connects or disconnects.                                                      |
| **Play a Sound**                   | Off     | A sound for a mention or a look-here marker.                                          |
| **Notify Server Update**           | On      | A toast when a newer server release exists.                                           |
| **Notify Plugin Update**           | On      | A toast when a newer plugin version exists. The link opens the Marketplace URL.       |

### Play

| Setting            | Default | Purpose                                                                                                                                        |
| ------------------ | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| **Play with Team** | Off     | The green Play button hosts or joins one shared game with people who have this map open. See [Work together](work-together.md#play-with-team). |

## Check the connection

Open `http://<server>:7878/health` from the same PC that runs the editor. Then open **Collaborators**. The panel leaves "Connecting to ..." once the socket is up.

| Message                                            | What to fix                                                                                    |
| -------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| The server URL must start with `ws://` or `wss://` | **Server URL** in Project Settings.                                                            |
| Wrong password.                                    | **Password** must match the server exactly.                                                    |
| The plugin and server versions are incompatible    | Update the plugin and the server together. See [Maintenance](../server/maintenance.md#update). |
| The room is full.                                  | Raise `-max-room-clients` (64 by default).                                                     |
| Invalid room name.                                 | Change **Room**, or leave it empty.                                                            |
| The server cannot read its database.               | See [Maintenance](../server/maintenance.md).                                                   |
