package main

import (
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxBlobBytes = 256 << 20
	blobDirName  = "blobs"
	// An uploaded blob that no package or op references yet is kept this long, so a publish
	// that follows the upload is not raced by garbage collection.
	blobGrace = time.Hour
)

func validBlobHash(hash string) bool {
	if len(hash) != 32 {
		return false
	}
	for _, r := range hash {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (s *server) blobPath(hash string) string {
	return filepath.Join(s.cfg.dataDir, blobDirName, hash[:2], hash)
}

// Requires s.mu.
func (s *server) blobFromToken(r *http.Request) *client {
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || token == "" {
		return nil
	}
	c := s.blobTokens[token]
	if c == nil || c.room == nil || sessionNotice(c.room, c) != nil {
		return nil
	}
	return c
}

// Requires s.mu so collection cannot remove the file between authorization and use.
func (s *server) roomBlobExists(room, hash string) bool {
	if !validBlobHash(hash) || s.db == nil {
		return false
	}
	var present int
	if err := s.db.sql.QueryRow(`SELECT 1 FROM room_blobs WHERE room=? AND hash=?`, room, hash).Scan(&present); err != nil {
		return false
	}
	info, err := os.Stat(s.blobPath(hash))
	return err == nil && info.Mode().IsRegular()
}

func (s *server) handleBlobPut(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c := s.blobFromToken(r)
	s.mu.Unlock()
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hash := strings.ToLower(r.PathValue("hash"))
	if !validBlobHash(hash) {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	dest := s.blobPath(hash)

	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		http.Error(w, "storage", http.StatusInternalServerError)
		return
	}
	temp, err := os.CreateTemp(filepath.Dir(dest), hash+".*")
	if err != nil {
		http.Error(w, "storage", http.StatusInternalServerError)
		return
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	sum, strong := md5.New(), sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, sum, strong), io.LimitReader(r.Body, maxBlobBytes+1))
	if err != nil {
		temp.Close()
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	if written > maxBlobBytes {
		temp.Close()
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	if hex.EncodeToString(sum.Sum(nil)) != hash {
		temp.Close()
		http.Error(w, "hash mismatch", http.StatusBadRequest)
		return
	}
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if syncErr != nil || closeErr != nil {
		http.Error(w, "storage", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blobFromToken(r) != c {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Keep the protocol MD5 key, but compare SHA-256 before deduplicating any upload.
	// This prevents a colliding payload from granting access to another room's bytes.
	strongHash := hex.EncodeToString(strong.Sum(nil))
	info, err := os.Stat(dest)
	switch {
	case err == nil && info.Mode().IsRegular():
		existingHash, err := s.existingBlobDigest(hash, dest)
		if err != nil {
			http.Error(w, "storage", http.StatusInternalServerError)
			return
		}
		if info.Size() != written || existingHash != strongHash {
			http.Error(w, "hash collision", http.StatusConflict)
			return
		}
	case os.IsNotExist(err):
		if err := os.Rename(tempName, dest); err != nil {
			http.Error(w, "storage", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "storage", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	if err := os.Chtimes(dest, now, now); err != nil {
		http.Error(w, "storage", http.StatusInternalServerError)
		return
	}
	if err := s.grantBlob(c.room.name, hash, strongHash, written, now); err != nil {
		http.Error(w, "storage", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *server) existingBlobDigest(hash, path string) (string, error) {
	var digest string
	err := s.db.sql.QueryRow(`SELECT strong_hash FROM blobs WHERE hash=?`, hash).Scan(&digest)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if digest != "" {
		return digest, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func (s *server) grantBlob(room, hash, digest string, size int64, now time.Time) error {
	tx, err := s.db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO blobs (hash,size,at,strong_hash) VALUES (?,?,?,?)
		ON CONFLICT(hash) DO UPDATE SET size=excluded.size, at=excluded.at, strong_hash=excluded.strong_hash`,
		hash, size, now.UnixMilli(), digest); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO room_blobs (room,hash) VALUES (?,?)`, room, hash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *server) handleBlobGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c := s.blobFromToken(r)
	if c == nil {
		s.mu.Unlock()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hash := strings.ToLower(r.PathValue("hash"))
	if !validBlobHash(hash) {
		s.mu.Unlock()
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	if !s.roomBlobExists(c.room.name, hash) {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(s.blobPath(hash))
	s.mu.Unlock()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, hash, info.ModTime(), file)
}

// gcBlobs removes blob files that nothing references and that are older than blobGrace.
func (s *server) gcBlobs() {
	if s.db == nil || s.cfg.dataDir == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := map[string]bool{}
	collect := func(rows *sql.Rows) {
		if rows == nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			if rows.Scan(&value) == nil && validBlobHash(strings.ToLower(value)) {
				keep[strings.ToLower(value)] = true
			}
		}
	}
	rows, err := s.db.sql.Query(`SELECT hash FROM packages`)
	if err != nil {
		return
	}
	collect(rows)
	rows, err = s.db.sql.Query(`SELECT ops FROM ops`)
	if err != nil {
		return
	}
	func() {
		defer rows.Close()
		for rows.Next() {
			var text string
			if rows.Scan(&text) != nil {
				continue
			}
			for _, hash := range opBlobHashes([]byte(text)) {
				keep[hash] = true
			}
		}
	}()

	root := filepath.Join(s.cfg.dataDir, blobDirName)
	cutoff := time.Now().Add(-blobGrace)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return nil
		}
		hash := strings.ToLower(info.Name())
		if !validBlobHash(hash) || keep[hash] || info.ModTime().After(cutoff) {
			return nil
		}
		// Remove access before removing bytes so a later upload cannot revive stale grants.
		if _, err := s.db.sql.Exec(`DELETE FROM room_blobs WHERE hash = ?`, hash); err == nil {
			if os.Remove(path) == nil {
				_, _ = s.db.sql.Exec(`DELETE FROM blobs WHERE hash = ?`, hash)
			}
		}

		return nil
	})
}
