package openai_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/deyna256/clan/internal/usage"
)

func TestGenerateRetainsUsageAfterHTTPReadFailure(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := textResponse("Hello", `{"input_tokens":12,"output_tokens":3}`)
			client := testClient(t, truncatedHTTPBody(body, status), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			assertFailureWithUsage(t, err, generation.TransportError, result.Usage)
			if result.Identity != (generation.Identity{ID: "resp_1", Model: "test-model"}) || !reflect.DeepEqual(result.Response, generation.Response{}) {
				t.Fatalf("result = %#v; want identity without successful content", result)
			}
		})
	}
}

func TestStreamStartupRetainsUsageAfterHTTPReadFailure(t *testing.T) {
	body := `{"error":{"code":"rate_limit_exceeded"},"usage":{"input_tokens":12,"output_tokens":3}}`
	client := testClient(t, truncatedHTTPBody(body, http.StatusTooManyRequests), io.Discard)

	stream, err := client.GenerateStream(t.Context(), testAttempt(), textRequest())

	if stream != nil {
		_ = stream.Close()
		t.Fatal("failed startup returned a stream")
	}
	var startup *openai.StartupError
	if !errors.As(err, &startup) {
		t.Fatalf("error = %v; want startup failure with usage", err)
	}
	assertFailureWithUsage(t, err, generation.TransportError, startup.Result.Usage)
}

func TestGenerateBoundsBodyBeforeExtractingUsage(t *testing.T) {
	body := textResponse("Hello", `{"input_tokens":12,"output_tokens":3}`)
	client, err := openai.New(openai.Config{
		BaseURL: "https://example.invalid/v1", MaxResponseBytes: int64(len(body) - 1), MaxEventBytes: 1024,
	}, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	assertProtocolError(t, err)
	if !reflect.DeepEqual(result, generation.Result{}) {
		t.Fatalf("oversized body produced a result: %#v", result)
	}
}

func TestRetrieveResponseRetainsUsageAfterBodyFailure(t *testing.T) {
	body := storedResponse("completed", `[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"opaque","annotations":[]}]}]`, "")
	for _, tc := range resourceBodyFailures(body) {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, tc.handler, io.Discard)

			result, err := client.RetrieveResponse(t.Context(), testAttempt(), "resp_1", openai.ResponseRetrieveOptions{})

			assertFailureWithUsage(t, err, tc.kind, result.Usage)
			if result.Output != nil {
				t.Fatalf("failed response returned output: %#v", result.Output)
			}
		})
	}
}

func TestCompactRetainsUsageAfterBodyFailure(t *testing.T) {
	body := `{"id":"cmp_1","object":"response.compaction","created_at":0,"output":[{"type":"compaction","id":"cmp_item_1","encrypted_content":"opaque"}],"usage":{"input_tokens":12,"output_tokens":3}}`
	for _, tc := range resourceBodyFailures(body) {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, tc.handler, io.Discard)

			result, err := client.Compact(t.Context(), testAttempt(), generation.CompactRequest{Model: generation.Some("test-model")})

			assertFailureWithUsage(t, err, tc.kind, result.Usage)
			if result.Output != nil {
				t.Fatalf("failed compaction returned output: %#v", result.Output)
			}
		})
	}
}

func truncatedHTTPBody(body string, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func resourceBodyFailures(body string) []struct {
	name    string
	handler http.HandlerFunc
	kind    generation.FailureKind
} {
	return []struct {
		name    string
		handler http.HandlerFunc
		kind    generation.FailureKind
	}{
		{name: "HTTP framing", handler: truncatedHTTPBody(body, http.StatusOK), kind: generation.TransportError},
		{name: "invalid UTF-8 content", handler: staticJSON(strings.ReplaceAll(body, "opaque", "opaque\xff")), kind: generation.ProtocolError},
	}
}

func assertFailureWithUsage(t *testing.T, err error, kind generation.FailureKind, got usage.Snapshot) {
	t.Helper()
	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Kind != kind {
		t.Fatalf("error = %v; want %v", err, kind)
	}
	want := usage.Snapshot{Input: count(12), Output: count(3)}
	if got != want {
		t.Fatalf("usage = %#v; want %#v", got, want)
	}
}
