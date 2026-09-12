package openai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/deyna256/clan/internal/provider/openai/internal/testutil"
	"github.com/deyna256/clan/internal/usage"
	"github.com/stretchr/testify/require"
)

func TestGeneratePreservesOutputAndUsage(t *testing.T) {
	requestBody := make(chan string, 1)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- string(body)
		_, _ = io.WriteString(w, textResponse("Hello", `{"input_tokens":10,"input_tokens_details":{"cached_tokens":4},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":15}`))
	}, io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	require.NoError(t, err)
	want := generation.Result{
		Identity: generation.Identity{ID: "resp_1", Model: "test-model"},
		Response: generation.Response{Output: []generation.Item{generation.Message{
			ID: "msg_1", Role: generation.Assistant, Status: generation.ItemCompleted,
			Parts: []generation.Part{generation.Text{Text: "Hello"}},
		}}, Finish: generation.Finish{Status: "completed", Reason: "stop"}},
		Usage: usage.Snapshot{Input: count(10), Output: count(5), Total: count(15), CacheRead: count(4), Reasoning: count(2)},
	}
	require.Equal(t, want, result)
	var sent struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal([]byte(<-requestBody), &sent); err != nil || sent.Model != "test-model" || sent.Stream {
		t.Fatalf("sent = %#v, %v", sent, err)
	}
}

func TestGenerateRetainsUsageOnContentFailure(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		kind         generation.FailureKind
	}{
		{name: "unsupported item", output: `[{"type":"new_content","id":"new_1"}]`, kind: generation.Unsupported},
		{name: "non-array output", output: `"malformed output"`, kind: generation.ProtocolError},
		{name: "missing message content", output: `[{"type":"message","id":"msg_1","role":"assistant","status":"completed"}]`, kind: generation.ProtocolError},
		{name: "null output", output: `null`, kind: generation.ProtocolError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, staticJSON(`{"id":"resp_1","model":"test-model","status":"completed","output":`+tc.output+`,"usage":{"input_tokens":12,"output_tokens":3}}`), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			var failure *generation.Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, tc.kind, failure.Kind)
			require.Equal(t, generation.Response{}, result.Response)
			require.Equal(t, count(12), result.Usage.Input)
			require.Equal(t, count(3), result.Usage.Output)
		})
	}
}

func TestGenerateExposesValidationErrors(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request reached upstream") }, io.Discard)
	request := textRequest()
	request.MaxOutputTokens = generation.Some(int64(-1))

	_, err := client.Generate(t.Context(), testAttempt(), request)

	var input *openai.InputError
	var failure *generation.Failure
	if !errors.As(err, &input) || input.Field != "max_output_tokens" || !errors.As(err, &failure) || failure.Kind != generation.InvalidRequest || failure.OutcomeUnknown {
		t.Fatalf("error = %#v; want classified field validation", err)
	}
}

