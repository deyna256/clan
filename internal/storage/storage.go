// Package storage persists absolute budget-window snapshots in SQLite or PostgreSQL.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/budget"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// Supported database backends.
const (
	SQLite     = "sqlite"
	PostgreSQL = "postgres"
)

// Config selects one backend. Empty Backend selects SQLite. DSN is a required
// file path for SQLite or a PostgreSQL connection string. Environment loading
// and selection of a default file path belong to the application.
type Config struct {
	Backend string
	DSN     string
}

// Store owns its database connections. Construct it with [Open], then call
// [Store.Close] when finished. Calls may overlap, but the caller must order
// saves for each key by waiting for the previous call before starting the next.
type Store struct {
	db *sql.DB
}

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects to the selected database and applies embedded migrations.
// Failures never select another backend. Only one application instance may use
// an installation; callers must not run overlapping opens during migration.
func Open(ctx context.Context, config Config) (*Store, error) {
	if strings.TrimSpace(config.DSN) == "" {
		return nil, errors.New("storage: database target is required")
	}
	var driver string
	var dialect goose.Dialect
	switch config.Backend {
	case "", SQLite:
		if strings.ContainsRune(config.DSN, 0) {
			return nil, errors.New("storage: invalid SQLite path")
		}
		driver, dialect = "sqlite", goose.DialectSQLite3
		path, err := filepath.Abs(config.DSN)
		if err != nil {
			return nil, errors.New("storage: invalid SQLite path")
		}
		target := url.URL{Scheme: "file", Path: path}
		// Driver pragmas run on every connection, including replacements.
		target.RawQuery = url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)"}}.Encode()
		config.DSN = target.String()
	case PostgreSQL:
		driver, dialect = "pgx", goose.DialectPostgres
	default:
		return nil, errors.New("storage: unsupported database backend")
	}
	db, err := sql.Open(driver, config.DSN)
	if err != nil {
		return nil, databaseError(ctx, "open database", err)
	}
	if dialect == goose.DialectSQLite3 {
		// ponytail: serialize SQLite I/O; add a read pool if contention warrants it.
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // Preserve the connection failure; no store escapes.
		return nil, databaseError(ctx, "connect to database", err)
	}
	files, err := fs.Sub(migrations, "migrations")
	if err == nil {
		var provider *goose.Provider
		provider, err = goose.NewProvider(dialect, db, files, goose.WithDisableGlobalRegistry(true))
		if err == nil {
			_, err = provider.Up(ctx)
		}
	}
	if err != nil {
		_ = db.Close() // Preserve the migration failure; no store escapes.
		return nil, databaseError(ctx, "migrate database", err)
	}
	return &Store{db: db}, nil
}

// Load returns the saved snapshot; found distinguishes a saved zero state from
// absence. Read or validation failures return no state and a non-nil error.
func (s *Store) Load(ctx context.Context, id accesskey.ID) (state budget.State, found bool, err error) {
	if err := validateID(id); err != nil {
		return budget.State{}, false, err
	}
	var five, seven sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT five_hours_opened_at, five_hours_used,
		seven_days_opened_at, seven_days_used FROM budget_snapshots WHERE access_key_id = $1`, string(id)).
		Scan(&five, &state.FiveHours.Used, &seven, &state.SevenDays.Used)
	if errors.Is(err, sql.ErrNoRows) {
		return budget.State{}, false, nil
	}
	if err != nil {
		return budget.State{}, false, databaseError(ctx, "load snapshot", err)
	}
	for _, field := range []struct {
		text   sql.NullString
		window *budget.Window
	}{{five, &state.FiveHours}, {seven, &state.SevenDays}} {
		if field.text.Valid {
			field.window.OpenedAt, err = time.Parse(time.RFC3339Nano, field.text.String)
			if err != nil || field.window.OpenedAt.IsZero() || field.window.OpenedAt.UTC().Format(time.RFC3339Nano) != field.text.String {
				return budget.State{}, false, errors.New("storage: invalid saved opening time")
			}
		}
		if _, err := encodeWindow(*field.window); err != nil {
			return budget.State{}, false, err
		}
	}
	return state, true, nil
}

// Save atomically replaces both windows without changing key settings or
// recalculating openings. Repeating a snapshot is safe, including after an
// unconfirmed save. Errors do not establish whether a database write committed.
func (s *Store) Save(ctx context.Context, id accesskey.ID, state budget.State) error {
	if err := validateID(id); err != nil {
		return err
	}
	five, err := encodeWindow(state.FiveHours)
	if err != nil {
		return err
	}
	seven, err := encodeWindow(state.SevenDays)
	if err != nil {
		return err
	}
	// A canceled upsert must not commit later, after the caller starts a new save.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return databaseError(ctx, "begin snapshot save", err)
	}
	defer tx.Rollback() // Roll back unless committed; preserve the operation error.
	_, err = tx.ExecContext(ctx, `INSERT INTO budget_snapshots
		(access_key_id, five_hours_opened_at, five_hours_used, seven_days_opened_at, seven_days_used)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (access_key_id) DO UPDATE SET
		five_hours_opened_at = excluded.five_hours_opened_at, five_hours_used = excluded.five_hours_used,
		seven_days_opened_at = excluded.seven_days_opened_at, seven_days_used = excluded.seven_days_used`,
		string(id), five, state.FiveHours.Used, seven, state.SevenDays.Used)
	if err != nil {
		return databaseError(ctx, "save snapshot", err)
	}
	return databaseError(ctx, "commit snapshot", tx.Commit())
}

// Close releases the database connections after in-flight operations finish.
func (s *Store) Close() error {
	return databaseError(context.Background(), "close database", s.db.Close())
}

func validateID(id accesskey.ID) error {
	if strings.TrimSpace(string(id)) == "" || !utf8.ValidString(string(id)) || strings.ContainsRune(string(id), 0) {
		return errors.New("storage: access-key ID must be nonblank UTF-8 without NUL")
	}
	return nil
}

func encodeWindow(window budget.Window) (sql.NullString, error) {
	if window.Used < 0 || (window.OpenedAt.IsZero() && window.Used != 0) {
		return sql.NullString{}, errors.New("storage: invalid budget usage")
	}
	if window.OpenedAt.IsZero() {
		return sql.NullString{}, nil
	}
	encoded, err := window.OpenedAt.UTC().MarshalText()
	if err != nil {
		return sql.NullString{}, errors.New("storage: opening time is outside years 0000–9999 UTC")
	}
	return sql.NullString{String: string(encoded), Valid: true}, nil
}

func databaseError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	// Raw driver errors may contain DSNs, passwords or server-provided values.
	if ctx.Err() != nil {
		return fmt.Errorf("storage: %s: %w", operation, ctx.Err())
	}
	return fmt.Errorf("storage: %s failed", operation)
}
