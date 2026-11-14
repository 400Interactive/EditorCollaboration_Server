package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func putBlob(t *testing.T, tsURL, token string, body []byte) string {
	t.Helper()
	sum := md5.Sum(body)
	hash := hex.EncodeToString(sum[:])
	req, err := http.NewRequest(http.MethodPut, tsURL+"/blob/"+hash, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(resp.Body)
		t.Fatalf("put blob: %d %s", resp.StatusCode, detail)
	}
	return hash
}

func TestBlobRoundTripAndAuth(t *testing.T) {
	ts, _ := newTestServer(t, "pw", t.TempDir())
	alice := join(t, ts, "Hotel", "pw", "Alice")
	conn := dial(t, ts)
	send(t, conn, hello("Hotel", "pw", "Cara"))
	welcome := expect(t, conn, "welcome")
	token, _ := welcome["blobToken"].(string)
	if token == "" {
		t.Fatal("welcome should carry a blob token")
	}
	for i := 0; i < 5; i++ {
		receive(t, conn)
	}

	body := []byte("blueprint-bytes")
	hash := putBlob(t, ts.URL, token, body)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/blob/"+hash, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("blob without a token should be refused, got %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/blob/"+hash, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("get blob: %d %q", resp.StatusCode, got)
	}

	bad := md5.Sum([]byte("other"))
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/blob/"+hex.EncodeToString(bad[:]), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched hash should be rejected, got %d", resp.StatusCode)
	}
	_ = alice
}

func TestPackagePublishCompactsOps(t *testing.T) {
	dir := t.TempDir()
	ts, srv := newTestServer(t, "", dir)
	alice := join(t, ts, "Hotel", "", "Alice")
	bob := join(t, ts, "Hotel", "", "Bob")
	expect(t, alice.conn, "presence")

	conn := dial(t, ts)
	send(t, conn, hello("Hotel", "", "Uploader"))
	token := expect(t, conn, "welcome")["blobToken"].(string)
	for i := 0; i < 5; i++ {
		receive(t, conn)
	}
	expect(t, alice.conn, "presence")
	expect(t, bob.conn, "presence")
	hash := putBlob(t, ts.URL, token, []byte("map-v1"))

	send(t, alice.conn, map[string]any{
		"type": "ops", "target": "/Game/Maps/Lvl_Hotel", "kind": "level", "base": "",
		"ops": []any{map[string]any{"op": "set", "actor": "Door"}},
	})
	applied := expect(t, bob.conn, "opsApplied")
	if applied["seq"].(float64) != 1 {
		t.Fatalf("first op should be seq 1, got %v", applied["seq"])
	}
	expect(t, alice.conn, "opsApplied")

	send(t, alice.conn, map[string]any{
		"type": "pkg.publish", "package": "/Game/Maps/Lvl_Hotel", "hash": hash, "size": len("map-v1"), "base": "", "since": 1,
	})
	published := expect(t, bob.conn, "pkgPublished")["record"].(map[string]any)
	if published["hash"] != hash || published["author"] != "Alice" {
		t.Fatalf("unexpected publish %v", published)
	}
	expect(t, alice.conn, "pkgPublished")
	srv.db.flush()

	send(t, bob.conn, map[string]any{"type": "ops.sync", "target": "/Game/Maps/Lvl_Hotel", "since": 1, "revision": 1})
	batch := expect(t, bob.conn, "opsBatch")
	entries, _ := batch["entries"].([]any)
	if len(entries) != 0 {
		t.Fatalf("publish should drop earlier ops, got %v", entries)
	}

	send(t, alice.conn, map[string]any{
		"type": "ops", "target": "/Game/Maps/Lvl_Hotel", "kind": "level", "revision": 1,
		"ops": []any{map[string]any{"op": "delete", "actor": "Chair"}},
	})
	next := expect(t, bob.conn, "opsApplied")
	if next["seq"].(float64) != 2 {
		t.Fatalf("sequence should keep growing after compaction, got %v", next["seq"])
	}

	send(t, alice.conn, map[string]any{
		"type": "ops.preview", "target": "/Game/Maps/Lvl_Hotel", "revision": 1,
		"ops": []any{map[string]any{"op": "set", "actor": "Door"}},
	})
	if preview := expect(t, bob.conn, "opsPreview"); preview["seq"] != nil {
		t.Fatalf("previews are not stored and have no sequence, got %v", preview)
	}
	srv.db.flush()
	var n int
	if err := srv.db.sql.QueryRow(`SELECT COUNT(*) FROM ops WHERE target = ?`, "/Game/Maps/Lvl_Hotel").Scan(&n); err != nil || n != 1 {
		t.Fatalf("only the op after publish should remain, count=%d err=%v", n, err)
	}

	cara := join(t, ts, "Hotel", "", "Cara")
	records := cara.packages
	if len(records) != 1 || records[0].(map[string]any)["hash"] != hash {
		t.Fatalf("late joiner should see the published package, got %v", records)
	}
}

