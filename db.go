package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Everything that outlives a connection is kept in one SQLite file: the whole chat history, comment threads and
// the modified-file records. Rooms in use are mirrored in memory (see roomData); every change is applied in memory
// first, under server.mu, then queued for the single writer goroutine, which commits whatever is waiting in one
// transaction. Busy rooms therefore never wait for the disk, and nothing is lost on a clean shutdown.

const (
	databaseFileName      = "collab.db"
	chatPageSize          = 100
	maxMessagesPerComment = 500
	maxCommentsPerRoom    = 2000
	maxChatRowsPerRoom    = 10000
	maxAssetsPerRoom      = 4000
	assetRecordMaxAge     = 30 * 24 * time.Hour
	writeQueueSize        = 8192
	maxWritesPerCommit    = 1024
)

const schema = `
CREATE TABLE IF NOT EXISTS chat (
	seq       INTEGER PRIMARY KEY,
	room      TEXT NOT NULL,
	id        TEXT NOT NULL UNIQUE,
	author_id TEXT NOT NULL,
	author    TEXT NOT NULL,
	color     TEXT NOT NULL,
	text      TEXT NOT NULL,
	at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS chat_by_room ON chat(room, seq);

CREATE TABLE IF NOT EXISTS comments (
	seq        INTEGER PRIMARY KEY,
	room       TEXT NOT NULL,
	id         TEXT NOT NULL UNIQUE,
	anchor     TEXT NOT NULL,
	resolved   INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	messages   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_by_room ON comments(room, seq);

CREATE TABLE IF NOT EXISTS assets (
	room      TEXT NOT NULL,
	package   TEXT NOT NULL,
	author    TEXT NOT NULL,
	asset     TEXT NOT NULL,
	hash      TEXT NOT NULL,
	dirty     INTEGER NOT NULL,
	working   INTEGER NOT NULL DEFAULT 0,
	author_id TEXT NOT NULL,
	color     TEXT NOT NULL,
	at        INTEGER NOT NULL,
	PRIMARY KEY (room, package, author)
);
`

// assetRecord is the last known state of one package file on one collaborator's machine.
// Other clients compare Hash with their own copy: identical content means the change has reached them (git pull).
type assetRecord struct {
	Package  string `json:"package"`
	Asset    string `json:"asset"`
	Hash     string `json:"hash"`
	Dirty    bool   `json:"dirty"`
	Working  bool   `json:"working"`
	AuthorID string `json:"authorId"`
	Author   string `json:"author"`
	Color    string `json:"color"`
	At       int64  `json:"at"`
}

type chatMessage struct {
	ID       string `json:"id"`
	AuthorID string `json:"authorId"`
	Author   string `json:"author"`
	Color    string `json:"color"`
	Text     string `json:"text"`
	At       int64  `json:"at"`
	EditedAt int64  `json:"editedAt,omitempty"`
}

type comment struct {
	ID        string          `json:"id"`
	Anchor    json.RawMessage `json:"anchor"`
	Resolved  bool            `json:"resolved"`
	CreatedAt int64           `json:"createdAt"`
	Messages  []chatMessage   `json:"messages"`
}

type writeOp struct {
	apply func(tx *sql.Tx) error
	// done is closed once every write queued before this one is committed.
	done chan struct{}
}

type database struct {
	sql    *sql.DB
	writes chan writeOp
	closed chan struct{}

	mu       sync.RWMutex
	isClosed bool
}

// ensureColumn adds a column to databases created before it existed. Fresh databases already have it.
func ensureColumn(handle *sql.DB, table, column, decl string) error {
	rows, err := handle.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = handle.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

func openDatabase(dir string) (*database, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("cannot create data directory %q: %w", dir, err)
	}
	path := filepath.Join(dir, databaseFileName)
	pragmas := "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(10000)&_pragma=temp_store(MEMORY)"
	handle, err := sql.Open("sqlite", path+pragmas)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", path, err)
	}
	if _, err := handle.Exec(schema); err != nil {
		handle.Close()
		return nil, fmt.Errorf("cannot prepare %s: %w", path, err)
	}
	if err := ensureColumn(handle, "assets", "working", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		handle.Close()
		return nil, err
	}
	db := &database{sql: handle, writes: make(chan writeOp, writeQueueSize), closed: make(chan struct{})}
	go db.writeLoop()
	if err := db.importLegacyRooms(dir); err != nil {
		db.close()
		return nil, err
	}
	return db, nil
}

// enqueue schedules a write. It only blocks when the writer is thousands of operations behind.
func (d *database) enqueue(apply func(tx *sql.Tx) error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if !d.isClosed {
		d.writes <- writeOp{apply: apply}
	}
}

