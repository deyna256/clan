package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/budget"
	"github.com/deyna256/clan/internal/storage"
)

func TestMissingAndSavedZeroSnapshotsDiffer(t *testing.T) {
	path, _ := database(t)
	store := openStore(t, path)

	got, found, err := store.Load(t.Context(), "missing")
	if err != nil || found || got != (budget.State{}) {
		t.Fatalf("missing snapshot = %+v, %v, %v; want zero, false, nil", got, found, err)
	}

	if err := store.Save(t.Context(), "zero", budget.State{}); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, store, "zero", budget.State{})
}

func TestSnapshotPreservesTimestampsAndWindows(t *testing.T) {
	initial := sampleSnapshot()
	cases := []struct {
		id    accesskey.ID
		state budget.State
	}{
		{id: "expired", state: initial},
		{id: "epoch", state: budget.State{FiveHours: budget.Window{OpenedAt: time.Unix(0, 0), Used: math.MaxInt64}}},
		{id: "future", state: budget.State{SevenDays: budget.Window{OpenedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)}}},
		{id: "year zero", state: budget.State{FiveHours: budget.Window{OpenedAt: time.Date(0, 1, 1, 0, 0, 0, 1, time.UTC)}}},
	}
	for _, tc := range cases {
		t.Run(string(tc.id), func(t *testing.T) {
			path, _ := database(t)
			store := openStore(t, path)

			if err := store.Save(t.Context(), tc.id, tc.state); err != nil {
				t.Fatal(err)
			}
			assertSnapshot(t, store, tc.id, tc.state)
		})
	}
}

func TestSnapshotReplacementIsRepeatable(t *testing.T) {
	initial := sampleSnapshot()
	opening := initial.FiveHours.OpenedAt
	path, _ := database(t)
	store := openStore(t, path)
	if err := store.Save(t.Context(), "retry", initial); err != nil {
		t.Fatal(err)
	}
	next := budget.State{
		FiveHours: budget.Window{OpenedAt: opening.Add(5 * time.Hour), Used: 50},
		SevenDays: budget.Window{OpenedAt: initial.SevenDays.OpenedAt, Used: 950},
	}
	if err := store.Save(t.Context(), "retry", next); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), "retry", next); err != nil {
		t.Fatal(err)
	}

	assertSnapshot(t, store, "retry", next)
}