func TestBlobGarbageCollectionKeepsReferencedFiles(t *testing.T) {
	dir := t.TempDir()
	ts, srv := newTestServer(t, "", dir)
	alice := join(t, ts, "Hotel", "", "Alice")
	conn := dial(t, ts)
	send(t, conn, hello("Hotel", "", "Uploader"))
	token := expect(t, conn, "welcome")["blobToken"].(string)
	for i := 0; i < 5; i++ {
		receive(t, conn)
	}
	expect(t, alice.conn, "presence")
	keep := putBlob(t, ts.URL, token, []byte("kept"))
	drop := putBlob(t, ts.URL, token, []byte("dropped"))
	send(t, alice.conn, map[string]any{
		"type": "pkg.publish", "package": "/Game/BP_Door", "hash": keep, "size": 4, "base": "",
	})
	expect(t, alice.conn, "pkgPublished")
	srv.db.flush()

	old := time.Now().Add(-2 * blobGrace)
	for _, hash := range []string{keep, drop} {
		path := filepath.Join(dir, blobDirName, hash[:2], hash)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	alice.conn.CloseNow()
	conn.CloseNow()
	time.Sleep(100 * time.Millisecond)
	srv.gcBlobs()

	if _, err := os.Stat(filepath.Join(dir, blobDirName, keep[:2], keep)); err != nil {
		t.Fatalf("referenced blob should stay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, blobDirName, drop[:2], drop)); !os.IsNotExist(err) {
		t.Fatalf("orphan blob should be removed, err=%v", err)
	}
}

func TestRoomCredentialsAndBlobIsolation(t *testing.T) {
	ts, _ := newTestServer(t, "instance-password", t.TempDir(), map[string][32]byte{
		"ProjectA": sha256.Sum256([]byte("password-a")),
		"ProjectB": sha256.Sum256([]byte("password-b")),
	})
	alice := join(t, ts, "ProjectA", "password-a", "Alice")
	bob := join(t, ts, "ProjectB", "password-b", "Bob")
	for _, greeting := range []map[string]any{
		hello("ProjectA", "password-b", "Bob"),
		hello("ProjectC", "instance-password", "Bob"),
	} {
		conn := dial(t, ts)
		send(t, conn, greeting)
		if got := expect(t, conn, "error")["code"]; got != "auth" {
			t.Fatalf("expected room access denied, got %v", got)
		}
	}
	hash := putBlob(t, ts.URL, alice.blobToken, []byte("private bytes"))
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		req, err := http.NewRequest(method, ts.URL+"/blob/"+hash, bytes.NewReader(nil))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bob.blobToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := http.StatusNotFound
		if method == http.MethodPut {
			want = http.StatusBadRequest
		}
		if resp.StatusCode != want {
			t.Fatalf("%s with a foreign hash: got %d, want %d", method, resp.StatusCode, want)
		}
	}
	send(t, bob.conn, map[string]any{"type": "pkg.publish", "package": "/Game/Private", "hash": hash, "size": 13})
	if got := expect(t, bob.conn, "error")["reason"]; got != "missingBlob" {
		t.Fatalf("cross-room publish: %v", got)
	}
}

func TestContentSequenceAndPublicationAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	const target = "/Game/Maps/Restart"
	op := map[string]any{"type": "ops", "target": target, "kind": "level", "ops": []any{map[string]any{"op": "set"}}}
	t.Run("publish only the saved cutoff", func(t *testing.T) {
		ts, srv := newTestServer(t, "", dir)
		alice := join(t, ts, "Hotel", "", "Alice")
		hash := putBlob(t, ts.URL, alice.blobToken, []byte("map"))
		for i := 1; i <= 2; i++ {
			send(t, alice.conn, op)
			if got := expect(t, alice.conn, "opsApplied")["seq"]; got != float64(i) {
				t.Fatalf("seq=%v, want %d", got, i)
			}
		}
		send(t, alice.conn, map[string]any{"type": "pkg.publish", "package": target, "hash": hash, "size": 3, "since": 1})
		record := expect(t, alice.conn, "pkgPublished")["record"].(map[string]any)
		if record["seq"] != float64(1) || record["cutoff"] != float64(1) {
			t.Fatalf("unexpected baseline: %v", record)
		}
		var count int
		if err := srv.db.sql.QueryRow("SELECT count(*) FROM ops WHERE target=?", target).Scan(&count); err != nil || count != 1 {
			t.Fatalf("publish was acknowledged before compaction or lost a later edit: count=%d err=%v", count, err)
		}
		// Compaction leaves an empty journal while the head keeps its sequence.
		send(t, alice.conn, map[string]any{"type": "pkg.publish", "package": target, "hash": hash, "size": 3, "since": 2, "revision": 1})
		expect(t, alice.conn, "pkgPublished")
	})
	t.Run("resume from committed head", func(t *testing.T) {
		ts, _ := newTestServer(t, "", dir)
		alice := join(t, ts, "Hotel", "", "Alice")
		send(t, alice.conn, op)
		expect(t, alice.conn, "contentConflict")
		op["revision"] = 2
		send(t, alice.conn, op)
		if got := expect(t, alice.conn, "opsApplied")["seq"]; got != float64(3) {
			t.Fatalf("reused a journal sequence: %v", got)
		}
		send(t, alice.conn, map[string]any{"type": "ops.sync", "target": target, "revision": 2, "since": 2})
		entries := expect(t, alice.conn, "opsBatch")["entries"].([]any)
		if len(entries) != 1 || entries[0].(map[string]any)["seq"] != float64(3) {
			t.Fatalf("wrong replay after restart: %v", entries)
		}
	})
}

