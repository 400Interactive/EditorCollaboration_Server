package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxTextLength   = 2000
	maxAnchorLength = 4096
)

var anchorKinds = map[string]bool{"map": true, "graph": true, "asset": true}

func cleanText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > maxTextLength || !utf8.ValidString(text) {
		return "", false
	}
	return text, true
}

func validAnchor(anchor json.RawMessage) bool {
	if len(anchor) == 0 || len(anchor) > maxAnchorLength {
		return false
	}
	var fields struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(anchor, &fields) == nil && anchorKinds[fields.Kind]
}

func stateString(state map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := state[key]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

// newMessage stamps a message with the sender identity known by the server. Requires s.mu.
func (c *client) newMessage(text string) chatMessage {
	return chatMessage{
		ID:       newID(),
		AuthorID: c.author,
		Author:   c.name,
		Color:    stateString(c.state, "color"),
		Text:     text,
		At:       time.Now().UnixMilli(),
	}
}

// delivery is a broadcast prepared and queued under server.mu, so every client receives the changes in the order
// they were applied. Queuing never blocks: a client whose queue is full is disconnected.
type delivery struct {
	recipients []*client
	message    []byte
}

func queueBroadcast(rm *room, senderID string, payload any, batch *[]delivery) {
	if rm == nil || batch == nil {
		return
	}
	recipients := make([]*client, 0, len(rm.clients))
	for id, other := range rm.clients {
		if senderID == "" || id != senderID {
			recipients = append(recipients, other)
		}
	}
	*batch = append(*batch, delivery{recipients: recipients, message: mustJSON(payload)})
}

func deliver(batch []delivery) {
	for _, item := range batch {
		for _, other := range item.recipients {
			other.enqueue(item.message)
		}
	}
}

// authoredBy is true for the client that created the message. authorId wins; a legacy row with an empty id
// still matches the display name it was stored with.
func authoredBy(message chatMessage, c *client) bool {
	if message.AuthorID != "" {
		return message.AuthorID == c.author
	}
	return message.Author == c.name
}

// handleCollab processes chat and comment messages. Everything is broadcast to the whole room, sender included,
// so that every client shows the server-assigned ids and timestamps.
func (s *server) handleCollab(c *client, msg inbound) {
	s.mu.Lock()
	var batch []delivery
	s.applyCollab(c, msg, &batch)
	var writes []func(*sql.Tx) error
	if c.room != nil && c.room.data != nil {
		writes = c.room.data.takePending()
	}
	deliver(batch)
	s.mu.Unlock()
	s.persist(writes)
}

func (s *server) applyCollab(c *client, msg inbound, batch *[]delivery) {
	if c.room == nil {
		return
	}
	data := c.room.data

	switch msg.Type {
	case "chat":
		text, ok := cleanText(msg.Text)
		if !ok {
			return
		}
		message := c.newMessage(text)
		data.addChat(message)
		queueBroadcast(c.room, "", map[string]any{"type": "chat", "message": message}, batch)

	case "comment.create":
		text, ok := cleanText(msg.Text)
		if !ok || !validAnchor(msg.Anchor) {
			c.enqueue(errorMessage("comment", "invalid", "Invalid comment."))
			return
		}
		if len(data.Comments) >= maxCommentsPerRoom {
			c.enqueue(errorMessage("comment", "full", "This room has too many comments."))
			return
		}
		thread := &comment{
			ID:        newID(),
			Anchor:    msg.Anchor,
			CreatedAt: time.Now().UnixMilli(),
			Messages:  []chatMessage{c.newMessage(text)},
		}
		data.addComment(thread)
		queueBroadcast(c.room, "", map[string]any{"type": "comment", "comment": thread}, batch)

	case "comment.reply":
		text, ok := cleanText(msg.Text)
		thread, _ := data.findComment(msg.ID)
		if !ok || thread == nil || len(thread.Messages) >= maxMessagesPerComment {
			return
		}
		thread.Messages = append(thread.Messages, c.newMessage(text))
		data.saveComment(thread, false)
		queueBroadcast(c.room, "", map[string]any{"type": "comment", "comment": thread}, batch)

	case "comment.resolve":
		thread, _ := data.findComment(msg.ID)
		if thread == nil || msg.Resolved == nil {
			return
		}
		thread.Resolved = *msg.Resolved
		data.saveComment(thread, false)
		queueBroadcast(c.room, "", map[string]any{"type": "comment", "comment": thread}, batch)

	case "comment.move":
		thread, _ := data.findComment(msg.ID)
		if thread == nil || !validAnchor(msg.Anchor) {
			return
		}
		thread.Anchor = append(json.RawMessage(nil), msg.Anchor...)
		data.saveComment(thread, false)
		queueBroadcast(c.room, "", map[string]any{"type": "comment", "comment": thread}, batch)

	case "ping":
		if !validAnchor(msg.Anchor) {
			return
		}
		seconds := msg.Seconds
		if seconds < 10 || seconds > 20 {
			seconds = 15
		}
		queueBroadcast(c.room, "", map[string]any{
			"type": "ping", "id": newID(), "anchor": json.RawMessage(msg.Anchor),
			"authorId": c.author, "author": c.name, "color": stateString(c.state, "color"),
			"at": time.Now().UnixMilli(), "seconds": seconds,
		}, batch)

	case "asset.modify":
		record, ok := c.newAssetRecord(msg)
		if !ok {
			return
		}
		previousName, ok := data.upsertAsset(record, c.room.online)
		if !ok {
			return
		}
		if previousName != "" {
			queueBroadcast(c.room, "", map[string]any{"type": "assetCleared", "package": record.Package, "author": previousName}, batch)
		}
		queueBroadcast(c.room, "", map[string]any{"type": "assetModified", "record": record}, batch)

	case "asset.clear":
		if !validPackageName(msg.Package) {
			return
		}
		record, ok := data.removeAsset(msg.Package, c.author, c.name, c.room.online)
		if !ok {
			return
		}
		queueBroadcast(c.room, "", map[string]any{"type": "assetCleared", "package": msg.Package, "author": record.Author}, batch)

	case "comment.edit":
		text, ok := cleanText(msg.Text)
		thread, _ := data.findComment(msg.ID)
		if !ok || thread == nil {
			if !ok {
				c.enqueue(errorMessage("comment", "invalid", "Invalid comment."))
			}
			return
		}
		for i := range thread.Messages {
			if thread.Messages[i].ID != msg.MessageID {
				continue
			}
			if !authoredBy(thread.Messages[i], c) {
				c.enqueue(errorMessage("comment", "notMessageAuthor", "Only the author can edit this message."))
				return
			}
			thread.Messages[i].Text = text
			thread.Messages[i].EditedAt = time.Now().UnixMilli()
			data.saveComment(thread, false)
			queueBroadcast(c.room, "", map[string]any{"type": "comment", "comment": thread}, batch)
			return
		}

	case "comment.delete":
		thread, index := data.findComment(msg.ID)
		if thread == nil {
			return
		}
		if len(thread.Messages) == 0 || !authoredBy(thread.Messages[0], c) {
			c.enqueue(errorMessage("comment", "notAuthor", "Only the author can delete this comment."))
			return
		}
		id := thread.ID
		data.deleteComment(index)
		queueBroadcast(c.room, "", map[string]any{"type": "commentDeleted", "id": id}, batch)
	}
}

func validPackageName(name string) bool {
	return strings.HasPrefix(name, "/") && len(name) <= 512 && utf8.ValidString(name) && !strings.ContainsAny(name, "\x00\n\r")
}

// newAssetRecord validates an asset.modify message. Requires s.mu.
func (c *client) newAssetRecord(msg inbound) (assetRecord, bool) {
	asset := msg.Asset
	if asset == "" {
		asset = msg.Package
	}
	if !validPackageName(msg.Package) || !validPackageName(asset) || len(msg.Hash) > 64 {
		return assetRecord{}, false
	}
	for _, r := range msg.Hash {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return assetRecord{}, false
		}
	}
	return assetRecord{
		Package:  msg.Package,
		Asset:    asset,
		Hash:     strings.ToLower(msg.Hash),
		Dirty:    msg.Dirty,
		Working:  msg.Working,
		AuthorID: c.author,
		Author:   c.name,
		Color:    stateString(c.state, "color"),
		At:       time.Now().UnixMilli(),
	}, true
}

// errorMessage carries codes that clients translate; message is an English fallback.
func errorMessage(code, reason, message string) []byte {
	return mustJSON(map[string]any{"type": "error", "code": code, "reason": reason, "message": message})
}
