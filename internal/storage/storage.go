// Package storage persists CLAN accounts, client access keys and request metadata in SQLite.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/credentialcipher"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const schemaVersion = 2

var (
	// ErrNotFound means the requested record does not exist.
	ErrNotFound = errors.New("storage: record not found")
	// ErrConflict means a value is already in use or a conditional update is stale.
	ErrConflict = errors.New("storage: record conflicts with existing data")
	// ErrInvalid means an operation received a value that cannot be stored.
	ErrInvalid = errors.New("storage: invalid input")
	// ErrCorrupt means a stored record could not be decoded and authenticated.
	ErrCorrupt = errors.New("storage: corrupt record")
)

// AccountRecord combines a persisted account snapshot with its enabled state.
// Reads retain a private revision for conditional credential updates.
type AccountRecord struct {
	Account  account.Account
	Enabled  bool
	revision string
}

// AccessKeyRecord combines a persisted key snapshot with its verification hash
// and concurrency limit.
type AccessKeyRecord struct {
	Key              accesskey.AccessKey
	VerificationHash accesskey.VerificationHash
	ConcurrencyLimit int
}

// Store owns the operational and report connection pools and the credential cipher.
// Construct it with [Open] and close it when the process no longer needs it.
type Store struct {
	db     *sql.DB
	readDB *sql.DB
	cipher *credentialcipher.Cipher
}

// Open validates and migrates path, authenticates stored credentials and opens
// a WAL database with separate operational and read-only report pools.
func Open(ctx context.Context, path string, cipher *credentialcipher.Cipher) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: database path is required", ErrInvalid)
	}
	if cipher == nil {
		return nil, fmt.Errorf("%w: credential cipher is required", ErrInvalid)
	}

	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, fmt.Errorf("storage: resolving database path: %w", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: opening database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db, cipher: cipher}
	if err := store.migrate(ctx); err != nil {
		return nil, closeAfterOpenError(db, err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		return nil, closeAfterOpenError(db, fmt.Errorf("storage: enabling WAL: %w", err))
	}
	if mode != "wal" {
		return nil, closeAfterOpenError(db, errors.New("storage: WAL is required"))
	}
	reader, err := sql.Open("sqlite", dsn+"&mode=ro")
	if err != nil {
		return nil, closeAfterOpenError(db, err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	store.readDB = reader
	if err := reader.PingContext(ctx); err != nil {
		return nil, errors.Join(err, store.Close())
	}
	return store, nil
}

// Close releases both SQLite connection pools.
func (s *Store) Close() error {
	return errors.Join(s.readDB.Close(), s.db.Close())
}

func validateAccountID(id account.ID) error {
	if strings.TrimSpace(string(id)) == "" {
		return fmt.Errorf("%w: account id is required", ErrInvalid)
	}
	return nil
}

func validateAccessKeyID(id accesskey.ID) error {
	if strings.TrimSpace(string(id)) == "" {
		return fmt.Errorf("%w: access key id is required", ErrInvalid)
	}
	return nil
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: checking updated record: %w", err)
	}
	if affected != 0 {
		return nil
	}
	return ErrNotFound
}

func createSchema(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY NOT NULL,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
			credentials BLOB NOT NULL
		)`,
		`CREATE TABLE access_keys (
			id TEXT PRIMARY KEY NOT NULL,
			name TEXT NOT NULL,
			verification_hash BLOB NOT NULL UNIQUE,
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
			concurrency_limit INTEGER NOT NULL CHECK (concurrency_limit >= -1)
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("storage: creating schema: %w", err)
		}
	}
	return nil
}

func classifyWriteError(operation string, err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_UNIQUE:
			return fmt.Errorf("%w: %s", ErrConflict, operation)
		}
	}
	return fmt.Errorf("storage: %s: %w", operation, err)
}

func sqliteDSN(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return (&url.URL{
		Scheme:   "file",
		Path:     absolute,
		RawQuery: "_pragma=busy_timeout%3d5000",
	}).String(), nil
}

func closeAfterOpenError(db *sql.DB, err error) error {
	if closeErr := db.Close(); closeErr != nil {
		return errors.Join(err, fmt.Errorf("storage: closing database: %w", closeErr))
	}
	return err
}