func TestInvalidSnapshotsPreserveState(t *testing.T) {
	initial := sampleSnapshot()
	opening := initial.FiveHours.OpenedAt
	path, _ := database(t)
	store := openStore(t, path)
	if err := store.Save(t.Context(), "invalid", initial); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []budget.Window{
		{Used: 1},
		{OpenedAt: opening, Used: -1},
		{OpenedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{OpenedAt: time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		for _, bad := range []budget.State{
			{FiveHours: invalid, SevenDays: initial.SevenDays},
			{FiveHours: budget.Window{OpenedAt: opening, Used: 999}, SevenDays: invalid},
		} {
			if err := store.Save(t.Context(), "invalid", bad); err == nil {
				t.Fatalf("Save(%+v) succeeded; want validation failure", bad)
			}
			assertSnapshot(t, store, "invalid", initial)
		}
	}
	for _, id := range []accesskey.ID{"", " \t\n", "bad\x00id", "\xff"} {
		if err := store.Save(t.Context(), id, initial); err == nil {
			t.Errorf("Save accepted invalid ID %q", id)
		}
		if _, found, err := store.Load(t.Context(), id); err == nil || found {
			t.Errorf("Load(%q) found=%v err=%v; want false and error", id, found, err)
		}
	}
}

func TestFailedSavePreservesBothWindows(t *testing.T) {
	initial := sampleSnapshot()
	opening := initial.FiveHours.OpenedAt
	path, raw := database(t)
	store := openStore(t, path)
	if err := store.Save(t.Context(), "atomic", initial); err != nil {
		t.Fatal(err)
	}
	// A database-side rejection exercises the actual upsert's atomicity.
	if _, err := raw.ExecContext(t.Context(), `ALTER TABLE budget_snapshots
		ADD COLUMN reject_update INTEGER NOT NULL DEFAULT 0 CHECK (seven_days_used <> 999)`); err != nil {
		t.Fatal(err)
	}
	next := budget.State{
		FiveHours: budget.Window{OpenedAt: opening.Add(5 * time.Hour), Used: 5},
		SevenDays: budget.Window{OpenedAt: opening, Used: 999},
	}

	if err := store.Save(t.Context(), "atomic", next); err == nil {
		t.Fatal("Save succeeded despite database constraint")
	}

	assertSnapshot(t, store, "atomic", initial)
}

func TestLoadRejectsCorruptSnapshots(t *testing.T) {
	initial := sampleSnapshot()
	path, raw := database(t)
	store := openStore(t, path)
	if err := store.Save(t.Context(), "corrupt", initial); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{"secret-corrupt-timestamp", "0001-01-01T00:00:00Z", "2020-01-01T00:00:00.0000000001Z"} {
		if _, err := raw.ExecContext(t.Context(), `UPDATE budget_snapshots SET seven_days_opened_at = $1 WHERE access_key_id = 'corrupt'`, malformed); err != nil {
			t.Fatal(err)
		}

		got, found, err := store.Load(t.Context(), "corrupt")
		if err == nil || found || got != (budget.State{}) || strings.Contains(err.Error(), malformed) {
			t.Fatalf("corrupt snapshot = %+v, %v, %v; want zero, false, sanitized error", got, found, err)
		}
	}
}

func TestSnapshotKeysRemainDistinct(t *testing.T) {
	initial := sampleSnapshot()
	path, _ := database(t)
	store := openStore(t, path)
	for _, id := range []accesskey.ID{"key", " key "} {
		if err := store.Save(t.Context(), id, initial); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Save(t.Context(), "key", budget.State{}); err != nil {
		t.Fatal(err)
	}

	assertSnapshot(t, store, "key", budget.State{})
	assertSnapshot(t, store, " key ", initial)
	got, found, err := store.Load(t.Context(), "other")
	if err != nil || found || got != (budget.State{}) {
		t.Fatalf("other key = %+v, %v, %v; want zero, false, nil", got, found, err)
	}
}

func TestSnapshotCancellationPreservesState(t *testing.T) {
	initial := sampleSnapshot()
	path, _ := database(t)
	store := openStore(t, path)
	if err := store.Save(t.Context(), "cancel", initial); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := store.Save(ctx, "cancel", budget.State{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save error = %v; want context.Canceled", err)
	}
	if _, found, err := store.Load(ctx, "cancel"); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("Load found=%v err=%v; want false, context.Canceled", found, err)
	}
	assertSnapshot(t, store, "cancel", initial)
	if opened, err := storage.Open(ctx, path); opened != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Open = %v, %v; want nil, context.Canceled", opened, err)
	}
}

func TestReopenPreservesSnapshots(t *testing.T) {
	initial := sampleSnapshot()
	path, _ := database(t)
	store := openStore(t, path)
	if err := store.Save(t.Context(), "reopen", initial); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), "zero", budget.State{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, path)

	assertSnapshot(t, reopened, "reopen", initial)
	assertSnapshot(t, reopened, "zero", budget.State{})
	if _, found, err := store.Load(t.Context(), "reopen"); found || err == nil {
		t.Fatalf("closed Load found=%v err=%v; want false and read error", found, err)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	for _, path := range []string{"", " \t\n"} {
		store, err := storage.Open(t.Context(), path)

		if err == nil || store != nil {
			t.Fatalf("Open(%q) = %v, %v; want nil store and invalid-path error", path, store, err)
		}
	}
}

func TestOpenRejectsNULPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.db")

	store, err := storage.Open(t.Context(), path+"\x00other.db")

	if store != nil {
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err == nil || store != nil {
		t.Fatalf("Open = %v, %v; want nil store and invalid-path error", store, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prefix path stat error = %v; want no database created", err)
	}
}

func TestOpenMigrationFailure(t *testing.T) {
	path, raw := database(t)
	if _, err := raw.ExecContext(t.Context(), `CREATE TABLE budget_snapshots (incompatible TEXT)`); err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(t.Context(), path)

	if err == nil || store != nil {
		t.Fatalf("Open = %v, %v; want nil store and migration error", store, err)
	}
	if _, err := raw.ExecContext(t.Context(), `DROP TABLE budget_snapshots`); err != nil {
		t.Fatal(err)
	}
	openStore(t, path)
}

func sampleSnapshot() budget.State {
	opening := time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 3*60*60))
	return budget.State{
		FiveHours: budget.Window{OpenedAt: opening, Used: 150},
		SevenDays: budget.Window{OpenedAt: opening.Add(-24 * time.Hour), Used: 900},
	}
}

func database(t *testing.T) (string, *sql.DB) {
	t.Helper()
	if testing.Short() {
		t.Skip("database integration test")
	}
	path := filepath.Join(t.TempDir(), "budget ?#%.db")
	target := url.URL{Scheme: "file", Path: path}
	raw, err := sql.Open("sqlite", target.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Error("close test database failed")
		}
	})
	return path, raw
}

func openStore(t *testing.T, path string) *storage.Store {
	t.Helper()
	store, err := storage.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func assertSnapshot(t *testing.T, store *storage.Store, id accesskey.ID, want budget.State) {
	t.Helper()
	got, found, err := store.Load(t.Context(), id)
	if err != nil || !found || got.FiveHours.Used != want.FiveHours.Used || got.SevenDays.Used != want.SevenDays.Used ||
		!got.FiveHours.OpenedAt.Equal(want.FiveHours.OpenedAt) || !got.SevenDays.OpenedAt.Equal(want.SevenDays.OpenedAt) {
		t.Fatalf("Load(%q) = %+v, %v, %v; want %+v, true, nil", id, got, found, err, want)
	}
}
