package execution_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/execution"
	"github.com/deyna256/clan/internal/storage"
)

func TestAccountingLastSelectedAccountIncludesPreparationFailure(t *testing.T) {
	var f fixture
	f = newFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("ChatGPT-Account-Id") != "one" {
			t.Error("disabled account was dispatched")
		}
		// The candidate snapshot still contains two, but registration will reject it.
		if err := f.executor.DisableAccount(t.Context(), "two"); err != nil {
			return nil, err
		}
		return response(429, `{}`), nil
	}))
	f.addAccount(t, "two")

	result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "retry-preparation"}, f.request)
	defer result.Close()
	if err == nil {
		t.Fatal("expected failure")
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	row, err := f.store.GetRequest(t.Context(), "retry-preparation")
	if err != nil || row.AccountID != "two" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}

func TestAccountingOnceAfterDeliveryWithGatewayStart(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, completed), nil }))
	started := time.Now().Add(-3 * time.Second)

	result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "once", Started: started}, f.request)
	defer result.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetRequest(t.Context(), "once"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("saved before delivery: %v", err)
	}
	result.ResponseStarted()
	beforeClose := time.Now()
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	afterClose := time.Now()
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	row, err := f.store.GetRequest(t.Context(), "once")
	if err != nil || !row.ResponseStarted || row.Result != "completed" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	if row.Duration < beforeClose.Sub(started).Truncate(time.Millisecond) || row.Duration > afterClose.Sub(started).Truncate(time.Millisecond) {
		t.Fatalf("duration=%v, want elapsed time from gateway start through Close", row.Duration)
	}
	if row.FinishedAt.Before(beforeClose.Truncate(time.Millisecond)) || row.FinishedAt.After(afterClose.Truncate(time.Millisecond)) {
		t.Fatalf("finished_at=%v, want time during Close [%v, %v]", row.FinishedAt, beforeClose, afterClose)
	}
	if strings.Count(f.logs.String(), `"msg":"request finished"`) != 1 || strings.Contains(f.logs.String(), "accounting failed") {
		t.Fatalf("duplicate finalization: %s", f.logs.String())
	}
}
