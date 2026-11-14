package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxOpsPerMessage = 64
	maxOpsPageBytes  = 48 << 10
	maxSnapshotBytes = 48 << 10
	maxBaseLength    = 64
)

// packageRecord is the last published file of one package. Hash names the blob that holds the bytes.
type packageRecord struct {
	Package  string `json:"package"`
	Hash     string `json:"hash"`
	Size     int64  `json:"size"`
	Base     string `json:"base"`
	AuthorID string `json:"authorId"`
	Author   string `json:"author"`
	Color    string `json:"color"`
	Seq      int64  `json:"seq"`
	Cutoff   int64  `json:"cutoff"`
	Engine   string `json:"engine"`
	At       int64  `json:"at"`
}

type packageRemoval struct {
	Package  string `json:"package"`
	AuthorID string `json:"authorId"`
	To       string `json:"to,omitempty"`
	Revision int64  `json:"revision"`
	Cutoff   int64  `json:"cutoff"`
}

const removalSelect = `SELECT r.package,r.author_id,r.renamed_to,h.revision,h.cutoff
    FROM package_removals r JOIN content_heads h ON h.room=r.room AND h.target=r.package`

func (r packageRemoval) event() map[string]any {
	return map[string]any{"type": "pkgDeleted", "package": r.Package, "authorId": r.AuthorID,
		"to": r.To, "revision": r.Revision, "cutoff": r.Cutoff}
}

func removePackage(tx *sql.Tx, c *client, target, renamedTo string, head contentHead) (packageRemoval, error) {
	room := c.room.name
	if _, err := tx.Exec(`DELETE FROM packages WHERE room=? AND package=?`, room, target); err != nil {
		return packageRemoval{}, err
	}
	if _, err := tx.Exec(`DELETE FROM ops WHERE room=? AND target=?`, room, target); err != nil {
		return packageRemoval{}, err
	}
	head.revision++
	head.cutoff, head.base, head.engine = head.seq, c.commit, c.engine
	if err := head.save(tx, room, target); err != nil {
		return packageRemoval{}, err
	}
	_, err := tx.Exec(`INSERT INTO package_removals(room,package,author_id,renamed_to)
        VALUES (?,?,?,?) ON CONFLICT(room,package) DO UPDATE SET
        author_id=excluded.author_id,renamed_to=excluded.renamed_to`, room, target, c.author, renamedTo)
	return packageRemoval{target, c.author, renamedTo, head.revision, head.cutoff}, err
}

type storedOp struct {
	OperationID string          `json:"operationId,omitempty"`
	Seq         int64           `json:"seq"`
	Kind        string          `json:"kind"`
	AuthorID    string          `json:"authorId"`
	Ops         json.RawMessage `json:"ops"`
	At          int64           `json:"at"`
}

type contentHead struct {
	seq, revision, cutoff int64
	base, engine          string
}

func readHead(db interface{ QueryRow(string, ...any) *sql.Row }, room, target string) (contentHead, error) {
	var head contentHead
	err := db.QueryRow(`SELECT seq, revision, cutoff, base, engine FROM content_heads WHERE room = ? AND target = ?`,
		room, target).Scan(&head.seq, &head.revision, &head.cutoff, &head.base, &head.engine)
	if err == sql.ErrNoRows {
		err = nil
	}
	return head, err
}

func (h contentHead) save(tx *sql.Tx, room, target string) error {
	_, err := tx.Exec(`INSERT INTO content_heads (room, target, seq, revision, cutoff, base, engine)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(room, target) DO UPDATE SET
		seq=excluded.seq, revision=excluded.revision, cutoff=excluded.cutoff, base=excluded.base, engine=excluded.engine`,
		room, target, h.seq, h.revision, h.cutoff, h.base, h.engine)
	return err
}

func (h contentHead) matches(c *client) bool {
	return h.base == c.commit && h.engine == c.engine
}

func (s *server) handleSync(c *client, msg inbound) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.room == nil || c.room.data == nil || sessionNotice(c.room, c) != nil {
		return
	}
	var batch []delivery
	if err := s.applySync(c, msg, &batch); err != nil {
		log.Printf("content transaction: %v", err)
		c.enqueue(errorMessage("storage", "", "The server could not save the content update."))
		return
	}
	deliver(batch)
}

