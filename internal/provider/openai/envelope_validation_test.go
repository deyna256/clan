package openai_test

import (
	"io"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/usage"
	"github.com/stretchr/testify/require"
)

func TestGenerateRejectsMissingOutputMessageStatus(t *testing.T) {
	for _, tc := range []struct{ name, status string }{
		{name: "missing status"},
		{name: "null status", status: `,"status":null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant"` + tc.status + `,"content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			var failure *generation.Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, generation.ProtocolError, failure.Kind)
			require.Equal(t, generation.Response{}, result.Response)
			wantUsage := usage.Snapshot{Input: count(10), Output: count(2), Total: count(12)}
			if result.Usage != wantUsage {
				t.Fatalf("usage = %#v; want %#v", result.Usage, wantUsage)
			}
		})
	}
}

func TestGenerateRetainsUsageOnInvalidUTF8(t *testing.T) {
	for _, tc := range []struct {
		name, original string
		wantIdentity   generation.Identity
	}{
		{name: "output", original: "Hello", wantIdentity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		{name: "response ID", original: "resp_1", wantIdentity: generation.Identity{Model: "test-model"}},
		{name: "model", original: "test-model", wantIdentity: generation.Identity{ID: "resp_1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := textResponse("Hello", `{"input_tokens":10,"output_tokens":2,"total_tokens":12}`)
			body = strings.Replace(body, tc.original, "\xff", 1)
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			var failure *generation.Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, generation.ProtocolError, failure.Kind)
			require.Equal(t, generation.Response{}, result.Response)
			wantUsage := usage.Snapshot{Input: count(10), Output: count(2), Total: count(12)}
			if result.Usage != wantUsage || result.Identity != tc.wantIdentity {
				t.Fatalf("Generate = %#v; want usage %#v and identity %#v", result, wantUsage, tc.wantIdentity)
			}
		})
	}
}
