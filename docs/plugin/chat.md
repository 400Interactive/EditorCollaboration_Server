---
description: Team chat in the editor, mentions, unread messages, and how history is loaded.
---

# Chat

Open it from the speech-bubble button at the top right, or from `Window > Tools > Chat`. A red badge is the unread count. That counter stays active even when **Notify Chat** is off. The notification toast is what **Notify Chat** controls.

* Enter sends the message.
* `@Name` mentions someone. The message is highlighted for them. **Notify Mentions** covers mentions inside comments. A mention in chat follows the chat notification.
* **Play a Sound** can play for a mention.

The server keeps the last 10,000 messages of each room. On join you receive the last 100 messages. **Load older messages**, at the top of the chat, asks for 100 more. The server answers with `complete: true` when nothing older remains.

A comment can be shared into the chat. With **Comment Links in Chat** on, that link becomes a button that opens the thread.

{% hint style="info" %}
Chat is per room. People in another project on the same server do not see it.
{% endhint %}
