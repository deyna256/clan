package app

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/storage"
)

func TestRetentionRunsHourlyAndStops(t *testing.T) {
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	synctest.Test(t, func(t *testing.T) {
		cipher, err := credentialcipher.New(make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		store, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "retention.db"), cipher)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		now := time.Now()
		for i := range 1001 {
			if err := store.InsertRequest(t.Context(), storage.RequestRecord{ID: fmt.Sprint(i), KeyID: "key", Result: "canceled", FinishedAt: now.Add(-48 * time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.InsertRequest(t.Context(), storage.RequestRecord{ID: "fresh", KeyID: "key", Result: "completed", FinishedAt: now}); err != nil {
			t.Fatal(err)
		}
		a := &application{store: store}
		a.startRetention(24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
		defer a.stopRetention()
		synctest.Wait()

		time.Sleep(time.Hour - time.Nanosecond)
		synctest.Wait()
		if _, err := store.GetRequest(t.Context(), "0"); err != nil {
			t.Fatalf("record removed before the first hourly run: %v", err)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		rows, err := store.ListRequests(t.Context(), storage.RequestFilter{}, nil, 100)
		if err != nil || len(rows) != 1 || rows[0].ID != "fresh" {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		a.stopRetention()
		if err := store.InsertRequest(t.Context(), storage.RequestRecord{ID: "after-stop", KeyID: "key", Result: "completed", FinishedAt: now.Add(-48 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		if _, err := store.GetRequest(t.Context(), "after-stop"); err != nil {
			t.Fatalf("worker ran after stop: %v", err)
		}
	})
}
