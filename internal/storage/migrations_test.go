package storage_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3/database"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/storage"
)

func TestMigrationAuthenticatesLegacyBeforeMutation(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "credentials.db")
	db := openLegacyDB(t, path)
	insertLegacyAccount(t, db, newCipher(t), newAccount(t, "account", "Keep"))
	wrong, err := credentialcipher.New(bytes.Repeat([]byte{99}, 32))
	if err != nil {
		t.Fatal(err)
	}

	if opened, err := storage.Open(t.Context(), path, wrong); err == nil {
		opened.Close()
		t.Fatal("wrong key accepted")
	}
	var version, count int
	var mode string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('requests','goose_db_version')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if version != 1 || mode != "delete" || count != 0 {
		t.Fatalf("rejection mutated schema: version=%d mode=%s objects=%d", version, mode, count)
	}
}

func TestFailedMigrationCanResume(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "retry.db")
	db := openLegacyDB(t, path)
	if _, err := db.Exec(`CREATE INDEX requests_finished_at ON accounts(name)`); err != nil {
		t.Fatal(err)
	}

	if opened, err := storage.Open(t.Context(), path, newCipher(t)); err == nil {
		opened.Close()
		t.Fatal("migration should fail on conflicting index")
	}
	var version, latest int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if version != 1 || latest != 1 {
		t.Fatalf("failed migration committed: schema=%d goose=%d", version, latest)
	}
	if _, err := db.Exec(`DROP INDEX requests_finished_at`); err != nil {
		t.Fatal(err)
	}
	upgraded := openStore(t, path, newCipher(t))
	if err := upgraded.InsertRequest(t.Context(), storage.RequestRecord{ID: "ok", KeyID: "key", Result: "completed"}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationAdoptsLegacyCredentialsAndInterruptedHistory(t *testing.T) {
	requireStorage(t)
	for _, tt := range []struct {
		name    string
		history []int64
	}{
		{name: "absent"},
		{name: "initialized", history: []int64{0}},
		{name: "baseline", history: []int64{0, 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			cipher := newCipher(t)
			db := openLegacyDB(t, path)
			a := newAccount(t, "account", "Keep")
			insertLegacyAccount(t, db, cipher, a)
			key, hash := newKey(t, "key", "Keep", true, fixtureKeyOne)
			if _, err := db.ExecContext(t.Context(), `INSERT INTO access_keys
				(id, name, verification_hash, enabled, concurrency_limit) VALUES ('key', 'Keep', ?, 1, 3)`, hash[:]); err != nil {
				t.Fatal(err)
			}
			if tt.history != nil {
				ledger, err := database.NewStore(database.DialectSQLite3, "goose_db_version")
				if err != nil {
					t.Fatal(err)
				}
				if err := ledger.CreateVersionTable(t.Context(), db); err != nil {
					t.Fatal(err)
				}
				for _, version := range tt.history {
					if err := ledger.Insert(t.Context(), db, database.InsertRequest{Version: version}); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := readAccountBlob(t, path, "account")

			upgraded, err := storage.Open(t.Context(), path, cipher)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := upgraded.Close(); err != nil {
					t.Error(err)
				}
			})
			got, err := upgraded.GetAccount(t.Context(), "account")
			if err != nil {
				t.Fatal(err)
			}
			assertAccountRecord(t, got, a, true)
			found, err := upgraded.FindAccessKeyByHash(t.Context(), hash)
			if err != nil {
				t.Fatal(err)
			}
			assertKeyRecord(t, found, key, hash, 3)
			if !bytes.Equal(before, readAccountBlob(t, path, "account")) {
				t.Fatal("encrypted credentials changed")
			}
			if err := upgraded.Close(); err != nil {
				t.Fatal(err)
			}
			var version int
			var mode string
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
				t.Fatal(err)
			}
			if version != 2 || mode != "wal" {
				t.Fatalf("version=%d mode=%s", version, mode)
			}
			reopened := openStore(t, path, cipher)
			if _, err := reopened.GetAccount(t.Context(), "account"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMigrationPreflightRejectionDoesNotMutate(t *testing.T) {
	requireStorage(t)
	for _, tt := range []struct{ name, change string }{
		{name: "future history", change: `INSERT INTO goose_db_version(version_id,is_applied) VALUES(99,1)`},
		{name: "missing baseline", change: `DELETE FROM goose_db_version WHERE version_id=1`},
		{name: "missing history", change: `DROP TABLE goose_db_version`},
		{name: "empty history", change: `DELETE FROM goose_db_version`},
		{name: "missing table", change: `DROP TABLE requests`},
		{name: "mismatched version", change: `PRAGMA user_version=1`},
		{name: "duplicate version", change: `INSERT INTO goose_db_version(version_id,is_applied) VALUES(2,1)`},
		{name: "unapplied version", change: `UPDATE goose_db_version SET is_applied=0 WHERE version_id=2`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reject.db")
			s := openStore(t, path, newCipher(t))
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			db := openRaw(t, path)
			if _, err := db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tt.change); err != nil {
				t.Fatal(err)
			}
			before := readMigrationState(t, db)

			if opened, err := storage.Open(t.Context(), path, newCipher(t)); err == nil {
				opened.Close()
				t.Fatal("accepted invalid schema")
			}
			after := readMigrationState(t, db)
			if after != before {
				t.Fatalf("rejection changed database: before=%+v after=%+v", before, after)
			}
		})
	}
}

// Keep the released v1 schema independent of current migration code.
func openLegacyDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db := openRaw(t, path)
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE accounts (
			id TEXT PRIMARY KEY NOT NULL,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
			credentials BLOB NOT NULL
		);
		CREATE TABLE access_keys (
			id TEXT PRIMARY KEY NOT NULL,
			name TEXT NOT NULL,
			verification_hash BLOB NOT NULL UNIQUE,
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
			concurrency_limit INTEGER NOT NULL CHECK (concurrency_limit >= -1)
		);
		PRAGMA user_version=1;
		PRAGMA journal_mode=DELETE;`); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertLegacyAccount(t *testing.T, db *sql.DB, cipher *credentialcipher.Cipher, a account.Account) {
	t.Helper()
	encoded, err := json.Marshal(a.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt(a.Identity().ID, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO accounts (id, name, enabled, credentials)
		VALUES (?, ?, 1, ?)`, a.Identity().ID, a.Identity().Name, encrypted); err != nil {
		t.Fatal(err)
	}
}

type migrationState struct {
	schemaVersion, userVersion int
	journalMode, history       string
}

func readMigrationState(t *testing.T, db *sql.DB) migrationState {
	t.Helper()
	var state migrationState
	for _, query := range []struct {
		sql  string
		dest any
	}{
		{"PRAGMA schema_version", &state.schemaVersion},
		{"PRAGMA user_version", &state.userVersion},
		{"PRAGMA journal_mode", &state.journalMode},
	} {
		if err := db.QueryRowContext(t.Context(), query.sql).Scan(query.dest); err != nil {
			t.Fatal(err)
		}
	}
	var exists bool
	if err := db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM sqlite_master
		WHERE type='table' AND name='goose_db_version')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		if err := db.QueryRowContext(t.Context(), `SELECT json_group_array(json_array(id, version_id, is_applied, tstamp))
			FROM (SELECT * FROM goose_db_version ORDER BY id)`).Scan(&state.history); err != nil {
			t.Fatal(err)
		}
	}
	return state
}
