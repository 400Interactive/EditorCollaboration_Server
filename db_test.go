package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func seedChat(t *testing.T, db *database, room string, count int) {
	t.Helper()
	db.enqueue(func(tx *sql.Tx) error {
		for i := 0; i < count; i++ {
			if err := insertChat(tx, room, chatMessage{ID: fmt.Sprintf("m%04d", i), Author: "Seed", Text: fmt.Sprint(i), At: int64(i)}); err != nil {
				return err
			}
		}
		return nil
	})
	db.flush()
}

// BenchmarkChatWrites measures how many chat messages per second the single writer commits, spread over 200 rooms.
func BenchmarkChatWrites(b *testing.B) {
	db, err := openDatabase(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer db.close()
	rooms := make([]*roomData, 200)
	for i := range rooms {
		rooms[i] = &roomData{db: db, name: fmt.Sprintf("room-%d", i), ChatComplete: true}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rooms[i%len(rooms)].addChat(chatMessage{ID: newID(), Author: "Bench", Color: "#FFFFFF", Text: "Un message de chat de taille habituelle.", At: int64(i)})
	}
	db.flush()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "msg/s")
}

func messageText(message any) string {
	return message.(map[string]any)["text"].(string)
}

func TestChatHistoryIsUnlimitedAndPaged(t *testing.T) {
	ts, srv := newTestServer(t, "", t.TempDir())
	seedChat(t, srv.db, "Hotel", 2*chatPageSize+50)

	alice := join(t, ts, "Hotel", "", "Alice")
	if len(alice.chat) != chatPageSize || alice.chatComplete || messageText(alice.chat[0]) != fmt.Sprint(chatPageSize+50) {
		t.Fatalf("join should carry the latest page: %d messages, complete=%v", len(alice.chat), alice.chatComplete)
	}

	oldest := alice.chat[0].(map[string]any)["id"].(string)
	var collected []any
	for page := 0; page < 3; page++ {
		send(t, alice.conn, map[string]any{"type": "chat.history", "before": oldest})
		older := expect(t, alice.conn, "chatOlder")
		messages := older["messages"].([]any)
		collected = append(messages, collected...)
		if older["complete"].(bool) {
			break
		}
		oldest = messages[0].(map[string]any)["id"].(string)
	}
	if len(collected) != chatPageSize+50 || messageText(collected[0]) != "0" || messageText(collected[len(collected)-1]) != fmt.Sprint(chatPageSize+49) {
		t.Fatalf("older pages should reach the first message in order, got %d messages", len(collected))
	}
}

func TestRoomIsReloadedFromTheDatabase(t *testing.T) {
	ts, srv := newTestServer(t, "", t.TempDir())

	alice := join(t, ts, "Hotel", "", "Alice")
	send(t, alice.conn, map[string]any{"type": "chat", "text": "avant"})
	expect(t, alice.conn, "chat")
	send(t, alice.conn, map[string]any{"type": "asset.modify", "package": "/Game/BP_Door", "dirty": true})
	expect(t, alice.conn, "assetModified")
	alice.conn.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		_, loaded := srv.rooms["Hotel"]
		srv.mu.Unlock()
		if !loaded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the empty room should be unloaded")
		}
		time.Sleep(10 * time.Millisecond)
	}

	bob := join(t, ts, "Hotel", "", "Bob")
	if len(bob.chat) != 1 || messageText(bob.chat[0]) != "avant" || !bob.chatComplete || len(bob.assets) != 1 {
		t.Fatalf("the room should come back from the database: chat %v, assets %v", bob.chat, bob.assets)
	}
}

func TestLegacyRoomFilesAreImported(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"room":"Hotel","chat":[{"id":"a","author":"Ann","text":"bonjour","at":1}],
		"comments":[{"id":"c1","anchor":{"kind":"map","map":"/Game/Maps/Lvl_Hotel"},"resolved":true,"createdAt":2,"messages":[{"id":"r1","author":"Ann","text":"ici"}]}],
		"assets":[{"package":"/Game/BP_Door","asset":"/Game/BP_Door","hash":"ab","author":"Ann","at":` + fmt.Sprint(time.Now().UnixMilli()) + `}]}`
	path := filepath.Join(dir, "room-0123456789abcdef.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := openDatabase(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.close()
	if _, err := os.Stat(path + ".imported"); err != nil {
		t.Fatalf("the legacy file should be renamed: %v", err)
	}
	data, err := db.loadRoom("Hotel")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Chat) != 1 || len(data.Comments) != 1 || !data.Comments[0].Resolved || len(data.Comments[0].Messages) != 1 || len(data.Assets) != 1 {
		t.Fatalf("unexpected import: %+v", data)
	}
}
