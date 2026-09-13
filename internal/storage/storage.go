// Package storage persists CLAN accounts and client access keys in SQLite.
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

const schemaVersion = 1

var (
	// ErrNotFound means the requested account or access key does not exist.
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

// Store owns one SQLite connection and the cipher used for account credentials.
// Construct it with [Open] and close it when the process no longer needs it.
type Store struct {
	db     *sql.DB
	cipher *credentialcipher.Cipher
}

// Open opens path, initializes schema version 1 when needed and authenticates
// all existing account credential bundles before returning a writable store.
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
	if err := store.initialize(ctx); err != nil {
		return nil, closeAfterOpenError(db, err)
	}
	if _, err := store.ListAccounts(ctx); err != nil {
		return nil, closeAfterOpenError(db, fmt.Errorf("storage: verifying account credentials: %w", err))
	}
	return store, nil
}

// Close releases the store's SQLite connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initialize(ctx context.Context) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: beginning schema transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("storage: rolling back schema transaction: %w", rollbackErr))
		}
	}()

	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("storage: reading schema version: %w", err)
	}
	switch version {
	case 0:
		conflict, err := schemaConflict(ctx, tx)
		if err != nil {
			return err
		}
		if conflict {
			return errors.New("storage: conflicting schema")
		}
		if err := createSchema(ctx, tx); err != nil {
			return err
		}
	case schemaVersion:
		if err := checkSchemaTables(ctx, tx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("storage: unsupported schema version %d", version)
	}
	if version == 0 {
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
			return fmt.Errorf("storage: setting schema version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: committing schema transaction: %w", err)
	}
	return nil
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

func schemaConflict(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name NOT GLOB 'sqlite_*'`,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("storage: checking unversioned schema: %w", err)
	}
	return count != 0, nil
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

func checkSchemaTables(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"accounts", "access_keys"} {
		var count int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&count); err != nil {
			return fmt.Errorf("storage: checking schema table: %w", err)
		}
		if count != 1 {
			return errors.New("storage: conflicting schema")
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
