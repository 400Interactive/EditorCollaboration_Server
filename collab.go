package main

import (
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
		AuthorID: c.id,
		Author:   c.name,
		Color:    stateString(c.state, "color"),
		Text:     text,
		At:       time.Now().UnixMilli(),
	}
}

// handleCollab processes chat and comment messages. Everything is broadcast to the whole room, sender included,
// so that every client shows the server-assigned ids and timestamps.
func (s *server) handleCollab(c *client, msg inbound) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		s.broadcastLocked(c.room, "", mustJSON(map[string]any{"type": "chat", "message": message}))

	case "comment.create":
		text, ok := cleanText(msg.Text)
		if !ok || !validAnchor(msg.Anchor) {
			c.enqueue(errorMessage("comment", "invalid", "Invalid comment."))
			return
		}
		thread := &comment{
			ID:        newID(),
			Anchor:    msg.Anchor,
			CreatedAt: time.Now().UnixMilli(),
			Messages:  []chatMessage{c.newMessage(text)},
		}
		data.addComment(thread)
		s.broadcastComment(c.room, thread)

	case "comment.reply":
		text, ok := cleanText(msg.Text)
		thread, _ := data.findComment(msg.ID)
		if !ok || thread == nil || len(thread.Messages) >= maxMessagesPerComment {
			return
		}
		thread.Messages = append(thread.Messages, c.newMessage(text))
		data.saveComment(thread, false)
		s.broadcastComment(c.room, thread)

	case "comment.resolve":
		thread, _ := data.findComment(msg.ID)
		if thread == nil || msg.Resolved == nil {
			return
		}
		thread.Resolved = *msg.Resolved
		data.saveComment(thread, false)
		s.broadcastComment(c.room, thread)

	case "comment.move":
		thread, _ := data.findComment(msg.ID)
		if thread == nil || !validAnchor(msg.Anchor) {
			return
		}
		thread.Anchor = append(json.RawMessage(nil), msg.Anchor...)
		data.saveComment(thread, false)
		s.broadcastComment(c.room, thread)

	case "ping":
		if !validAnchor(msg.Anchor) {
			return
		}
		seconds := msg.Seconds
		if seconds < 10 || seconds > 20 {
			seconds = 15
		}
		s.broadcastLocked(c.room, "", mustJSON(map[string]any{
			"type": "ping", "id": newID(), "anchor": json.RawMessage(msg.Anchor),
			"authorId": c.id, "author": c.name, "color": stateString(c.state, "color"),
			"at": time.Now().UnixMilli(), "seconds": seconds,
		}))

	case "asset.modify":
		record, ok := c.newAssetRecord(msg)
		if !ok {
			return
		}
		data.upsertAsset(record)
		s.broadcastLocked(c.room, "", mustJSON(map[string]any{"type": "assetModified", "record": record}))

	case "asset.clear":
		if !validPackageName(msg.Package) || !data.removeAsset(msg.Package, c.name) {
			return
		}
		s.broadcastLocked(c.room, "", mustJSON(map[string]any{"type": "assetCleared", "package": msg.Package, "author": c.name}))

	case "comment.delete":
		thread, index := data.findComment(msg.ID)
		if thread == nil {
			return
		}
		if len(thread.Messages) > 0 && thread.Messages[0].Author != c.name {
			c.enqueue(errorMessage("comment", "notAuthor", "Only the author can delete this comment."))
			return
		}
		data.deleteComment(index)
		s.broadcastLocked(c.room, "", mustJSON(map[string]any{"type": "commentDeleted", "id": thread.ID}))
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
		AuthorID: c.id,
		Author:   c.name,
		Color:    stateString(c.state, "color"),
		At:       time.Now().UnixMilli(),
	}, true
}

func (s *server) broadcastComment(rm *room, thread *comment) {
	s.broadcastLocked(rm, "", mustJSON(map[string]any{"type": "comment", "comment": thread}))
}

// errorMessage carries codes that clients translate; message is an English fallback.
func errorMessage(code, reason, message string) []byte {
	return mustJSON(map[string]any{"type": "error", "code": code, "reason": reason, "message": message})
}