func TestGenerateDistinguishesLocalAndUncertainFailures(t *testing.T) {
	for _, tc := range []struct {
		name, key          string
		cause              error
		kind               generation.FailureKind
		retryable, unknown bool
	}{
		{name: "invalid key", key: "invalid key", kind: generation.Authentication},
		{name: "dial", key: "valid", cause: &net.OpError{Op: "dial", Err: errors.New("refused")}, kind: generation.TransportError, retryable: true},
		{name: "read after send", key: "valid", cause: io.ErrUnexpectedEOF, kind: generation.TransportError, unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := clientWithTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if tc.cause == nil {
					t.Error("invalid key reached transport")
				}
				return nil, tc.cause
			}))
			attempt := testAttempt()
			attempt.Credentials.Key = tc.key

			_, err := client.Generate(t.Context(), attempt, textRequest())

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != tc.kind || failure.Retryable != tc.retryable || failure.OutcomeUnknown != tc.unknown {
				t.Fatalf("error = %#v; want kind %s, retry %v, unknown %v", err, tc.kind, tc.retryable, tc.unknown)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func clientWithTransport(t *testing.T, transport http.RoundTripper) *openai.Client {
	t.Helper()
	client, err := openai.New(openai.Config{BaseURL: "https://example.invalid/v1", MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 20}, &http.Client{Transport: transport}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	return client
}

func TestGenerateKeepsValidCountersWhenUsageIsMalformed(t *testing.T) {
	var logs bytes.Buffer
	client := testClient(t, staticJSON(textResponse("Usable", `{"input_tokens":12,"output_tokens":"private-content","total_tokens":-4}`)), &logs)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	if err != nil || result.Usage.Input != count(12) || result.Usage.Output.Known || result.Usage.Total.Known {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if !strings.Contains(logs.String(), `"reason":"invalid_usage"`) || strings.Contains(logs.String(), "private-content") {
		t.Fatalf("unexpected diagnostics: %s", logs.String())
	}
}

func TestGenerateClassifiesHTTPFailuresWithoutRetry(t *testing.T) {
	tests := []struct {
		name, code string
		status     int
		kind       generation.FailureKind
		retryable  bool
	}{
		{name: "temporary rate limit", code: "rate_limit_exceeded", status: 429, kind: generation.RateLimited, retryable: true},
		{name: "quota", code: "insufficient_quota", status: 429, kind: generation.QuotaExhausted},
		{name: "unknown rate limit", status: 429, kind: generation.Unavailable},
		{name: "authentication", status: 401, kind: generation.Authentication},
		{name: "server error", code: "server_error", status: 500, kind: generation.Unavailable, retryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := make(chan struct{}, 8)
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests <- struct{}{}
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, `{"error":{"code":"`+tt.code+`","message":"secret-key private-prompt"},"usage":{"input_tokens":3}}`)
			}, io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			var httpErr *openai.HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != tt.status || httpErr.Failure.Kind != tt.kind || httpErr.Failure.Retryable != tt.retryable {
				t.Fatalf("error = %#v; want status %d, kind %s, retry %v", err, tt.status, tt.kind, tt.retryable)
			}
			if len(requests) != 1 || result.Usage.Input != count(3) || httpErr.Cooldown.Until.IsZero() {
				t.Fatalf("requests = %d; result = %#v; cooldown = %#v", len(requests), result, httpErr.Cooldown)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestGenerateRejectsBackgroundBeforeDispatch(t *testing.T) {
	requests := make(chan struct{}, 1)
	client := testClient(t, func(http.ResponseWriter, *http.Request) { requests <- struct{}{} }, io.Discard)
	request := textRequest()
	request.OpenAI.Background = generation.Some(true)

	_, ordinaryErr := client.Generate(t.Context(), testAttempt(), request)
	stream, streamErr := client.GenerateStream(t.Context(), testAttempt(), request)

	if ordinaryErr == nil || streamErr == nil || stream != nil || len(requests) != 0 {
		t.Fatalf("ordinary = %v; stream = %v, %v; requests = %d", ordinaryErr, stream, streamErr, len(requests))
	}
}

func TestGenerateRespectsCancelledContext(t *testing.T) {
	client := testClient(t, staticJSON(textResponse("unused", "null")), io.Discard)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := client.Generate(ctx, testAttempt(), textRequest())

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
}

func testClient(t *testing.T, handler http.HandlerFunc, logs io.Writer) *openai.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := openai.New(openai.Config{
		BaseURL: server.URL, UpstreamID: "upstream-1", MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 20,
	}, server.Client(), slog.New(slog.NewJSONHandler(logs, nil)))
	require.NoError(t, err)
	return client
}

func staticJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

func testAttempt() openai.Attempt {
	return openai.Attempt{ID: "attempt-1", RequestID: "request-1", Credentials: account.APIKeyCredentials{Key: "secret-key"}}
}

func textRequest() generation.Request {
	return generation.Request{Model: "test-model", Input: []generation.Item{generation.Message{Role: generation.User, Parts: []generation.Part{generation.Text{Text: "private-prompt"}}}}}
}

func textResponse(text, usage string) string {
	encoded, _ := json.Marshal(text)
	return `{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":` + string(encoded) + `,"annotations":[]}]}],"usage":` + usage + `}`
}

func count(tokens int64) usage.Counter { return usage.Counter{Tokens: tokens, Known: true} }

func assertRequestJSON(t *testing.T, got, want string) {
	t.Helper()
	testutil.EqualJSON(t, got, want)
}
