package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func newTestServer(t *testing.T, password string, dataDir string) (*httptest.Server, *server) {
	t.Helper()
	db, err := openDatabase(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.close)
	srv := &server{
		cfg:          config{password: password, maxRoomClients: 8, dataDir: dataDir},
		passwordHash: sha256.Sum256([]byte(password)),
		rooms:        map[string]*room{},
		db:           db,
	}
	ts := httptest.NewServer(httpHandler(srv))
	t.Cleanup(ts.Close)
	return ts, srv
}

func dial(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func send(t *testing.T, conn *websocket.Conn, value any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, mustJSON(value)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func receive(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return msg
}

func expect(t *testing.T, conn *websocket.Conn, messageType string) map[string]any {
	t.Helper()
	msg := receive(t, conn)
	if msg["type"] != messageType {
		t.Fatalf("expected %q, got %v", messageType, msg)
	}
	return msg
}

func hello(room, password, name string) map[string]any {
	return map[string]any{
		"type": "hello", "protocolVersion": protocolVersion, "room": room, "password": password,
		"state": map[string]any{"name": name, "color": "#FF0000", "map": "/Game/Maps/Lvl_Hotel", "secret": "dropped"},
	}
}

type joined struct {
	conn         *websocket.Conn
	id           string
	peers        []any
	chat         []any
	chatComplete bool
	comments     []any
	assets       []any
}

func join(t *testing.T, ts *httptest.Server, room, password, name string) joined {
	t.Helper()
	conn := dial(t, ts)
	send(t, conn, hello(room, password, name))
	j := joined{conn: conn}
	j.id = expect(t, conn, "welcome")["selfId"].(string)
	j.peers = expect(t, conn, "roster")["peers"].([]any)
	history := expect(t, conn, "chatHistory")
	j.chat = history["messages"].([]any)
	j.chatComplete = history["complete"].(bool)
	j.comments = expect(t, conn, "comments")["comments"].([]any)
	j.assets = expect(t, conn, "assets")["records"].([]any)
	return j
}

func TestPresenceFlow(t *testing.T) {
	ts, _ := newTestServer(t, "pw", t.TempDir())

	alice := join(t, ts, "Hotel", "pw", "Alice")
	if len(alice.peers) != 0 {
		t.Fatalf("expected empty roster, got %v", alice.peers)
	}

	bob := join(t, ts, "Hotel", "pw", "Bob")
	if len(bob.peers) != 1 {
		t.Fatalf("bob should see alice, got %v", bob.peers)
	}
	aliceState := bob.peers[0].(map[string]any)["state"].(map[string]any)
	if aliceState["name"] != "Alice" || aliceState["secret"] != nil {
		t.Fatalf("unexpected alice state %v", aliceState)
	}

	if joinedMsg := expect(t, alice.conn, "presence"); joinedMsg["id"] != bob.id {
		t.Fatalf("alice should be told bob joined, got %v", joinedMsg)
	}

	send(t, bob.conn, map[string]any{"type": "presence", "state": map[string]any{"asset": map[string]any{"path": "/Game/BP_Door.BP_Door"}, "map": nil}})
	state := expect(t, alice.conn, "presence")["state"].(map[string]any)
	if state["asset"] == nil {
		t.Fatalf("unexpected update %v", state)
	}
	if value, present := state["map"]; !present || value != nil {
		t.Fatalf("map should be cleared with null, got %v", state)
	}

	bob.conn.Close(websocket.StatusNormalClosure, "")
	if leave := expect(t, alice.conn, "leave"); leave["id"] != bob.id {
		t.Fatalf("expected bob to leave, got %v", leave)
	}
}

func TestRoomsAreIsolated(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())

	join(t, ts, "ProjectA", "", "A")
	b := join(t, ts, "ProjectB", "", "B")
	if len(b.peers) != 0 {
		t.Fatalf("rooms leaked: %v", b.peers)
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	ts, _ := newTestServer(t, "secret", t.TempDir())

	conn := dial(t, ts)
	send(t, conn, hello("Hotel", "nope", "Mallory"))
	if msg := expect(t, conn, "error"); msg["code"] != "auth" {
		t.Fatalf("expected auth error, got %v", msg)
	}
}

func TestWrongProtocolIsRejected(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())

	conn := dial(t, ts)
	send(t, conn, map[string]any{"type": "hello", "protocolVersion": 999, "room": "Hotel"})
	if msg := expect(t, conn, "error"); msg["code"] != "version" {
		t.Fatalf("expected version error, got %v", msg)
	}
}

func TestChatHistoryForLateJoiners(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())

	alice := join(t, ts, "Hotel", "", "Alice")
	send(t, alice.conn, map[string]any{"type": "chat", "text": "  salut  "})
	echo := expect(t, alice.conn, "chat")["message"].(map[string]any)
	if echo["text"] != "salut" || echo["author"] != "Alice" || echo["authorId"] != alice.id || echo["color"] != "#FF0000" {
		t.Fatalf("unexpected chat echo %v", echo)
	}
	send(t, alice.conn, map[string]any{"type": "chat", "text": "   "})

	bob := join(t, ts, "Hotel", "", "Bob")
	if len(bob.chat) != 1 || bob.chat[0].(map[string]any)["text"] != "salut" {
		t.Fatalf("bob should get the chat history, got %v", bob.chat)
	}
}

