---
description: Pin comment threads on actors, graph nodes, and assets, then resolve or reply.
---

# Comments

You need to be connected. If you are not, the editor says "Connect to the server to add comments."

![A comment pinned on a Blueprint node, listed with the other threads in the room.](https://3124660912-files.gitbook.io/~/files/v0/b/gitbook-x-prod.appspot.com/o/spaces%2FCzO6BrBWHeqbWYPwcAp9%2Fuploads%2FF6BUJJhZcv4JKs5s1jBn%2Fcomments-graph.jpg?alt=media)

## Pin one

Use the pin button at the top right, the **Comment** toolbar button in the level editor or an asset editor, or `Ctrl+Alt+C`. Then click:

* in the viewport, to pin the comment to the object you clicked (it follows that object when it moves)
* in a graph, to pin it to the node you clicked
* an asset in the Content Browser, with a right-click, when **Comments on Assets** is on

`Esc` or a right-click cancels placement. In the dialog, `Ctrl+Enter` publishes.

Open comments are drawn in the author's color. Click one to open its thread. Hide the pins with **Show Comments** without deleting them.

## The list

`Window > Tools > Comments`, or the comment-list button at the top right. The number on that button is the count of open comments.

Each thread can be replied to (`Ctrl+Enter`), **Resolve**d, **Reopen**ed, or **Go**ne to. **Go** opens the map or the graph and frames the comment. On a World Partition map, **Load the Actor Cell** loads the cell that contains the actor.

**Share in chat** posts a link to the thread.

Filters in the list, also saved in personal settings:

| Filter            | Effect                                                     |
| ----------------- | ---------------------------------------------------------- |
| **Show resolved** | Include threads that are already resolved. Off by default. |
| **This map**      | Only threads on the open map.                              |
| **This graph**    | Only threads on the focused graph.                         |
| **Mine**          | Only threads you started.                                  |

You can edit your own messages. Deleting a thread asks for confirmation. Only the author can edit a message or delete a thread, also after restarting the editor. The server refuses anyone else with "Only the author can edit this message." or "Only the author can delete this comment."

A notification appears when someone comments, replies, or mentions you.

{% hint style="info" %}
A room holds up to 2,000 threads. One thread holds 500 messages. Messages written before plugin 1.3 can no longer be edited.
{% endhint %}