// flush waits until every write queued so far is committed.
func (d *database) flush() {
	done := make(chan struct{})
	d.mu.RLock()
	if d.isClosed {
		d.mu.RUnlock()
		return
	}
	d.writes <- writeOp{done: done}
	d.mu.RUnlock()
	<-done
}

// close commits the pending writes. Safe to call more than once.
func (d *database) close() {
	d.mu.Lock()
	wasOpen := !d.isClosed
	if wasOpen {
		d.isClosed = true
		close(d.writes)
	}
	d.mu.Unlock()
	<-d.closed
	if wasOpen {
		d.sql.Close()
	}
}

func (d *database) writeLoop() {
	defer close(d.closed)
	batch := make([]writeOp, 0, maxWritesPerCommit)
	for first := range d.writes {
		batch = append(batch[:0], first)
	drain:
		for len(batch) < maxWritesPerCommit {
			select {
			case op, ok := <-d.writes:
				if !ok {
					break drain
				}
				batch = append(batch, op)
			default:
				break drain
			}
		}
		d.commit(batch)
	}
}

func (d *database) commit(batch []writeOp) {
	tx, err := d.sql.Begin()
	if err != nil {
		log.Print(T("server.txStart", len(batch), err))
	} else {
		for _, op := range batch {
			if op.apply == nil {
				continue
			}
			if err := op.apply(tx); err != nil {
				log.Print(T("server.saveChange", err))
			}
		}
		if err := tx.Commit(); err != nil {
			log.Print(T("server.commit", len(batch), err))
		}
	}
	for _, op := range batch {
		if op.done != nil {
			close(op.done)
		}
	}
}

// roomData is the in-memory part of a room in use: all its comments and modified files, and only the most recent
// chat messages (older ones are read from the database on request). Requires server.mu.
type roomData struct {
	db       *database
	name     string
	Comments []*comment
	// Chat holds the last chatPageSize messages; ChatComplete tells whether there is nothing older.
	Chat         []chatMessage
	ChatComplete bool
	Assets       []assetRecord
	// pending is drained by the caller after it releases server.mu, so a full write queue cannot stall every room.
	pending []func(tx *sql.Tx) error
}

func (d *roomData) deferWrite(apply func(tx *sql.Tx) error) {
	d.pending = append(d.pending, apply)
}

func (d *roomData) takePending() []func(tx *sql.Tx) error {
	out := d.pending
	d.pending = nil
	return out
}

func (s *server) persist(writes []func(tx *sql.Tx) error) {
	if s == nil || s.db == nil {
		return
	}
	for _, apply := range writes {
		s.db.enqueue(apply)
	}
}

// loadRoom reads a room from the database. Called without server.mu.
func (d *database) loadRoom(name string) (*roomData, error) {
	cutoff := time.Now().Add(-assetRecordMaxAge).UnixMilli()
	d.enqueue(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM assets WHERE room = ? AND at < ?`, name, cutoff); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM comments WHERE room = ? AND seq NOT IN (
			SELECT seq FROM comments WHERE room = ? ORDER BY seq DESC LIMIT ?)`, name, name, maxCommentsPerRoom)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM chat WHERE room = ? AND seq NOT IN (
			SELECT seq FROM chat WHERE room = ? ORDER BY seq DESC LIMIT ?)`, name, name, maxChatRowsPerRoom)
		return err
	})
	// Includes the writes of a previous session of the same room that may still be queued.
	d.flush()

	data := &roomData{db: d, name: name, Comments: []*comment{}, Assets: []assetRecord{}}
	var err error
	if data.Chat, data.ChatComplete, err = d.chatPage(name, ""); err != nil {
		return nil, err
	}

	rows, err := d.sql.Query(`SELECT id, anchor, resolved, created_at, messages FROM comments WHERE room = ? ORDER BY seq`, name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var thread comment
		var anchor, messages string
		if err := rows.Scan(&thread.ID, &anchor, &thread.Resolved, &thread.CreatedAt, &messages); err != nil {
			rows.Close()
			return nil, err
		}
		thread.Anchor = json.RawMessage(anchor)
		if json.Unmarshal([]byte(messages), &thread.Messages) != nil {
			thread.Messages = []chatMessage{}
		}
		data.Comments = append(data.Comments, &thread)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = d.sql.Query(`SELECT package, asset, hash, dirty, working, author_id, author, color, at FROM assets WHERE room = ? ORDER BY at`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r assetRecord
		if err := rows.Scan(&r.Package, &r.Asset, &r.Hash, &r.Dirty, &r.Working, &r.AuthorID, &r.Author, &r.Color, &r.At); err != nil {
			return nil, err
		}
		data.Assets = append(data.Assets, r)
	}
	return data, rows.Err()
}

// chatPage returns up to chatPageSize messages older than beforeID (the latest ones when empty), oldest first,
// and whether they reach the start of the history.
func (d *database) chatPage(room, beforeID string) ([]chatMessage, bool, error) {
	query := `SELECT id, author_id, author, color, text, at FROM chat WHERE room = ? ORDER BY seq DESC LIMIT ?`
	args := []any{room, chatPageSize + 1}
	if beforeID != "" {
		query = `SELECT id, author_id, author, color, text, at FROM chat
			WHERE room = ? AND seq < (SELECT seq FROM chat WHERE id = ? AND room = ?) ORDER BY seq DESC LIMIT ?`
		args = []any{room, beforeID, room, chatPageSize + 1}
	}
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	messages := []chatMessage{}
	for rows.Next() {
		var m chatMessage
		if err := rows.Scan(&m.ID, &m.AuthorID, &m.Author, &m.Color, &m.Text, &m.At); err != nil {
			return nil, false, err
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	complete := len(messages) <= chatPageSize
	if !complete {
		messages = messages[:chatPageSize]
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, complete, nil
}

func (d *roomData) findComment(id string) (*comment, int) {
	for index, c := range d.Comments {
		if c.ID == id {
			return c, index
		}
	}
	return nil, -1
}

func (d *roomData) addChat(m chatMessage) {
	d.Chat = append(d.Chat, m)
	if overflow := len(d.Chat) - chatPageSize; overflow > 0 {
		d.Chat = append([]chatMessage(nil), d.Chat[overflow:]...)
		d.ChatComplete = false
	}
	room := d.name
	d.deferWrite(func(tx *sql.Tx) error {
		if err := insertChat(tx, room, m); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM chat WHERE room = ? AND seq NOT IN (
			SELECT seq FROM chat WHERE room = ? ORDER BY seq DESC LIMIT ?)`, room, room, maxChatRowsPerRoom)
		return err
	})
}