func TestAssetModificationsAreSharedAndKeptPerAuthor(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())

	alice := join(t, ts, "Hotel", "", "Alice")
	send(t, alice.conn, map[string]any{"type": "asset.modify", "package": "/Game/BP_Door", "dirty": true})
	first := expect(t, alice.conn, "assetModified")["record"].(map[string]any)
	if first["author"] != "Alice" || first["dirty"] != true || first["asset"] != "/Game/BP_Door" {
		t.Fatalf("unexpected record %v", first)
	}

	send(t, alice.conn, map[string]any{"type": "asset.modify", "package": "/Game/BP_Door", "hash": "ABCDEF0123", "dirty": false})
	expect(t, alice.conn, "assetModified")
	send(t, alice.conn, map[string]any{"type": "asset.modify", "package": "not-a-package"})
	send(t, alice.conn, map[string]any{"type": "asset.modify", "package": "/Game/Maps/__ExternalActors__/X", "asset": "/Game/Maps/Lvl_Hotel", "hash": "zz"})

	bob := join(t, ts, "Hotel", "", "Bob")
	if len(bob.assets) != 1 {
		t.Fatalf("expected a single record per package and author, got %v", bob.assets)
	}
	record := bob.assets[0].(map[string]any)
	if record["hash"] != "abcdef0123" || record["dirty"] != false {
		t.Fatalf("the latest state should win: %v", record)
	}

	send(t, bob.conn, map[string]any{"type": "asset.clear", "package": "/Game/BP_Door"})
	send(t, alice.conn, map[string]any{"type": "asset.clear", "package": "/Game/BP_Door"})
	cleared := expect(t, bob.conn, "assetCleared")
	if cleared["author"] != "Alice" || cleared["package"] != "/Game/BP_Door" {
		t.Fatalf("only the author's own record may be cleared: %v", cleared)
	}
}

