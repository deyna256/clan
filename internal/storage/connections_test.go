package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/credentialcipher"
)

func TestReportPoolDoesNotBlockOperationalConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	cipher, err := credentialcipher.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "pool.db"), cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key, err := accesskey.New(accesskey.Identity{ID: "key", Name: "test"}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, hash := accesskey.Generate()
	if err := s.CreateAccessKey(t.Context(), key, hash, 1); err != nil {
		t.Fatal(err)
	}
	acquire, release := context.WithTimeout(t.Context(), 2*time.Second)
	defer release()
	for range 4 {
		conn, err := s.readDB.Conn(acquire)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		tx, err := conn.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var count int
		if err := tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM requests`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(t.Context(), `DELETE FROM requests`); err == nil || !strings.Contains(err.Error(), "readonly") {
			t.Fatalf("read pool write error = %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := s.FindAccessKeyByHash(ctx, hash); err != nil {
		t.Fatalf("authentication blocked: %v", err)
	}
	if err := s.InsertRequest(ctx, RequestRecord{ID: "record", KeyID: "key", FinishedAt: time.Now(), Result: "completed"}); err != nil {
		t.Fatalf("write blocked by readers: %v", err)
	}
	queued, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer stop()
	if _, err := s.GetRequest(queued, "record"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool wait=%v", err)
	}
}

func TestRequestInsertRespectsConnectionPoolDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	cipher, err := credentialcipher.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "deadline.db"), cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err = s.InsertRequest(ctx, RequestRecord{ID: "blocked", KeyID: "key", Result: "completed"})
	conn.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool cancellation=%v", err)
	}
	if _, err := s.GetRequest(t.Context(), "blocked"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unexpected insert: %v", err)
	}
}
