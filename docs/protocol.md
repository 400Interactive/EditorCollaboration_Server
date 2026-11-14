---
description: >-
  JSON messages the Unreal plugin and the collaboration server exchange over
  WebSocket.
---

# Protocol

Clients speak JSON over a WebSocket. The current protocol version is **9**, sent in `hello` and reported by `GET /health` as `protocolVersion`. A mismatch is refused with the error code `version`.

{% hint style="info" %}
This page is for anyone building or debugging a client. Day-to-day setup does not require it.
{% endhint %}

## Client to server

| Message           | Role                                                                                          |
| ----------------- | --------------------------------------------------------------------------------------------- |
| `hello`           | Join a room. Fields: `protocolVersion`, `room`, `password`, `clientId`, `session`, `engineVersion`, `commit`, `state`. |
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
| `pkg.publish`     | A saved file, already uploaded. Fields: `package`, `hash`, `size`, `base` (Git commit), `revision`, `since`, optional `from`, `fromRevision` and `fromSince` for an atomic rename.       |
| `pkg.delete`      | A deleted asset. Fields: `package`, `base`, `revision`.                                                            |
| `ops`             | Live edits. Fields: `target`, `kind` (`level` or `graph`), `base`, `revision`, `operationId`, `ops`. See [Content sync](#content-sync). |
| `ops.preview`     | A drag in progress. Fields: `target`, `revision`, `ops`. Relayed, not stored.                             |
| `ops.sync`        | Catch up on a target. Fields: `target`, `revision`, `since`.                                              |
| `graph.resync`    | Ask the others for a full copy of a Blueprint. Fields: `target`, `graph`, `revision`.                     |
| `graph.snapshot`  | Answer to `graph.resync`. Fields: `target`, `graph`, `revision`, and `text` or `blob`.                    |

```json
{"type":"hello","protocolVersion":9,"room":"MyProject","password":"...","clientId":"...","session":"...","engineVersion":"5.8.0","commit":"...","state":{}}
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

On join, the server sends `welcome` (`selfId`, `authorId`, `protocolVersion`, `blobToken`), `roster` (`peers`, each with `id`, `authorId`, and `state`), `chatHistory` (`messages`, `complete`), `comments`, `assets` (`records`), and `packages` (`records`).

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
| `pkgPublished`   | One package `record`, with revision in `seq` and last compacted journal entry in `cutoff`.                |
| `pkgDeleted`     | `package`, `authorId`, `revision`, `cutoff`, optional `to` for a rename. |
| `opsAck`         | `target`, `operationId`, `seq`. Sent when a batch has already been committed. |
| `opsApplied`     | `target`, `seq`, `revision`, `kind`, `authorId`, `senderId`, `operationId`, `ops`. Sent to everyone.               |
| `opsPreview`     | `target`, `authorId`, `ops`. Not sent back to the author.                   |
| `opsBatch`       | `target`, `since`, `revision`, `entries` (`seq`, `kind`, `authorId`, `ops`, `at`), `complete`. |
| `graphResync`    | `target`, `graph`, `authorId`. Not sent back to the author.                 |
| `graphSnapshot`  | `target`, `graph`, `authorId`, and `text` or `blob`. Sent to everyone.      |
| `notice`         | `code`: `engine` or `commit` blocks content for a mixed room. `compatible` resumes content.           |
| `error`          | `code`, `reason`, and a fallback English `message`.                         |

`complete` is `true` when no older chat remains. Joining returns the last 100 messages. A room keeps its last 10,000 chat messages and accepts up to 2,000 comment threads. A thread holds 500 messages.

## State

`state` fields: `name`, `color`, `map`, `cam`, `asset`, `graph`, `nodes`, `actors`, `active`, `pie`, `play`, `sequencer`, `branch`, `liveBp`, `actorsMore`, `nodesMore`.

`liveBp` is `true` when that editor has the experimental live Blueprint sync on.

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
| `sync`     | A published file, op or snapshot names a blob that was not uploaded (reason `missingBlob`).      |

The English `message` is a fallback. The Unreal plugin translates `code` into the editor language.

## What is stored

File records are kept per package and author. A record left under a name by an author who is not connected (a record from protocol 5, or from another copy of the project) is taken over by the next `asset.modify` or `asset.clear` under that name. Changing `name` moves the sender's records to the new name: `assetCleared` for the old one, then `assetModified`. A record not updated for 30 days is dropped.

When the last editor with an `authorId` disconnects, the server clears its "I'm working on this" marks and broadcasts each one as `assetModified` with `working: false`. Git LFS locks are not stored on the server. Chat text may contain `[[comment:<id>]]`. The server stores that as text. The plugin turns it into a button when **Comment Links in Chat** is on.

## Content sync

Files go through HTTP on the same address: `PUT /blob/{md5}` and `GET /blob/{md5}`, with `Authorization: Bearer <blobToken>` from `welcome`. The body is the file, up to 256 MB. The server checks the MD5 and keeps blobs in `blobs` inside its data directory. A blob that nothing names anymore is removed after an hour.

`ops` are appended to a journal per `target` (a long package name such as `/Game/Maps/Lobby`) and numbered by `seq`. Each successful `pkg.publish` advances the package revision and compacts entries through its `since` cutoff. Sequences persist independently of compacted rows. Later entries remain in the journal. `ops.sync` returns entries after `since` in pages when the client has the current `revision`. Ask again with the last applied `seq` until `complete` is `true`. A stale revision receives the current package record or `contentBaseline` when no saved file is available. A stale mutation receives `contentConflict`. The server commits the file record or operation before acknowledging it.

Blob tokens are scoped to their room. A hash uploaded in one room cannot be read or referenced from another room unless the bytes were independently uploaded there. The server verifies upload bodies even when a matching blob already exists. Content is disabled while room members report different engine versions or Git commits. Presence, chat and comments remain available.

Each op is an object with an `op` name of 64 characters at most. A message holds at most 64 ops and 48 KB of ops, and 64 KB in total. A bigger op is uploaded as a blob first and sent as `{"op":"...","blob":"<md5>","packed":true}`. Every `blob` named in an op, a record or a snapshot must already be on the server, or the message is refused with the `sync` error (reason `missingBlob`). Malformed ops are dropped. The server does not read the ops further; the plugin README lists them.

## Upgrading from protocol 6

Update the plugin and the server together. The database is kept, and the new tables are created on the first start.

## Upgrading from protocol 5

Update the plugin and the server together: each refuses the other's previous version with `version`. The database is kept as is. File records from protocol 5 are taken over by name, as above. Comment messages written under protocol 5 keep the connection id they were written with, so nobody can edit them anymore.