func TestCommentLifecycleAndPersistence(t *testing.T) {
	dataDir := t.TempDir()
	ts, srv := newTestServer(t, "", dataDir)

	alice := join(t, ts, "Hotel", "", "Alice")
	bob := join(t, ts, "Hotel", "", "Bob")
	expect(t, alice.conn, "presence")

	anchor := map[string]any{"kind": "graph", "asset": "/Game/BP_Door.BP_Door", "graph": "/Game/BP_Door.BP_Door:EventGraph", "x": 10, "y": 20}
	send(t, alice.conn, map[string]any{"type": "comment.create", "anchor": anchor, "text": "A revoir"})
	created := expect(t, bob.conn, "comment")["comment"].(map[string]any)
	expect(t, alice.conn, "comment")
	id := created["id"].(string)

	send(t, bob.conn, map[string]any{"type": "comment.reply", "id": id, "text": "Je regarde"})
	replied := expect(t, alice.conn, "comment")["comment"].(map[string]any)
	messages := replied["messages"].([]any)
	if len(messages) != 2 || messages[1].(map[string]any)["author"] != "Bob" {
		t.Fatalf("unexpected thread %v", replied)
	}
	expect(t, bob.conn, "comment")
	opening := messages[0].(map[string]any)
	reply := messages[1].(map[string]any)
	openingID, replyID := opening["id"].(string), reply["id"].(string)
	openingAt := opening["at"]

	send(t, bob.conn, map[string]any{"type": "comment.edit", "id": id, "messageId": openingID, "text": "non"})
	if refused := expect(t, bob.conn, "error"); refused["code"] != "comment" || refused["reason"] != "notMessageAuthor" {
		t.Fatalf("only the author may edit the opening message: %v", refused)
	}

	send(t, alice.conn, map[string]any{"type": "comment.edit", "id": id, "messageId": openingID, "text": "Revu"})
	edited := expect(t, bob.conn, "comment")["comment"].(map[string]any)
	expect(t, alice.conn, "comment")
	editedOpening := edited["messages"].([]any)[0].(map[string]any)
	if editedOpening["text"] != "Revu" || editedOpening["at"] != openingAt || editedOpening["editedAt"] == nil || len(edited["messages"].([]any)) != 2 {
		t.Fatalf("edit should replace the text and keep the time: %v", edited)
	}

	send(t, alice.conn, map[string]any{"type": "comment.edit", "id": id, "messageId": replyID, "text": "non plus"})
	if refused := expect(t, alice.conn, "error"); refused["code"] != "comment" || refused["reason"] != "notMessageAuthor" {
		t.Fatalf("only the author may edit a reply: %v", refused)
	}

	send(t, bob.conn, map[string]any{"type": "comment.edit", "id": id, "messageId": replyID, "text": "Je regarde bien"})
	bobEdit := expect(t, alice.conn, "comment")["comment"].(map[string]any)
	expect(t, bob.conn, "comment")
	if bobEdit["messages"].([]any)[1].(map[string]any)["text"] != "Je regarde bien" {
		t.Fatalf("bob should edit his reply: %v", bobEdit)
	}

	send(t, alice.conn, map[string]any{"type": "comment.edit", "id": id, "messageId": openingID, "text": "   "})
	if refused := expect(t, alice.conn, "error"); refused["code"] != "comment" {
		t.Fatalf("empty text must be refused: %v", refused)
	}

	send(t, bob.conn, map[string]any{"type": "comment.resolve", "id": id, "resolved": true})
	if resolved := expect(t, alice.conn, "comment")["comment"].(map[string]any); resolved["resolved"] != true {
		t.Fatalf("comment should be resolved: %v", resolved)
	}
	expect(t, bob.conn, "comment")

	send(t, bob.conn, map[string]any{"type": "comment.delete", "id": id})
	if refused := expect(t, bob.conn, "error"); refused["code"] != "comment" {
		t.Fatalf("only the author may delete: %v", refused)
	}

	send(t, alice.conn, map[string]any{"type": "comment.create", "anchor": map[string]any{"kind": "nope"}, "text": "x"})
	expect(t, alice.conn, "error")

	srv.db.flush()

	ts2, _ := newTestServer(t, "", dataDir)
	carol := join(t, ts2, "Hotel", "", "Carol")
	if len(carol.comments) != 1 {
		t.Fatalf("comments should survive a restart, got %v", carol.comments)
	}
	storedMessages := carol.comments[0].(map[string]any)["messages"].([]any)
	if storedMessages[0].(map[string]any)["text"] != "Revu" || storedMessages[1].(map[string]any)["text"] != "Je regarde bien" {
		t.Fatalf("edited text should survive a restart, got %v", carol.comments)
	}

	send(t, alice.conn, map[string]any{"type": "comment.delete", "id": id})
	if deleted := expect(t, alice.conn, "commentDeleted"); deleted["id"] != id {
		t.Fatalf("expected deletion, got %v", deleted)
	}
}