func insertChat(tx *sql.Tx, room string, m chatMessage) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO chat (room, id, author_id, author, color, text, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		room, m.ID, m.AuthorID, m.Author, m.Color, m.Text, m.At)
	return err
}

func (d *roomData) addComment(thread *comment) {
	d.Comments = append(d.Comments, thread)
	d.saveComment(thread, true)
}

// saveComment captures the thread now: it may change again before the writer runs.
func (d *roomData) saveComment(thread *comment, isNew bool) {
	room, id, anchor, resolved, createdAt := d.name, thread.ID, string(thread.Anchor), thread.Resolved, thread.CreatedAt
	messages := string(mustJSON(thread.Messages))
	if isNew {
		d.deferWrite(func(tx *sql.Tx) error { return insertComment(tx, room, id, anchor, resolved, createdAt, messages) })
		return
	}
	d.deferWrite(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE comments SET anchor = ?, resolved = ?, messages = ? WHERE id = ?`, anchor, resolved, messages, id)
		return err
	})
}

func insertComment(tx *sql.Tx, room, id, anchor string, resolved bool, createdAt int64, messages string) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO comments (room, id, anchor, resolved, created_at, messages) VALUES (?, ?, ?, ?, ?, ?)`,
		room, id, anchor, resolved, createdAt, messages)
	return err
}