func TestContentPersistenceFailureDoesNotAcknowledge(t *testing.T) {
	ts, srv := newTestServer(t, "", t.TempDir())
	alice := join(t, ts, "Hotel", "", "Alice")
	if _, err := srv.db.sql.Exec(`CREATE TRIGGER fail_ops BEFORE INSERT ON ops BEGIN SELECT RAISE(ABORT, 'write failed'); END`); err != nil {
		t.Fatal(err)
	}
	op := map[string]any{"type": "ops", "target": "/Game/Failure", "kind": "level", "ops": []any{map[string]any{"op": "set"}}}
	send(t, alice.conn, op)
	if got := expect(t, alice.conn, "error")["code"]; got != "storage" {
		t.Fatalf("expected storage error, got %v", got)
	}
	if _, err := srv.db.sql.Exec("DROP TRIGGER fail_ops"); err != nil {
		t.Fatal(err)
	}
	send(t, alice.conn, op)
	if got := expect(t, alice.conn, "opsApplied")["seq"]; got != float64(1) {
		t.Fatalf("failed operation advanced cursor: %v", got)
	}
}

func TestContentCompatibilityAndNormalizedBlobReference(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())
	greeting := hello("Hotel", "", "Alice")
	greeting["engineVersion"], greeting["commit"] = "5.8", "abc"
	alice := joinWith(t, ts, greeting)
	op := json.RawMessage(`[{"op":"set","packed":true,"b\u006cob":"1234567890abcdef1234567890abcdef"}]`)
	send(t, alice.conn, map[string]any{"type": "ops", "target": "/Game/Map", "kind": "level", "base": "abc", "ops": op})
	if got := expect(t, alice.conn, "error")["reason"]; got != "missingBlob" {
		t.Fatalf("escaped blob key was not validated: %v", got)
	}
	bob := dial(t, ts)
	greeting = hello("Hotel", "", "Bob")
	greeting["engineVersion"], greeting["commit"] = "5.8", "def"
	send(t, bob, greeting)
	for _, message := range []string{"welcome", "roster", "chatHistory", "comments", "assets"} {
		expect(t, bob, message)
	}
	if got := expect(t, bob, "notice")["code"]; got != "commit" {
		t.Fatalf("expected compatibility notice, got %v", got)
	}
	expect(t, bob, "packages")
	expect(t, alice.conn, "notice")
	expect(t, alice.conn, "presence")
	send(t, bob, map[string]any{"type": "ops", "target": "/Game/Map", "kind": "level", "base": "def", "ops": []any{map[string]any{"op": "set"}}})
	send(t, bob, map[string]any{"type": "chat", "text": "presence is still allowed"})
	expect(t, alice.conn, "chat")
}