func TestCommentMoveUpdatesTheAnchor(t *testing.T) {
	dataDir := t.TempDir()
	ts, srv := newTestServer(t, "", dataDir)

	alice := join(t, ts, "Hotel", "", "Alice")
	bob := join(t, ts, "Hotel", "", "Bob")
	expect(t, alice.conn, "presence")

	send(t, alice.conn, map[string]any{"type": "comment.create", "anchor": map[string]any{"kind": "graph", "x": 10, "y": 20}, "text": "Ici"})
	created := expect(t, bob.conn, "comment")["comment"].(map[string]any)
	expect(t, alice.conn, "comment")
	id := created["id"].(string)
	if messages := created["messages"].([]any); len(messages) != 1 {
		t.Fatalf("a move must not add a message, thread started as %v", created)
	}

	send(t, bob.conn, map[string]any{"type": "comment.move", "id": id, "anchor": map[string]any{"kind": "graph", "x": 400, "y": 80, "node": "K2Node_1"}})
	moved := expect(t, alice.conn, "comment")["comment"].(map[string]any)
	expect(t, bob.conn, "comment")
	anchor := moved["anchor"].(map[string]any)
	if anchor["x"].(float64) != 400 || anchor["y"].(float64) != 80 || anchor["node"] != "K2Node_1" {
		t.Fatalf("anchor should follow the move: %v", anchor)
	}
	if messages := moved["messages"].([]any); len(messages) != 1 {
		t.Fatalf("moving must not change the thread: %v", moved)
	}

	send(t, bob.conn, map[string]any{"type": "comment.move", "id": id, "anchor": map[string]any{"kind": "nope"}})
	send(t, bob.conn, map[string]any{"type": "comment.move", "id": "missing", "anchor": map[string]any{"kind": "graph", "x": 1, "y": 1}})
	send(t, alice.conn, map[string]any{"type": "chat", "text": "toujours la"})
	if chat := expect(t, bob.conn, "chat"); chat["message"].(map[string]any)["text"] != "toujours la" {
		t.Fatalf("an invalid move must not be broadcast")
	}

	srv.db.flush()
	ts2, _ := newTestServer(t, "", dataDir)
	carol := join(t, ts2, "Hotel", "", "Carol")
	if len(carol.comments) != 1 {
		t.Fatalf("the moved comment should be stored, got %v", carol.comments)
	}
	stored := carol.comments[0].(map[string]any)["anchor"].(map[string]any)
	if stored["x"].(float64) != 400 || stored["y"].(float64) != 80 {
		t.Fatalf("the stored anchor should be the moved one: %v", stored)
	}
}

func TestPingIsBroadcastAndNotStored(t *testing.T) {
	dataDir := t.TempDir()
	ts, srv := newTestServer(t, "", dataDir)

	alice := join(t, ts, "Hotel", "", "Alice")
	bob := join(t, ts, "Hotel", "", "Bob")
	expect(t, alice.conn, "presence")

	send(t, alice.conn, map[string]any{"type": "ping", "seconds": 99, "anchor": map[string]any{"kind": "map", "map": "/Game/Maps/Lvl_Hotel"}})
	ping := expect(t, bob.conn, "ping")
	expect(t, alice.conn, "ping")
	if ping["author"] != "Alice" || ping["seconds"].(float64) != 15 || ping["id"] == "" {
		t.Fatalf("ping should be stamped and clamped to 10-20s: %v", ping)
	}

	srv.db.flush()
	ts2, _ := newTestServer(t, "", dataDir)
	carol := join(t, ts2, "Hotel", "", "Carol")
	if len(carol.comments) != 0 || len(carol.chat) != 0 {
		t.Fatalf("a ping must not be written to the database")
	}
}

