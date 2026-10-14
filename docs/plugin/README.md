---
description: >-
  What the Editor Collaboration plugin shows in Unreal, and where to configure
  it.
---

# Plugin

The plugin runs inside the Unreal Editor. It connects to your server, draws everyone else's presence, and lets you chat and comment without leaving the editor.

![Cameras in the viewport, who is online, chat, and a comment pinned on the map.](https://3124660912-files.gitbook.io/~/files/v0/b/gitbook-x-prod.appspot.com/o/spaces%2FCzO6BrBWHeqbWYPwcAp9%2Fuploads%2FiYOaxeiLtsYQ4NawnSTb%2Feditor-overview.jpg?alt=media)

<table data-view="cards"><thead><tr><th></th><th></th><th></th><th data-hidden data-card-target data-type="content-ref"></th></tr></thead><tbody><tr><td><h3><i class="fa-sliders" style="color:$primary;">:sliders:</i></h3></td><td><strong>Configure</strong></td><td>Server URL for the team, and name, color, and password for each person.</td><td><a href="configure.md">configure.md</a></td></tr><tr><td><h3><i class="fa-location-dot" style="color:$primary;">:location-dot:</i></h3></td><td><strong>Work together</strong></td><td>Badges, Go to, cameras, graphs, Sequencer, and look-here markers.</td><td><a href="work-together.md">work-together.md</a></td></tr><tr><td><h3><i class="fa-comments" style="color:$primary;">:comments:</i></h3></td><td><strong>Chat</strong></td><td>Send messages, mention someone, and load older history.</td><td><a href="chat.md">chat.md</a></td></tr><tr><td><h3><i class="fa-note-sticky" style="color:$primary;">:note-sticky:</i></h3></td><td><strong>Comments</strong></td><td>Pin a thread on an actor, a graph node, or an asset.</td><td><a href="comments.md">comments.md</a></td></tr><tr><td><h3><i class="fa-file-pen" style="color:$primary;">:file-pen:</i></h3></td><td><strong>Files and locks</strong></td><td>Pencils for edits, soft locks, and Git LFS locks.</td><td><a href="files-and-locks.md">files-and-locks.md</a></td></tr></tbody></table>

## Where things live

| What                | Where                                                                       |
| ------------------- | --------------------------------------------------------------------------- |
| Who is online       | Badges at the top right of every window, next to the project name           |
| Collaborators panel | A badge, the **Collab** toolbar button, or `Window > Tools > Collaborators` |
| Chat                | Speech-bubble button, or `Window > Tools > Chat`                            |
| Comments            | Pin button, **Comment** toolbar button, or `Ctrl+Alt+C`                     |
| Look-here marker    | `Ctrl+Alt+P`, then click in the viewport or a graph                         |

{% hint style="warning" %}
A map or asset the other person has not saved, or has not pushed to Git yet, cannot be opened on your machine. The editor says so.
{% endhint %}
