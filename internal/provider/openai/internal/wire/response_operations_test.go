package wire_test

import (
	"errors"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestCompactRequestPreservesPresence(t *testing.T) {
	request := generation.CompactRequest{
		Model: generation.Null[string](), Input: generation.Some([]generation.Item{}),
		Instructions: generation.Some(""), PreviousResponseID: generation.Some("resp_1"),
		PromptCacheOptions: generation.Some(generation.CompactCacheOptions{Mode: generation.Some("explicit"), TTL: generation.Some("30m")}),
		PromptCacheKey:     generation.Null[string](), PromptCacheRetention: generation.Some("in_memory"), ServiceTier: generation.Some("flex"),
	}

	body, err := wire.EncodeCompact(request)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":null,"input":[],"instructions":"","previous_response_id":"resp_1","prompt_cache_key":null,"prompt_cache_retention":"in_memory","prompt_cache_options":{"mode":"explicit","ttl":"30m"},"service_tier":"flex"}`)
}

func TestCountRequestPreservesIndependentOptions(t *testing.T) {
	request := generation.InputTokenRequest{
		Model: generation.Null[string](), Input: generation.Some([]generation.Item{generation.Message{Role: generation.User, Parts: []generation.Part{generation.Text{Text: "Hello"}}}}),
		ParallelToolCalls: generation.Some(false), Reasoning: generation.Null[generation.ReasoningOptions](),
		Text:  generation.Some(generation.TextOptions{Format: generation.JSONSchemaFormat{Name: "answer", Schema: []byte(`{"type":"object"}`)}, Verbosity: generation.Some("low")}),
		Tools: generation.Some([]generation.Tool(nil)), ToolChoice: generation.Null[generation.ToolChoice](),
		Personality: generation.Some("pragmatic"), Truncation: generation.Some("disabled"),
	}

	body, err := wire.EncodeInputTokenRequest(request)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":null,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}],"parallel_tool_calls":false,"reasoning":null,"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"}},"verbosity":"low"},"tools":[],"tool_choice":null,"personality":"pragmatic","truncation":"disabled"}`)

}

func TestCountRequestOmitsUnsetOptions(t *testing.T) {
	body, err := wire.EncodeInputTokenRequest(generation.InputTokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{}`)
}

func TestResourceGenerationValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request generation.InputTokenRequest
		field   string
	}{
		{name: "two conversation sources", request: generation.InputTokenRequest{Conversation: generation.Some("conv_1"), PreviousResponseID: generation.Some("resp_1")}, field: "conversation"},
		{name: "invalid text", request: generation.InputTokenRequest{Instructions: generation.Some("\xff")}, field: "instructions"},
		{name: "null personality", request: generation.InputTokenRequest{Personality: generation.Null[string]()}, field: "personality"},
		{name: "null truncation", request: generation.InputTokenRequest{Truncation: generation.Null[string]()}, field: "truncation"},
		{name: "nil choice is not explicit null", request: generation.InputTokenRequest{ToolChoice: generation.Some[generation.ToolChoice](nil)}, field: "tool_choice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := wire.EncodeInputTokenRequest(tc.request)

			var input *wire.InputError
			if body != nil || !errors.As(err, &input) || input.Field != tc.field {
				t.Fatalf("error = %v; want input field %s", err, tc.field)
			}
		})
	}
}

func TestCompactRequestRequiresModel(t *testing.T) {
	body, err := wire.EncodeCompact(generation.CompactRequest{})

	var input *wire.InputError
	if body != nil || !errors.As(err, &input) || input.Field != "model" {
		t.Fatalf("missing compact model = %v", err)
	}
}