func (d *roomData) deleteComment(index int) {
	id := d.Comments[index].ID
	d.Comments = append(d.Comments[:index], d.Comments[index+1:]...)
	d.deferWrite(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM comments WHERE id = ?`, id)
		return err
	})
}

// clearWorking drops the soft lock of one author. A Git LFS lock is not stored here.
func (d *roomData) clearWorking(authorID string) []assetRecord {
	var updated []assetRecord
	for i := range d.Assets {
		if d.Assets[i].AuthorID == authorID && d.Assets[i].Working {
			d.Assets[i].Working = false
			record := d.Assets[i]
			updated = append(updated, record)
			room := d.name
			saved := record
			d.deferWrite(func(tx *sql.Tx) error { return upsertAsset(tx, room, saved) })
		}
	}
	return updated
}

// upsertAsset keeps one record per package and author id, and one per package and display name. A name used by
// another connected collaborator is refused, so renaming cannot steal their row. A record left under that name by
// an author who is gone (a protocol 5 row carries a connection id) is taken over. previousName is set when the
// caller's own record changes name. Requires server.mu.
func (d *roomData) upsertAsset(record assetRecord, online func(authorID string) bool) (previousName string, ok bool) {
	own, left := -1, -1
	for index, existing := range d.Assets {
		if existing.Package != record.Package {
			continue
		}
		if existing.AuthorID == record.AuthorID {
			own = index
		} else if existing.Author == record.Author {
			if online(existing.AuthorID) {
				return "", false
			}
			left = index
		}
	}
	switch {
	case own >= 0:
		if d.Assets[own].Author != record.Author {
			previousName = d.Assets[own].Author
		}
		d.Assets[own] = record
		if left >= 0 {
			d.Assets = append(d.Assets[:left], d.Assets[left+1:]...)
		}
	case left >= 0:
		d.Assets[left] = record
	default:
		if len(d.Assets) >= maxAssetsPerRoom {
			return "", false
		}
		d.Assets = append(d.Assets, record)
	}
	room := d.name
	d.deferWrite(func(tx *sql.Tx) error { return upsertAsset(tx, room, record) })
	return previousName, true
}

// upsertAsset writes what roomData.upsertAsset keeps: the row replaces those of the same package with the same
// author id or the same display name. Rows imported without an author id only match by name.
func upsertAsset(tx *sql.Tx, room string, r assetRecord) error {
	if err := deleteAssetRows(tx, room, r.Package, r.Author, r.AuthorID); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO assets (room, package, author, asset, hash, dirty, working, author_id, color, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		room, r.Package, r.Author, r.Asset, r.Hash, r.Dirty, r.Working, r.AuthorID, r.Color, r.At)
	return err
}

func deleteAssetRows(tx *sql.Tx, room, pkg, author, authorID string) error {
	_, err := tx.Exec(`DELETE FROM assets WHERE room = ? AND package = ? AND (author = ? OR (author_id = ? AND author_id <> ''))`,
		room, pkg, author, authorID)
	return err
}

// removeAsset drops the caller's own record, or the one left under the caller's name by an author who is gone.
// Requires server.mu.
func (d *roomData) removeAsset(pkg, authorID, name string, online func(authorID string) bool) (assetRecord, bool) {
	found := -1
	for index, existing := range d.Assets {
		if existing.Package != pkg {
			continue
		}
		if existing.AuthorID == authorID {
			found = index
			break
		}
		if existing.Author == name && !online(existing.AuthorID) {
			found = index
		}
	}
	if found < 0 {
		return assetRecord{}, false
	}
	record := d.Assets[found]
	d.Assets = append(d.Assets[:found], d.Assets[found+1:]...)
	room := d.name
	d.deferWrite(func(tx *sql.Tx) error { return deleteAssetRows(tx, room, pkg, record.Author, record.AuthorID) })
	return record, true
}

type assetRename struct {
	record       assetRecord
	previousName string
}

// renameAuthor puts a connection's new display name on its records, with the rules of upsertAsset: a package where
// a connected collaborator already uses that name keeps the old one. Requires server.mu.
func (d *roomData) renameAuthor(authorID, name string, online func(authorID string) bool) []assetRename {
	var stale []assetRecord
	for _, existing := range d.Assets {
		if existing.AuthorID == authorID && existing.Author != name {
			stale = append(stale, existing)
		}
	}
	var changes []assetRename
	for _, record := range stale {
		record.Author = name
		if previous, ok := d.upsertAsset(record, online); ok {
			changes = append(changes, assetRename{record: record, previousName: previous})
		}
	}
	return changes
}

// importLegacyRooms moves the room-*.json files of server versions before 1.2 into the database, once.
func (d *database) importLegacyRooms(dir string) error {
	files, _ := filepath.Glob(filepath.Join(dir, "room-*.json"))
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", path, err)
		}
		var legacy struct {
			Room     string        `json:"room"`
			Comments []*comment    `json:"comments"`
			Chat     []chatMessage `json:"chat"`
			Assets   []assetRecord `json:"assets"`
		}
		if err := json.Unmarshal(raw, &legacy); err != nil || strings.TrimSpace(legacy.Room) == "" {
			log.Print(T("server.notRoom", path))
			continue
		}
		if err := d.importLegacyRoom(legacy.Room, legacy.Chat, legacy.Comments, legacy.Assets); err != nil {
			return fmt.Errorf("cannot import %s: %w", path, err)
		}
		if err := os.Rename(path, path+".imported"); err != nil {
			return fmt.Errorf("cannot rename %s after import: %w", path, err)
		}
		log.Print(T("server.imported", filepath.Base(path), legacy.Room, len(legacy.Chat), len(legacy.Comments), len(legacy.Assets)))
	}
	return nil
}

// importLegacyRoom runs at startup, before any client, in its own transaction so that a failure keeps the file.
func (d *database) importLegacyRoom(room string, chat []chatMessage, comments []*comment, assets []assetRecord) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, m := range chat {
		if err := insertChat(tx, room, m); err != nil {
			return err
		}
	}
	for _, thread := range comments {
		if thread == nil || thread.ID == "" {
			continue
		}
		if thread.Messages == nil {
			thread.Messages = []chatMessage{}
		}
		if err := insertComment(tx, room, thread.ID, string(thread.Anchor), thread.Resolved, thread.CreatedAt, string(mustJSON(thread.Messages))); err != nil {
			return err
		}
	}
	for _, r := range assets {
		if err := upsertAsset(tx, room, r); err != nil {
			return err
		}
	}
	return tx.Commit()
}
