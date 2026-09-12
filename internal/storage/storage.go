// Package storage persists absolute budget-window snapshots in SQLite.
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
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// Store owns its database connections. Construct it with [Open], then call
// [Store.Close] when finished. Calls may overlap, but the caller must order
// saves for each key by waiting for the previous call before starting the next.
type Store struct {
	db *sql.DB
}

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens a SQLite file and applies embedded migrations. The application
// supplies the path and must not run overlapping opens during migration.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return nil, errors.New("storage: invalid SQLite path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("storage: invalid SQLite path")
	}
	target := url.URL{Scheme: "file", Path: path}
	// Driver pragmas run on every connection, including replacements.
	target.RawQuery = url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)"}}.Encode()
	db, err := sql.Open("sqlite", target.String())
	if err != nil {
		return nil, databaseError(ctx, "open database", err)
	}
	// ponytail: serialize SQLite I/O; add a read pool if contention warrants it.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // Preserve the connection failure; no store escapes.
		return nil, databaseError(ctx, "connect to database", err)
	}
	files, err := fs.Sub(migrations, "migrations")
	if err == nil {
		var provider *goose.Provider
		provider, err = goose.NewProvider(goose.DialectSQLite3, db, files, goose.WithDisableGlobalRegistry(true))
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
	// Raw driver errors may contain database paths or stored values.
	if ctx.Err() != nil {
		return fmt.Errorf("storage: %s: %w", operation, ctx.Err())
	}
	return fmt.Errorf("storage: %s failed", operation)
}