func (s *server) applySync(c *client, msg inbound, batch *[]delivery) error {
	room := c.room.name
	if msg.Type == "pkg.publish" || msg.Type == "pkg.delete" || msg.Type == "ops" {
		return s.commitContent(c, msg, batch)
	}
	switch msg.Type {
	case "ops.preview":
		ops, ok := cleanOps(msg.Ops)
		if !ok || !validPackageName(msg.Target) || !s.roomOpsBlobsExist(room, ops) {
			return nil
		}
		if !s.currentContentRevision(c, msg) {
			return nil
		}
		queueBroadcast(c.room, c.id, map[string]any{
			"type": "opsPreview", "target": msg.Target, "authorId": c.author, "ops": json.RawMessage(ops), "revision": msg.Revision,
		}, batch)

	case "graph.resync":
		if !validPackageName(msg.Target) || !validGraphKey(msg.Graph) || !s.currentContentRevision(c, msg) {
			return nil
		}
		queueBroadcast(c.room, c.id, map[string]any{
			"type": "graphResync", "target": msg.Target, "graph": msg.Graph, "authorId": c.author, "revision": msg.Revision,
		}, batch)

	case "graph.snapshot":
		if !validPackageName(msg.Target) || !validGraphKey(msg.Graph) || !s.currentContentRevision(c, msg) {
			return nil
		}
		if len(msg.Text) > maxSnapshotBytes || (msg.Text == "" && !validBlobHash(msg.Blob)) {
			return nil
		}
		if msg.Blob != "" && !s.roomBlobExists(room, msg.Blob) {
			c.enqueue(errorMessage("sync", "missingBlob", "The snapshot file was not uploaded."))
			return nil
		}
		payload := map[string]any{
			"type": "graphSnapshot", "target": msg.Target, "graph": msg.Graph, "authorId": c.author, "revision": msg.Revision,
		}
		if msg.Text != "" {
			payload["text"] = msg.Text
		}
		if msg.Blob != "" {
			payload["blob"] = msg.Blob
		}
		queueBroadcast(c.room, "", payload, batch)
	}
	return nil
}

func (s *server) currentContentRevision(c *client, msg inbound) bool {
	head, err := readHead(s.db.sql, c.room.name, msg.Target)
	return err == nil && head.revision == msg.Revision && (head == (contentHead{}) || head.matches(c))
}

