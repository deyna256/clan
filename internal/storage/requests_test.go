package storage_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/storage"
	"github.com/deyna256/clan/internal/usage"
)

func TestRequestRecordsAndPagination(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "usage.db")
	s := openStore(t, path, newCipher(t))
	stamp := time.Date(2026, 9, 1, 0, 0, 0, 124000000, time.UTC)
	for _, r := range []storage.RequestRecord{
		{ID: "a", KeyID: "key", FinishedAt: stamp, Result: "invalid_request"},
		{ID: "b", KeyID: "key", FinishedAt: stamp, Result: "completed", AccountID: "account", Model: "model",
			Usage: usage.Snapshot{Input: usage.Counter{Known: true}, Output: usage.Counter{Known: true, Tokens: 7}}},
		{ID: "c", KeyID: "other", FinishedAt: stamp.Add(-time.Millisecond), Result: "canceled"},
		{ID: "d", KeyID: "other", FinishedAt: stamp.Add(time.Millisecond), Result: "canceled"},
	} {
		if err := s.InsertRequest(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}

	unknown, err := s.GetRequest(t.Context(), "a")
	if err != nil || unknown.Model != "" || unknown.AccountID != "" || unknown.Usage.Input.Known {
		t.Fatalf("unknown=%+v err=%v", unknown, err)
	}
	var nulls bool
	if err := openRaw(t, path).QueryRowContext(t.Context(),
		`SELECT account_id IS NULL AND model IS NULL FROM requests WHERE id = ?`, "a").Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if !nulls {
		t.Fatal("unknown account and model must be stored as SQL NULL")
	}
	if err := s.InsertRequest(t.Context(), unknown); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.GetRequest(t.Context(), "absent"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	from, to := stamp, stamp.Add(time.Millisecond)
	rows, err := s.ListRequests(t.Context(), storage.RequestFilter{From: &from, To: &to}, nil, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != "b" {
		t.Fatalf("first page=%+v err=%v", rows, err)
	}
	after := &storage.RequestPosition{FinishedAt: rows[0].FinishedAt.UnixMilli(), ID: rows[0].ID}
	rows, err = s.ListRequests(t.Context(), storage.RequestFilter{From: &from, To: &to}, after, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != "a" {
		t.Fatalf("second page=%+v err=%v", rows, err)
	}
	fractionalFrom, fractionalTo := stamp.Add(-500*time.Microsecond), stamp.Add(500*time.Microsecond)
	for _, tt := range []struct {
		name   string
		filter storage.RequestFilter
		want   []string
	}{
		{name: "fractional bounds", filter: storage.RequestFilter{From: &fractionalFrom, To: &fractionalTo}, want: []string{"b", "a"}},
		{name: "key", filter: storage.RequestFilter{KeyID: "key"}, want: []string{"b", "a"}},
		{name: "account", filter: storage.RequestFilter{AccountID: "account"}, want: []string{"b"}},
		{name: "model", filter: storage.RequestFilter{Model: "model"}, want: []string{"b"}},
		{name: "result", filter: storage.RequestFilter{Result: "completed"}, want: []string{"b"}},
		{name: "filters combine with AND", filter: storage.RequestFilter{AccountID: "account", Model: "missing"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := s.ListRequests(t.Context(), tt.filter, nil, 10)

			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, row := range rows {
				ids = append(ids, row.ID)
			}
			if !slices.Equal(ids, tt.want) {
				t.Fatalf("ids=%v, want %v", ids, tt.want)
			}
		})
	}
}

func TestUsageGroupsAndNullableSums(t *testing.T) {
	requireStorage(t)
	s := openStore(t, filepath.Join(t.TempDir(), "summaries.db"), newCipher(t))
	stamp := time.Date(2026, 9, 1, 23, 59, 59, 999000000, time.UTC)
	for _, row := range []storage.RequestRecord{
		{ID: "unknown", KeyID: "first", FinishedAt: stamp, Result: "invalid_request"},
		{ID: "partial", KeyID: "first", FinishedAt: stamp, AccountID: "account", Model: "model", Result: "completed",
			Usage: usage.Snapshot{Input: usage.Counter{Known: true}, Output: usage.Counter{Known: true, Tokens: 7}}},
		{ID: "known", KeyID: "second", FinishedAt: stamp.Add(time.Millisecond), AccountID: "account", Model: "model", Result: "incomplete",
			Usage: usage.Snapshot{Input: usage.Counter{Known: true, Tokens: 5}, Output: usage.Counter{Known: true, Tokens: 3}, Total: usage.Counter{Known: true, Tokens: 8}}},
	} {
		if err := s.InsertRequest(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		name   string
		groups []string
		filter storage.RequestFilter
		want   []storage.UsageSummary
	}{
		{name: "key", groups: []string{"key"}, want: []storage.UsageSummary{
			{KeyID: "first", Requests: 2, Results: map[string]int64{"invalid_request": 1, "completed": 1}, UnknownUsage: 2,
				Usage: usage.Snapshot{Input: usage.Counter{Known: true}, Output: usage.Counter{Known: true, Tokens: 7}}},
			{KeyID: "second", Requests: 1, Results: map[string]int64{"incomplete": 1},
				Usage: usage.Snapshot{Input: usage.Counter{Known: true, Tokens: 5}, Output: usage.Counter{Known: true, Tokens: 3}, Total: usage.Counter{Known: true, Tokens: 8}}},
		}},
		{name: "account", groups: []string{"account"}, want: []storage.UsageSummary{
			{Requests: 1, Results: map[string]int64{"invalid_request": 1}, UnknownUsage: 1},
			{AccountID: "account", Requests: 2, Results: map[string]int64{"completed": 1, "incomplete": 1}, UnknownUsage: 1,
				Usage: usage.Snapshot{Input: usage.Counter{Known: true, Tokens: 5}, Output: usage.Counter{Known: true, Tokens: 10}, Total: usage.Counter{Known: true, Tokens: 8}}},
		}},
		{name: "model", groups: []string{"model"}, want: []storage.UsageSummary{
			{Requests: 1, Results: map[string]int64{"invalid_request": 1}, UnknownUsage: 1},
			{Model: "model", Requests: 2, Results: map[string]int64{"completed": 1, "incomplete": 1}, UnknownUsage: 1,
				Usage: usage.Snapshot{Input: usage.Counter{Known: true, Tokens: 5}, Output: usage.Counter{Known: true, Tokens: 10}, Total: usage.Counter{Known: true, Tokens: 8}}},
		}},
		{name: "UTC day", groups: []string{"day"}, want: []storage.UsageSummary{
			{Day: "2026-09-01", Requests: 2, Results: map[string]int64{"invalid_request": 1, "completed": 1}, UnknownUsage: 2,
				Usage: usage.Snapshot{Input: usage.Counter{Known: true}, Output: usage.Counter{Known: true, Tokens: 7}}},
			{Day: "2026-09-02", Requests: 1, Results: map[string]int64{"incomplete": 1},
				Usage: usage.Snapshot{Input: usage.Counter{Known: true, Tokens: 5}, Output: usage.Counter{Known: true, Tokens: 3}, Total: usage.Counter{Known: true, Tokens: 8}}},
		}},
		{name: "combined", groups: []string{"key", "account", "model", "day"}, want: []storage.UsageSummary{
			{KeyID: "first", Day: "2026-09-01", Requests: 1, Results: map[string]int64{"invalid_request": 1}, UnknownUsage: 1},
			{KeyID: "first", AccountID: "account", Model: "model", Day: "2026-09-01", Requests: 1, Results: map[string]int64{"completed": 1}, UnknownUsage: 1,
				Usage: usage.Snapshot{Input: usage.Counter{Known: true}, Output: usage.Counter{Known: true, Tokens: 7}}},
			{KeyID: "second", AccountID: "account", Model: "model", Day: "2026-09-02", Requests: 1, Results: map[string]int64{"incomplete": 1},
				Usage: usage.Snapshot{Input: usage.Counter{Known: true, Tokens: 5}, Output: usage.Counter{Known: true, Tokens: 3}, Total: usage.Counter{Known: true, Tokens: 8}}},
		}},
		{name: "no matches", groups: []string{"key"}, filter: storage.RequestFilter{KeyID: "missing"}, want: []storage.UsageSummary{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.SummarizeUsage(t.Context(), tt.filter, tt.groups)

			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("summary=%+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestUsageSumsWithinAndAcrossResults(t *testing.T) {
	requireStorage(t)
	s := openStore(t, filepath.Join(t.TempDir(), "sums.db"), newCipher(t))
	for _, row := range []storage.RequestRecord{
		{ID: "a", KeyID: "key", Result: "completed", Usage: usage.Snapshot{
			Input: usage.Counter{Known: true, Tokens: 2}, Output: usage.Counter{Known: true, Tokens: 2}, Total: usage.Counter{Known: true, Tokens: 4}}},
		{ID: "b", KeyID: "key", Result: "completed", Usage: usage.Snapshot{
			Input: usage.Counter{Known: true, Tokens: 3}, Output: usage.Counter{Known: true, Tokens: 3}, Total: usage.Counter{Known: true, Tokens: 6}}},
		{ID: "c", KeyID: "key", Result: "incomplete", Usage: usage.Snapshot{
			Output: usage.Counter{Known: true, Tokens: 4}, Total: usage.Counter{Known: true, Tokens: 4}}},
		{ID: "d", KeyID: "key", Result: "incomplete", Usage: usage.Snapshot{
			Input: usage.Counter{Known: true, Tokens: 1}, Total: usage.Counter{Known: true, Tokens: 1}}},
	} {
		if err := s.InsertRequest(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	want := []storage.UsageSummary{{KeyID: "key", Requests: 4, UnknownUsage: 2,
		Results: map[string]int64{"completed": 2, "incomplete": 2},
		Usage: usage.Snapshot{Input: usage.Counter{Known: true, Tokens: 6},
			Output: usage.Counter{Known: true, Tokens: 9}, Total: usage.Counter{Known: true, Tokens: 15}}}}

	got, err := s.SummarizeUsage(t.Context(), storage.RequestFilter{}, []string{"key"})

	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary=%+v, want %+v", got, want)
	}
}

func TestRetentionDeletesBatchesAndPreservesBoundary(t *testing.T) {
	requireStorage(t)
	s := openStore(t, filepath.Join(t.TempDir(), "retention.db"), newCipher(t))
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range 1002 {
		stamp := cutoff.Add(-time.Millisecond)
		if i == 1001 {
			stamp = cutoff
		}
		if err := s.InsertRequest(t.Context(), storage.RequestRecord{ID: fmt.Sprint(i), KeyID: "deleted-key", FinishedAt: stamp, Result: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int64{1000, 1, 0} {
		got, err := s.DeleteRequestsBefore(t.Context(), cutoff)
		if err != nil || got != want {
			t.Fatalf("deleted=%d want=%d err=%v", got, want, err)
		}
	}
	if _, err := s.GetRequest(t.Context(), "1001"); err != nil {
		t.Fatal(err)
	}
}
