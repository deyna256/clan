package execution_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

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
		"input_tokens": float64(7), "output_tokens": float64(3), "total_tokens": float64(10)} {
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
