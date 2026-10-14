---
description: >-
  JSON messages the Unreal plugin and the collaboration server exchange over
  WebSocket.
---

# Protocol

Clients speak JSON over a WebSocket. The current protocol version is **6** (plugin 1.3, server 1.3), sent in `hello` and reported by `GET /health` as `protocolVersion`. A mismatch is refused with the error code `version`.

{% hint style="info" %}
This page is for anyone building or debugging a client. Day-to-day setup does not require it.
{% endhint %}

## Client to server

| Message           | Role                                                                                          |
| ----------------- | --------------------------------------------------------------------------------------------- |
| `hello`           | Join a room. Fields: `protocolVersion`, `room`, `password`, `clientId`, `session`, `state`.   |
| `presence`        | Changed fields of `state`. A field set to `null` clears it.                                   |
| `chat`            | Send `text`.                                                                                  |
| `chat.history`    | Ask for older messages. `before` is the id of the oldest message already shown.               |
| `comment.create`  | New thread. Fields: `anchor`, `text`.                                                         |
| `comment.reply`   | Reply. Fields: `id`, `text`.                                                                  |
| `comment.resolve` | Resolve or reopen. Fields: `id`, `resolved`.                                                  |
| `comment.move`    | Move the pin. Fields: `id`, `anchor`.                                                         |
| `comment.edit`    | Replace the text of one message. Fields: `id`, `messageId`, `text`. Only its author succeeds. |
| `comment.delete`  | Delete a thread. Only the author succeeds.                                                    |
| `asset.modify`    | File state. Fields: `package`, `asset`, `hash`, `dirty`, `working`.                           |
| `asset.clear`     | Drop your record for a `package`.                                                             |
| `ping`            | Look-here marker. Fields: `anchor`, `seconds`. Broadcast only. Not stored.                    |

```json
{"type":"hello","protocolVersion":6,"room":"MyProject","password":"...","clientId":"...","session":"...","state":{}}
```

Clients send at most 10 messages per second, and only when something changed. Past its limit, the server stops reading that client for a moment instead of dropping messages.

## Identity

| Id         | Set by                                                                                | Lasts                                    | Seen by the others                       |
| ---------- | ------------------------------------------------------------------------------------- | ---------------------------------------- | ---------------------------------------- |
| `selfId`   | The server                                                                            | One connection                           | Yes: `presence`, `leave`, team play      |
| `authorId` | The server, derived from `clientId`                                                   | Across reconnections and editor restarts | Yes: chat, comments, pings, file records |
| `clientId` | The plugin, once per copy of the project, in `Saved/EditorCollaboration/ClientId.txt` | Until the file is deleted                | No, it is a secret                       |
| `session`  | The plugin, at each editor launch                                                     | One launch                               | No, it is a secret                       |

`clientId` and `session` are 16 to 64 letters, digits, or `-`. A `hello` without them is refused with `protocol`. An `authorId` cannot be sent back as a `clientId`, so seeing someone's messages does not let you act as them.

Two editors opened on the same copy of the project share one `authorId`, each with its own `session`. A `hello` with the `clientId` and `session` of a connection still in the room (its socket is not closed yet, as after a network drop) replaces it: the others get `leave`, then the new `presence`.

## Server to client

On join, the server sends `welcome` (`selfId`, `authorId`, `protocolVersion`), `roster` (`peers`, each with `id`, `authorId`, and `state`), `chatHistory` (`messages`, `complete`), `comments`, and `assets` (`records`).

| Message          | Role                                                                        |
| ---------------- | --------------------------------------------------------------------------- |
| `presence`       | `id` and a partial `state` for one peer. The first one also has `authorId`. |
| `leave`          | `id` of someone who disconnected.                                           |
| `chat`           | One new `message`.                                                          |
| `chatOlder`      | `before`, `messages`, `complete`.                                           |
| `comment`        | A created or updated thread.                                                |
| `commentDeleted` | `id` of a removed thread.                                                   |
| `assetModified`  | One file record.                                                            |
| `assetCleared`   | `package` and `author`.                                                     |
| `ping`           | `id`, `anchor`, `authorId`, `author`, `color`, `at`, `seconds`.             |
| `error`          | `code`, `reason`, and a fallback English `message`.                         |

`complete` is `true` when no older chat remains. Joining returns the last 100 messages. A room keeps its last 10,000 chat messages and accepts up to 2,000 comment threads. A thread holds 500 messages.

## State

`state` fields: `name`, `color`, `map`, `cam`, `asset`, `graph`, `nodes`, `actors`, `active`, `pie`, `play`, `sequencer`, `branch`, `actorsMore`, `nodesMore`.

`branch` is the Git branch, or `null` when HEAD is detached. `actorsMore` and `nodesMore` count selected actors or nodes that were not sent, past the limit of 64.

`play` is `null`, or the team game this editor hosts:

```json
{"address":"203.0.113.8","port":17777,"map":"/Game/Maps/Lobby","public":true,"since":1710000000000}
```

`public` is `false` when the address is only reachable on the local network. `since` is when hosting started; when two editors host the same map, the earlier one wins.

## Anchors

A comment or ping `anchor.kind` is `map`, `graph`, or `asset`. An asset anchor looks like:

```json
{"kind":"asset","package":"/Game/Maps/Lobby"}
```

## Errors

```json
{"type":"error","code":"auth","reason":"...","message":"..."}
```

| Code       | Meaning                                                                                          |
| ---------- | ------------------------------------------------------------------------------------------------ |
| `auth`     | Wrong password.                                                                                  |
| `version`  | Plugin and server protocol versions differ.                                                      |
| `room`     | Invalid room name.                                                                               |
| `full`     | The room is at `-max-room-clients` (64 by default).                                              |
| `protocol` | Malformed message, or a `hello` without `clientId` or `session`.                                 |
| `comment`  | Invalid comment, an edit or delete by someone other than the author, or a room at 2,000 threads. |
| `storage`  | The server cannot read its database.                                                             |

The English `message` is a fallback. The Unreal plugin translates `code` into the editor language.

## What is stored

File records are kept per package and author. A record left under a name by an author who is not connected (a record from protocol 5, or from another copy of the project) is taken over by the next `asset.modify` or `asset.clear` under that name. Changing `name` moves the sender's records to the new name: `assetCleared` for the old one, then `assetModified`. A record not updated for 30 days is dropped.

When the last editor with an `authorId` disconnects, the server clears its "I'm working on this" marks and broadcasts each one as `assetModified` with `working: false`. Git LFS locks are not stored on the server. Chat text may contain `[[comment:<id>]]`. The server stores that as text. The plugin turns it into a button when **Comment Links in Chat** is on.

## Upgrading from protocol 5

Update the plugin and the server together: each refuses the other's previous version with `version`. The database is kept as is. File records from protocol 5 are taken over by name, as above. Comment messages written under protocol 5 keep the connection id they were written with, so nobody can edit them anymore.
