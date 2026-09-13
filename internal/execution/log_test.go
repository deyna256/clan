package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/deyna256/clan/internal/codex"
)

func TestLogsContainOutcomeAndUsageWithoutContentOrCredentials(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, completed), nil
	}))
	request, err := codex.ParseRequest([]byte(`{"model":"model","input":"private-prompt","max_output_tokens":42}`))
	if err != nil {
		t.Fatal(err)
	}

	generated, err := f.executor.Generate(t.Context(), f.key, "request-123", request)
	if err := errors.Join(err, generated.Close()); err != nil {
		t.Fatal(err)
	}

	logs := f.logs.String()
	for _, secret := range []string{f.key, "access-one", "private-prompt"} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
	entries := logEntries(t, logs)
	if len(entries) != 2 || entries[0]["level"] != "WARN" || entries[0]["msg"] != "max_output_tokens omitted for Codex" {
		t.Fatalf("expected omission warning and final result, got %v", entries)
	}
	result := entries[1]
	for name, want := range map[string]any{"request_id": "request-123", "key_id": "key", "model": "model", "result": "completed",
		"input_tokens": float64(7), "output_tokens": float64(3), "total_tokens": float64(10), "response_started": false} {
		if result[name] != want {
			t.Errorf("%s = %v, want %v", name, result[name], want)
		}
	}
	if _, ok := result["duration_ms"].(float64); !ok {
		t.Error("duration is missing")
	}
}

func TestFailureLogsKeepPartialUsageWithoutInventingZeros(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"usage\":{\"input_tokens\":11}}}\n\n"), nil
	}))

	result, err := f.executor.Generate(t.Context(), f.key, "partial", f.request)
	if closeErr := result.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	if err == nil || !result.Usage.Input.Known || result.Usage.Input.Tokens != 11 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	entries := logEntries(t, f.logs.String())
	last := entries[len(entries)-1]
	if last["input_tokens"] != float64(11) || last["result"] != "invalid_response" {
		t.Fatalf("final log = %v", last)
	}
	if _, exists := last["output_tokens"]; exists {
		t.Error("unknown output tokens logged as known")
	}
	if _, exists := last["total_tokens"]; exists {
		t.Error("unknown total tokens logged as known")
	}
}

func TestUndeliveredTerminalDoesNotLogCompleted(t *testing.T) {
	for _, tt := range []struct {
		name      string
		streaming bool
		canceled  bool
	}{
		{name: "ordinary delivery failure"},
		{name: "stream delivery failure", streaming: true},
		{name: "ordinary cancellation", canceled: true},
		{name: "stream cancellation", streaming: true, canceled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
					return response(http.StatusOK, completed), nil
				}))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var closeResult func() error
				var deliveryFailed func()
				var responseStarted func()
				if tt.streaming {
					stream, err := f.executor.Stream(ctx, f.key, "undelivered", f.request)
					if err != nil {
						t.Fatal(err)
					}
					defer stream.Close()
					if _, err := stream.Next(); err != nil {
						t.Fatal(err)
					}
					closeResult, deliveryFailed = stream.Close, stream.DeliveryFailed
					responseStarted = stream.ResponseStarted
				} else {
					result, err := f.executor.Generate(ctx, f.key, "undelivered", f.request)
					defer result.Close()
					if err != nil {
						t.Fatal(err)
					}
					closeResult, deliveryFailed = result.Close, result.DeliveryFailed
					responseStarted = result.ResponseStarted
				}

				responseStarted()
				if tt.canceled {
					cancel()
				} else {
					deliveryFailed()
					deliveryFailed()
				}
				synctest.Wait()
				if f.logs.Len() != 0 {
					t.Fatal("result logged before caller finished delivery cleanup")
				}
				if err := closeResult(); err != nil {
					t.Fatal(err)
				}

				entries := logEntries(t, f.logs.String())
				if len(entries) != 1 {
					t.Fatalf("final logs = %v, want one entry", entries)
				}
				entry := entries[0]
				if entry["request_id"] != "undelivered" || entry["response_started"] != true || entry["input_tokens"] != float64(7) || entry["output_tokens"] != float64(3) {
					t.Fatalf("final log = %v, want request identity and known usage", entry)
				}
				want := "delivery_failed"
				if tt.canceled {
					want = "canceled"
				}
				if entry["result"] != want {
					t.Fatalf("result = %v, want %s", entry["result"], want)
				}
			})
		})
	}
}

func logEntries(t *testing.T, data string) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(data))
	var entries []map[string]any
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	return entries
}
