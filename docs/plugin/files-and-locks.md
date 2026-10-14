---
description: Pencils for files someone else changed, soft locks, and Git LFS locks.
---

# Files and locks

Git still moves the bytes. The plugin only tells you who is touching a file, and can hold a lock so two people do not save over each other.

## Pencil: someone else changed it

A pencil in the author's color appears on the asset in the **Content Browser** and at the top right of its editor. For the level editor, that is the open map. Hover shows who, when, and whether it is saved.

Each editor shares the state of its files:

* unsaved changes, or
* saved, with the file's MD5 fingerprint

The pencil disappears once your file has the same content as theirs, for example after a `git pull`. If you edited the file in the meantime, the pencil stays.

| Tracked                                               | Not tracked                |
| ----------------------------------------------------- | -------------------------- |
| Project content under `/Game/`                        | Engine plugins             |
| Content of plugins in the project's `Plugins/` folder | Plugins installed from Fab |

World Partition maps attach each modified actor file (`__ExternalActors__`) to its map.

If Git replaces your file (pull or revert) before you saved it, your own mark is removed. A mark not updated for 30 days is dropped.

Your marks stay yours when you reconnect or restart the editor: the plugin keeps an id for this copy of the project in `Saved/EditorCollaboration/ClientId.txt`. Changing your **Display Name** moves your marks to the new name.

{% hint style="warning" %}
Do not copy `Saved/EditorCollaboration/ClientId.txt` to another machine. Two copies of the project with the same file are seen as one person. Delete the file on the copy and the plugin creates a new one.
{% endhint %}

When someone closes their last editor on the project, or loses the connection, the server clears their "I'm working on this" marks and tells everyone. **Warn When Opening** and **Warn Before Save** use the same information. **Notify When a File Is Saved** fires when someone saves a file you have open.

**Show Their Files** lists those assets under the person in the collaborators panel. **Show Asset Filters** adds Content Browser filters for files modified by others and for locks.

## Locks

**Show Locks** draws a padlock in the Content Browser and a lock button on an open asset.

| Lock         | Stored where                                   | When it drops                                                                           |
| ------------ | ---------------------------------------------- | --------------------------------------------------------------------------------------- |
| Soft lock    | On the collaboration server, as a working mark | When you disconnect, and also when you go away if **Release Soft Lock When Away** is on |
| Git LFS lock | In Git LFS, not on the collaboration server    | When that person unlocks the file                                                       |

The lock button uses Git LFS when `git` is available and the asset has a file on disk. Otherwise the editor says `git` was not found and the soft lock is used. **Show Git LFS Owners** shows the name of the person who holds the LFS lock.

{% hint style="warning" %}
An LFS lock only helps if the repository actually uses Git LFS for that file. The collaboration server does not record LFS locks.
{% endhint %}
