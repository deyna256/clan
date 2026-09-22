package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var migrations embed.FS

func (s *Store) migrate(ctx context.Context) error {
	version, err := s.checkSchema(ctx)
	if err != nil {
		return err
	}
	if version != 0 {
		if _, err := s.ListAccounts(ctx); err != nil {
			return fmt.Errorf("storage: verifying account credentials: %w", err)
		}
	}
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	baseline := goose.NewGoMigration(1, &goose.GoFunc{RunTx: adoptSchema}, nil)
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.db, files,
		goose.WithGoMigrations(baseline), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("storage: configuring migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("storage: migrating: %w", err)
	}
	_, err = s.checkSchema(ctx)
	return err
}

func adoptSchema(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		if err := createSchema(ctx, tx); err != nil {
			return err
		}
	case 1:
		return nil // Preflight already validated the legacy schema.
	default:
		return fmt.Errorf("storage: unsupported baseline version %d", version)
	}
	_, err := tx.ExecContext(ctx, `PRAGMA user_version = 1`)
	return err
}

// Preflight must not initialize Goose: even GetDBVersion can create its table.
func (s *Store) checkSchema(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("storage: reading schema version: %w", err)
	}
	if version < 0 || version > schemaVersion {
		return 0, fmt.Errorf("storage: unsupported schema version %d", version)
	}
	objects, err := s.schemaObjects(ctx)
	if err != nil {
		return 0, err
	}
	conflict := errors.New("storage: conflicting schema")
	if version == 0 {
		for name := range objects {
			if name != "goose_db_version" {
				return 0, conflict
			}
		}
	} else {
		if objects["accounts"] != "table" || objects["access_keys"] != "table" {
			return 0, conflict
		}
		if version == 1 && objects["requests"] != "" {
			return 0, conflict
		}
		if version == 2 && objects["requests"] != "table" {
			return 0, conflict
		}
	}
	if objects["goose_db_version"] == "" && version < 2 {
		return version, nil
	}
	if objects["goose_db_version"] != "table" {
		return 0, conflict
	}
	ledger, err := database.NewStore(database.DialectSQLite3, "goose_db_version")
	if err != nil {
		return 0, err
	}
	history, err := ledger.ListMigrations(ctx, s.db)
	if err != nil {
		return 0, fmt.Errorf("storage: reading migration history: %w", err)
	}
	seen := make(map[int64]bool)
	var highest int64
	for _, entry := range history {
		if !entry.IsApplied || entry.Version < 0 || entry.Version > schemaVersion || seen[entry.Version] {
			return 0, conflict
		}
		seen[entry.Version] = true
		highest = max(highest, entry.Version)
	}
	for v := int64(0); v <= highest; v++ {
		if !seen[v] {
			return 0, conflict
		}
	}
	if highest != int64(version) && !(version == 1 && highest == 0) {
		return 0, conflict
	}
	return version, nil
}

func (s *Store) schemaObjects(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, type FROM sqlite_master WHERE name NOT GLOB 'sqlite_*'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := make(map[string]string)
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		objects[name] = kind
	}
	return objects, rows.Err()
}