func TestSoftLockClearsWhenTheAuthorLeaves(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())
	alice := join(t, ts, "Hotel", "", "Alice")
	bob := join(t, ts, "Hotel", "", "Bob")
	expect(t, alice.conn, "presence")

	send(t, alice.conn, map[string]any{"type": "asset.modify", "package": "/Game/BP_Door", "dirty": false, "working": true})
	expect(t, alice.conn, "assetModified")
	expect(t, bob.conn, "assetModified")

	alice.conn.Close(websocket.StatusNormalClosure, "")
	record := expect(t, bob.conn, "assetModified")["record"].(map[string]any)
	if record["working"] != false {
		t.Fatalf("the soft lock should drop when its author leaves: %v", record)
	}
	if left := expect(t, bob.conn, "leave"); left["id"] != alice.id {
		t.Fatalf("expected alice to leave, got %v", left)
	}
}

func TestAssetCommentIsBroadcast(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())
	alice := join(t, ts, "Hotel", "", "Alice")
	bob := join(t, ts, "Hotel", "", "Bob")
	expect(t, alice.conn, "presence")

	send(t, alice.conn, map[string]any{"type": "comment.create", "anchor": map[string]any{"kind": "asset", "package": "/Game/BP_Door"}, "text": "On the asset"})
	comment := expect(t, bob.conn, "comment")["comment"].(map[string]any)
	anchor := comment["anchor"].(map[string]any)
	if anchor["kind"] != "asset" || anchor["package"] != "/Game/BP_Door" {
		t.Fatalf("asset comment was not kept: %v", comment)
	}
}

func TestListenHostIsLoopback(t *testing.T) {
	if !listenHostIsLoopback("127.0.0.1:7878") || !listenHostIsLoopback("[::1]:7878") || !listenHostIsLoopback("localhost:7878") {
		t.Fatal("loopback addresses should be allowed without a password")
	}
	if listenHostIsLoopback(":7878") || listenHostIsLoopback("0.0.0.0:7878") || listenHostIsLoopback("192.168.1.10:7878") {
		t.Fatal("non-loopback addresses must require a password")
	}
}

func TestSpoofedNameCannotEditOrClear(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())
	alice := join(t, ts, "Hotel", "", "Alice")
	impostor := join(t, ts, "Hotel", "", "Alice")
	expect(t, alice.conn, "presence")

	send(t, alice.conn, map[string]any{"type": "comment.create", "anchor": map[string]any{"kind": "asset", "package": "/Game/BP_Door"}, "text": "Mine"})
	created := expect(t, impostor.conn, "comment")["comment"].(map[string]any)
	expect(t, alice.conn, "comment")
	openingID := created["messages"].([]any)[0].(map[string]any)["id"].(string)

	send(t, impostor.conn, map[string]any{"type": "comment.edit", "id": created["id"], "messageId": openingID, "text": "Stolen"})
	if refused := expect(t, impostor.conn, "error"); refused["reason"] != "notMessageAuthor" {
		t.Fatalf("a second client with the same display name must not edit: %v", refused)
	}

	send(t, alice.conn, map[string]any{"type": "asset.modify", "package": "/Game/BP_Door", "dirty": true})
	expect(t, alice.conn, "assetModified")
	expect(t, impostor.conn, "assetModified")
	send(t, impostor.conn, map[string]any{"type": "asset.clear", "package": "/Game/BP_Door"})
	send(t, impostor.conn, map[string]any{"type": "chat", "text": "still there"})
	if chat := expect(t, alice.conn, "chat"); chat["message"].(map[string]any)["text"] != "still there" {
		t.Fatalf("clearing someone else's asset must not be broadcast, got %v", chat)
	}
}

func TestSoftLockIsKeptPerAuthor(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())
	alice := join(t, ts, "Hotel", "", "Alice")
	send(t, alice.conn, map[string]any{"type": "asset.modify", "package": "/Game/BP_Door", "dirty": false, "working": true})
	record := expect(t, alice.conn, "assetModified")["record"].(map[string]any)
	if record["working"] != true || record["dirty"] != false {
		t.Fatalf("working flag should round-trip: %v", record)
	}
	bob := join(t, ts, "Hotel", "", "Bob")
	if len(bob.assets) != 1 || bob.assets[0].(map[string]any)["working"] != true {
		t.Fatalf("a late joiner should see the soft lock: %v", bob.assets)
	}
}
