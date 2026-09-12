package openai_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/deyna256/clan/internal/retry"
	"github.com/stretchr/testify/require"
)

func TestRetrieveResponseReportsInvalidUsageOnce(t *testing.T) {
	var logs bytes.Buffer
	client := rateLimitedResourceClient(t, &logs)

	result, err := client.RetrieveResponse(t.Context(), testAttempt(), "resp_1", openai.ResponseRetrieveOptions{})

	var failure *openai.HTTPError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, http.StatusTooManyRequests, failure.StatusCode)
	require.Equal(t, generation.RateLimited, failure.Failure.Kind)
	require.Equal(t, retry.RetryAt, failure.Cooldown.Kind)
	require.Equal(t, count(12), result.Usage.Input)
	require.False(t, result.Usage.Output.Known)
	require.Nil(t, result.Output)
	require.Equal(t, 1, strings.Count(logs.String(), `"reason":"invalid_usage"`))
	require.NotContains(t, logs.String(), "private")
}

func TestCompactReportsInvalidUsageOnce(t *testing.T) {
	var logs bytes.Buffer
	client := rateLimitedResourceClient(t, &logs)

	result, err := client.Compact(t.Context(), testAttempt(), generation.CompactRequest{Model: generation.Some("test-model")})

	var failure *openai.HTTPError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, http.StatusTooManyRequests, failure.StatusCode)
	require.Equal(t, generation.RateLimited, failure.Failure.Kind)
	require.Equal(t, retry.RetryAt, failure.Cooldown.Kind)
	require.Equal(t, count(12), result.Usage.Input)
	require.False(t, result.Usage.Output.Known)
	require.Nil(t, result.Output)
	require.Equal(t, 1, strings.Count(logs.String(), `"reason":"invalid_usage"`))
	require.NotContains(t, logs.String(), "private")
}

func rateLimitedResourceClient(t *testing.T, logs io.Writer) *openai.Client {
	t.Helper()
	return testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"private-error"},"usage":{"input_tokens":12,"output_tokens":"private-counter"}}`)
	}, logs)
}

func TestStreamDoesNotLogProviderControlledEventNames(t *testing.T) {
	var logs bytes.Buffer
	unknown := frame(`{"type":"private-json-name"}`)
	invalid := "event: private-sse-name\ndata: invalid-json\n\n"
	body := unknown + unknown + invalid + invalid + frame(`{"type":"response.completed","response":`+textResponse("Hello", "null")+`}`)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}, &logs)

	events, err := readStream(t, client)

	require.ErrorIs(t, err, io.EOF)
	require.NotEmpty(t, events)
	require.IsType(t, generation.ResponseEnded{}, events[len(events)-1])
	require.NotContains(t, logs.String(), "private")
	require.Equal(t, 2, strings.Count(logs.String(), `"event_type":"unrecognized"`))
	require.Equal(t, 2, strings.Count(logs.String(), `"reason":"unknown_event"`))
	require.Equal(t, 2, strings.Count(logs.String(), `"reason":"invalid_event"`))
	require.Equal(t, 2, strings.Count(logs.String(), `"count":2`))
	require.Contains(t, logs.String(), `"request_id":"request-1"`)
}