// A content commit is serialized with room membership, replay and publication. A failed
// transaction leaves the in-memory package list unchanged and sends no success event.
func (s *server) commitContent(c *client, msg inbound, batch *[]delivery) error {
	room, target := c.room.name, msg.Target
	if msg.Type != "ops" {
		target = msg.Package
	}
	if !validPackageName(target) {
		return nil
	}
	conflict := func() {
		c.enqueue(mustJSON(map[string]any{"type": "contentConflict", "target": target, "operationId": msg.OperationID}))
	}
	ack := func(seq int64) {
		if msg.OperationID != "" {
			c.enqueue(mustJSON(map[string]any{"type": "opsAck", "target": target, "operationId": msg.OperationID, "seq": seq}))
		}
	}
	if msg.Base != c.commit {
		conflict()
		return nil
	}

	if msg.OperationID != "" {
		if msg.Type != "ops" || !validClientKey(msg.OperationID) {
			return nil
		}
		var previous string
		var seq int64
		err := s.db.sql.QueryRow(`SELECT target,seq FROM op_receipts WHERE room=? AND author_id=? AND operation_id=?`,
			room, c.author, msg.OperationID).Scan(&previous, &seq)
		if err == nil {
			if previous == target {
				ack(seq)
			} else {
				conflict()
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
	}
	if msg.Type == "pkg.publish" && msg.From != "" &&
		(!validPackageName(msg.From) || msg.From == target) {
		return nil
	}

	var record packageRecord
	var ops json.RawMessage
	var ok bool
	switch msg.Type {
	case "pkg.publish":
		record, ok = c.newPackageRecord(msg)
		if !ok || !s.roomBlobExists(room, record.Hash) {
			c.enqueue(errorMessage("sync", "missingBlob", "The published file was not uploaded to this room."))
			return nil
		}
		info, err := os.Stat(s.blobPath(record.Hash))
		if err != nil {
			return err
		}
		if info.Size() != record.Size {
			c.enqueue(errorMessage("sync", "missingBlob", "The uploaded file size does not match the publication."))
			return nil
		}
	case "ops":
		ops, ok = cleanOps(msg.Ops)
		if !ok || (msg.Kind != "level" && msg.Kind != "graph") {
			return nil
		}
		if !s.roomOpsBlobsExist(room, ops) {
			c.enqueue(errorMessage("sync", "missingBlob", "An operation references a file not uploaded to this room."))
			return nil
		}
	}

	tx, err := s.db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	head, err := readHead(tx, room, target)
	if err != nil {
		return err
	}
	fresh := head == (contentHead{})
	sameBase := fresh || head.matches(c)
	expected := head.revision
	if !sameBase && msg.Type == "pkg.publish" {
		expected = 0 // Explicit publication establishes a baseline for a new checkout.
	}
	if msg.Revision != expected || (!sameBase && msg.Type != "pkg.publish") ||
		(msg.Type == "pkg.publish" && sameBase && (msg.Since < head.cutoff || msg.Since > head.seq)) {
		conflict()
		return nil
	}

	var removed packageRemoval
	var hasRemoval bool
	if msg.Type == "ops" {
		var deleted bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM package_removals WHERE room=? AND package=?)`,
			room, target).Scan(&deleted); err != nil {
			return err
		}
		if deleted {
			conflict()
			return nil
		}
	}
	switch msg.Type {
	case "pkg.publish":
		if msg.From != "" {
			source, err := readHead(tx, room, msg.From)
			if err != nil {
				return err
			}
			if source.revision != msg.FromRevision || source.seq != msg.FromSince || (source != (contentHead{}) && !source.matches(c)) {
				conflict()
				return nil
			}
			removed, err = removePackage(tx, c, msg.From, target, source)
			if err != nil {
				return err
			}
			hasRemoval = true
		}
		if _, err := tx.Exec(`DELETE FROM package_removals WHERE room=? AND package=?`, room, target); err != nil {
			return err
		}
		head.revision++
		head.cutoff = msg.Since
		if !sameBase {
			head.cutoff = head.seq
		}
		head.base, head.engine = c.commit, c.engine
		record.Seq, record.Cutoff, record.Engine = head.revision, head.cutoff, c.engine
		if _, err := tx.Exec(`INSERT INTO packages (room, package, hash, size, base, author_id, author, color, seq, at, cutoff, engine)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(room, package) DO UPDATE SET
			hash=excluded.hash, size=excluded.size, base=excluded.base, author_id=excluded.author_id,
			author=excluded.author, color=excluded.color, seq=excluded.seq, at=excluded.at, cutoff=excluded.cutoff, engine=excluded.engine`,
			room, target, record.Hash, record.Size, record.Base, record.AuthorID, record.Author, record.Color,
			record.Seq, record.At, record.Cutoff, record.Engine); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM ops WHERE room = ? AND target = ? AND seq <= ?`, room, target, head.cutoff); err != nil {
			return err
		}
	case "pkg.delete":
		removed, err = removePackage(tx, c, target, "", head)
		if err != nil {
			return err
		}
		hasRemoval = true
	case "ops":
		head.seq++
		head.base, head.engine = c.commit, c.engine
		if _, err := tx.Exec(`INSERT INTO ops (room, target, seq, kind, author_id, ops, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			room, target, head.seq, msg.Kind, c.author, string(ops), time.Now().UnixMilli()); err != nil {
			return err
		}
		if msg.OperationID != "" {
			if _, err := tx.Exec(`INSERT INTO op_receipts(room,author_id,operation_id,target,seq) VALUES(?,?,?,?,?)`,
				room, c.author, msg.OperationID, target, head.seq); err != nil {
				return err
			}
		}
	}
	if msg.Type != "pkg.delete" {
		if err := head.save(tx, room, target); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	switch msg.Type {
	case "pkg.publish":
		c.room.data.setPackage(target, &record)
		queueBroadcast(c.room, "", map[string]any{"type": "pkgPublished", "record": record}, batch)
	case "ops":
		queueBroadcast(c.room, "", map[string]any{"type": "opsApplied", "target": target, "seq": head.seq,
			"revision": head.revision, "kind": msg.Kind, "authorId": c.author, "senderId": c.id, "operationId": msg.OperationID, "ops": ops}, batch)
	}
	if hasRemoval {
		c.room.data.setPackage(removed.Package, nil)
		queueBroadcast(c.room, "", removed.event(), batch)
	}
	return nil
}

func (s *server) answerOpsSync(c *client, target string, since, revision int64) {
	if !validPackageName(target) || since < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.room == nil || sessionNotice(c.room, c) != nil {
		return
	}
	if err := s.replay(c, target, since, revision); err != nil {
		log.Printf("content replay: %v", err)
		c.enqueue(errorMessage("storage", "", "The server cannot read its database."))
	}
}

func (s *server) replay(c *client, target string, since, revision int64) error {
	room := c.room.name
	head, err := readHead(s.db.sql, room, target)
	if err != nil {
		return err
	}
	if head != (contentHead{}) && !head.matches(c) {
		c.enqueue(mustJSON(map[string]any{"type": "contentBaseline", "target": target, "incompatible": true}))
		return nil
	}
	var removed packageRemoval
	err = s.db.sql.QueryRow(removalSelect+` WHERE r.room=? AND r.package=?`, room, target).
		Scan(&removed.Package, &removed.AuthorID, &removed.To, &removed.Revision, &removed.Cutoff)
	if err == nil {
		c.enqueue(mustJSON(removed.event()))
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if revision != head.revision || since < head.cutoff || since > head.seq {
		for _, record := range c.room.data.Packages {
			if record.Package == target {
				c.enqueue(mustJSON(map[string]any{"type": "pkgPublished", "record": record}))
				return nil
			}
		}
		c.enqueue(mustJSON(map[string]any{"type": "contentBaseline", "target": target,
			"revision": head.revision, "cutoff": head.cutoff}))
		return nil
	}
	rows, err := s.db.sql.Query(`SELECT o.seq,o.kind,o.author_id,o.ops,o.at,COALESCE(r.operation_id,'') FROM ops o
        LEFT JOIN op_receipts r ON r.room=o.room AND r.target=o.target AND r.seq=o.seq
        WHERE o.room=? AND o.target=? AND o.seq>? ORDER BY o.seq`,
		room, target, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	batch := []storedOp{}
	size := 0
	complete := true
	for rows.Next() {
		var entry storedOp
		var ops string
		if err := rows.Scan(&entry.Seq, &entry.Kind, &entry.AuthorID, &ops, &entry.At, &entry.OperationID); err != nil {
			return err
		}
		if len(batch) > 0 && size+len(ops) > maxOpsPageBytes {
			complete = false
			break
		}
		entry.Ops = json.RawMessage(ops)
		batch = append(batch, entry)
		size += len(ops)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.enqueue(mustJSON(map[string]any{"type": "opsBatch", "target": target, "since": since,
		"revision": head.revision, "entries": batch, "complete": complete}))
	return nil
}

func (s *server) sendPackages(c *client, rm *room) {
	records := []packageRecord{}
	for _, record := range rm.data.Packages {
		if record.Base == c.commit && record.Engine == c.engine {
			records = append(records, record)
		}
	}
	removals := []packageRemoval{}
	rows, err := s.db.sql.Query(removalSelect+` WHERE r.room=? AND h.base=? AND h.engine=? ORDER BY r.package`,
		rm.name, c.commit, c.engine)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var removed packageRemoval
			if err = rows.Scan(&removed.Package, &removed.AuthorID, &removed.To, &removed.Revision, &removed.Cutoff); err != nil {
				break
			}
			removals = append(removals, removed)
		}
		if err == nil {
			err = rows.Err()
		}
	}
	if err != nil {
		log.Printf("content snapshot: %v", err)
		c.enqueue(errorMessage("storage", "", "The server cannot read its database."))
		return
	}
	c.enqueue(mustJSON(map[string]any{"type": "packages", "records": records, "removals": removals}))
}

func migrateContent(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`INSERT OR IGNORE INTO content_heads (room,target,seq,revision,cutoff,base,engine)
			SELECT p.room,p.package,COALESCE(MAX(o.seq),0),p.seq,COALESCE(MAX(o.seq),0),p.base,p.engine
			FROM packages p LEFT JOIN ops o ON o.room=p.room AND o.target=p.package GROUP BY p.room,p.package`,
		`INSERT OR IGNORE INTO content_heads (room,target,seq,revision,cutoff,base,engine)
			SELECT room,target,MAX(seq),0,MAX(seq),'','' FROM ops GROUP BY room,target`,
		`UPDATE packages SET cutoff=(SELECT h.cutoff FROM content_heads h WHERE h.room=packages.room AND h.target=packages.package)
			WHERE engine=''`,
		`INSERT OR IGNORE INTO room_blobs (room,hash) SELECT room,hash FROM packages`,
	} {
		if _, err := tx.Exec(query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *client) newPackageRecord(msg inbound) (packageRecord, bool) {
	if !validPackageName(msg.Package) || !validBlobHash(strings.ToLower(msg.Hash)) || msg.Size < 0 || msg.Size > maxBlobBytes {
		return packageRecord{}, false
	}
	base := msg.Base
	if utf8.RuneCountInString(base) > maxBaseLength || strings.ContainsAny(base, "\x00\n\r") {
		return packageRecord{}, false
	}
	return packageRecord{
		Package: msg.Package, Hash: strings.ToLower(msg.Hash), Size: msg.Size, Base: base,
		AuthorID: c.author, Author: c.name, Color: stateString(c.state, "color"), At: time.Now().UnixMilli(),
	}, true
}

func (d *roomData) setPackage(target string, record *packageRecord) {
	for i := range d.Packages {
		if d.Packages[i].Package == target {
			if record == nil {
				d.Packages = append(d.Packages[:i], d.Packages[i+1:]...)
			} else {
				d.Packages[i] = *record
			}
			return
		}
	}
	if record != nil {
		d.Packages = append(d.Packages, *record)
	}
}

func cleanOps(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 || len(raw) > maxMessageBytes {
		return nil, false
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil || len(list) == 0 || len(list) > maxOpsPerMessage {
		return nil, false
	}
	for _, item := range list {
		var fields struct {
			Op   string `json:"op"`
			Blob string `json:"blob"`
		}
		if json.Unmarshal(item, &fields) != nil || fields.Op == "" || utf8.RuneCountInString(fields.Op) > 64 {
			return nil, false
		}
		if fields.Blob != "" && !validBlobHash(strings.ToLower(fields.Blob)) {
			return nil, false
		}
	}
	return raw, true
}

func opBlobHashes(ops json.RawMessage) []string {
	var items []struct {
		Blob string `json:"blob"`
	}
	if json.Unmarshal(ops, &items) != nil {
		return nil
	}
	var hashes []string
	for _, item := range items {
		if item.Blob != "" {
			hashes = append(hashes, strings.ToLower(item.Blob))
		}
	}
	return hashes
}

func (s *server) roomOpsBlobsExist(room string, ops json.RawMessage) bool {
	for _, hash := range opBlobHashes(ops) {
		if !s.roomBlobExists(room, hash) {
			return false
		}
	}
	return true
}

func validGraphKey(key string) bool {
	return key != "" && len(key) <= 1024 && utf8.ValidString(key) && !strings.ContainsAny(key, "\x00\n\r")
}

// sessionNotice warns when a joiner is not on the same engine build or git commit as someone already in the room.
// It does not refuse the connection: presence still works, and the client turns live sync off on its side.
func sessionNotice(rm *room, c *client) map[string]any {
	for _, other := range rm.clients {
		if c.engine != other.engine {
			return map[string]any{"type": "notice", "code": "engine", "message": "Collaborators are not on the same engine version."}
		}
		if c.commit != other.commit {
			return map[string]any{"type": "notice", "code": "commit", "message": "Collaborators are not on the same git commit."}
		}
	}
	return nil
}

func cleanVersion(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > maxBaseLength || strings.ContainsAny(value, "\x00\n\r") {
		return ""
	}
	return value
}
