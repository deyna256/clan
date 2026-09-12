package wire_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeRequestOptions(t *testing.T) {
	request := requestWith()
	request.OpenAI = extendedOptions()
	unchanged := extendedOptions()

	body, err := wire.EncodeRequest(request, true)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":true,
		"metadata":{"project":"clan"},"conversation":"conv_1","user":"user_1",
		"prompt":{"id":"pmpt_1","version":"2","variables":{
			"name":"Привет","note":{"type":"input_text","text":"Read this"},
			"image":{"type":"input_image","image_url":"https://example.com/image.png","detail":"low"},
			"image_file":{"type":"input_image","file_id":"img_1","detail":"high"},
			"file":{"type":"input_file","file_id":"file_1"},
			"url":{"type":"input_file","file_url":"https://example.com/report.pdf"},
			"data":{"type":"input_file","file_data":"YQ==","filename":"a.txt"}
		}},
		"moderation":{"model":"omni-moderation-latest","policy":{"input":{"mode":"block"},"output":{"mode":"score"}}},
		"context_management":[{"type":"compaction","compact_threshold":12000}],
		"prompt_cache_options":{"mode":"explicit","ttl":"30m","comparison_response_id":"resp_1"},
		"prompt_cache_retention":"in_memory",
		"stream_options":{"include_obfuscation":false},
		"reasoning":{"context":"all_turns","effort":"max","summary":"detailed","generate_summary":"auto","mode":"future_mode"},
		"include":["reasoning.encrypted_content"]
	}`)
	require.Equal(t, unchanged, request.OpenAI)
}

func TestRequestOptionPresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options generation.OpenAIOptions
		fields  string
	}{
		{name: "omitted"},
		{name: "nullable objects", options: generation.OpenAIOptions{
			Metadata: generation.Null[map[string]string](), Conversation: generation.Null[string](),
			Prompt: generation.Null[generation.OpenAIPrompt](), Moderation: generation.Null[generation.OpenAIModerationOptions](),
			ContextManagement: generation.Null[[]generation.OpenAIContextManagement](), StreamOptions: generation.Null[generation.OpenAIStreamOptions](),
			Reasoning: generation.Null[generation.ReasoningOptions](), Include: generation.Null[[]string](),
		}, fields: `,"metadata":null,"conversation":null,"prompt":null,"moderation":null,"context_management":null,"stream_options":null,"reasoning":null,"include":null`},
		{name: "empty values", options: generation.OpenAIOptions{
			Metadata: generation.Some(map[string]string(nil)), ContextManagement: generation.Some([]generation.OpenAIContextManagement(nil)),
			PromptCacheOptions: generation.Some(generation.OpenAIPromptCacheOptions{}), StreamOptions: generation.Some(generation.OpenAIStreamOptions{}),
			Reasoning: generation.Some(generation.ReasoningOptions{}), Include: generation.Some([]string(nil)), User: generation.Some(""),
		}, fields: `,"metadata":{},"context_management":[],"prompt_cache_options":{},"stream_options":{},"reasoning":{},"include":[],"user":""`},
		{name: "nested nulls", options: generation.OpenAIOptions{
			Prompt:             generation.Some(generation.OpenAIPrompt{ID: "p", Version: generation.Null[string](), Variables: generation.Null[map[string]generation.PromptVariable]()}),
			Moderation:         generation.Some(generation.OpenAIModerationOptions{Model: "m", Policy: generation.Null[generation.OpenAIModerationPolicy]()}),
			PromptCacheOptions: generation.Some(generation.OpenAIPromptCacheOptions{ComparisonResponseID: generation.Null[string]()}),
			ContextManagement:  generation.Some([]generation.OpenAIContextManagement{{Type: "compaction", CompactThreshold: generation.Null[int64]()}}),
			Reasoning:          generation.Some(generation.ReasoningOptions{Context: generation.Null[string](), Effort: generation.Null[string](), Summary: generation.Null[string](), GenerateSummary: generation.Null[string]()}),
		}, fields: `,"prompt":{"id":"p","version":null,"variables":null},"moderation":{"model":"m","policy":null},"prompt_cache_options":{"comparison_response_id":null},"context_management":[{"type":"compaction","compact_threshold":null}],"reasoning":{"context":null,"effort":null,"summary":null,"generate_summary":null}`},
		{name: "nested empty values", options: generation.OpenAIOptions{
			Prompt:            generation.Some(generation.OpenAIPrompt{ID: "p", Version: generation.Some(""), Variables: generation.Some(map[string]generation.PromptVariable{})}),
			Moderation:        generation.Some(generation.OpenAIModerationOptions{Model: "m", Policy: generation.Some(generation.OpenAIModerationPolicy{})}),
			ContextManagement: generation.Some([]generation.OpenAIContextManagement{{Type: "compaction", CompactThreshold: generation.Some(int64(0))}}),
		}, fields: `,"prompt":{"id":"p","version":"","variables":{}},"moderation":{"model":"m","policy":{}},"context_management":[{"type":"compaction","compact_threshold":0}]`},
		{name: "nullable moderation directions", options: generation.OpenAIOptions{
			Moderation: generation.Some(generation.OpenAIModerationOptions{Model: "m", Policy: generation.Some(generation.OpenAIModerationPolicy{Input: generation.Null[generation.OpenAIModerationRule](), Output: generation.Null[generation.OpenAIModerationRule]()})}),
		}, fields: `,"moderation":{"model":"m","policy":{"input":null,"output":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.OpenAI = tc.options

			body, err := wire.EncodeRequest(request, true)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","input":[],"stream":true`+tc.fields+`}`)
		})
	}
}

func TestRequestConversationAndStreamingConstraints(t *testing.T) {
	for _, tc := range []struct {
		name      string
		options   generation.OpenAIOptions
		streaming bool
		field     string
	}{
		{name: "conversation and previous response", options: generation.OpenAIOptions{Conversation: generation.Some("conv_1"), PreviousResponseID: generation.Some("resp_1")}, field: "conversation"},
		{name: "empty conversation", options: generation.OpenAIOptions{Conversation: generation.Some("")}, field: "conversation"},
		{name: "invalid conversation UTF-8", options: generation.OpenAIOptions{Conversation: generation.Some("secret\xff")}, field: "conversation"},
		{name: "stream options without stream", options: generation.OpenAIOptions{StreamOptions: generation.Some(generation.OpenAIStreamOptions{})}, field: "stream_options"},
		{name: "null user", options: generation.OpenAIOptions{User: generation.Null[string]()}, field: "user"},
		{name: "invalid user UTF-8", options: generation.OpenAIOptions{User: generation.Some("secret\xff")}, field: "user"},
		{name: "null cache options", options: generation.OpenAIOptions{PromptCacheOptions: generation.Null[generation.OpenAIPromptCacheOptions]()}, field: "prompt_cache_options"},
		{
			name:    "null cache mode",
			options: generation.OpenAIOptions{PromptCacheOptions: generation.Some(generation.OpenAIPromptCacheOptions{Mode: generation.Null[string]()})},
			field:   "prompt_cache_options.mode",
		},
		{name: "invalid cache TTL", options: generation.OpenAIOptions{PromptCacheOptions: generation.Some(generation.OpenAIPromptCacheOptions{TTL: generation.Some("24h")})}, field: "prompt_cache_options.ttl"},
		{
			name:      "null obfuscation",
			options:   generation.OpenAIOptions{StreamOptions: generation.Some(generation.OpenAIStreamOptions{IncludeObfuscation: generation.Null[bool]()})},
			streaming: true,
			field:     "stream_options.include_obfuscation",
		},
		{name: "unknown context entry", options: generation.OpenAIOptions{ContextManagement: generation.Some([]generation.OpenAIContextManagement{{Type: "secret"}})}, field: "context_management[0].type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.OpenAI = tc.options

			body, err := wire.EncodeRequest(request, tc.streaming)

			assertOptionError(t, body, err, tc.field)
		})
	}
}

func TestNullableConversationDoesNotConflict(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		conversation, previous generation.Optional[string]
		fields                 string
	}{
		{name: "null conversation", conversation: generation.Null[string](), previous: generation.Some("resp_1"), fields: `"conversation":null,"previous_response_id":"resp_1"`},
		{name: "null previous response", conversation: generation.Some("conv_1"), previous: generation.Null[string](), fields: `"conversation":"conv_1","previous_response_id":null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.OpenAI.Conversation = tc.conversation
			request.OpenAI.PreviousResponseID = tc.previous

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,`+tc.fields+`}`)
		})
	}
}

func TestMetadataLimitsCountCharacters(t *testing.T) {
	request := requestWith()
	request.OpenAI.Metadata = generation.Some(map[string]string{strings.Repeat("界", 64): strings.Repeat("я", 512)})

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"metadata":{"`+strings.Repeat("界", 64)+`":"`+strings.Repeat("я", 512)+`"}}`)
}

func TestRejectInvalidMetadata(t *testing.T) {
	tooMany := make(map[string]string)
	for i := range 17 {
		tooMany[fmt.Sprint(i)] = "value"
	}
	for _, tc := range []struct {
		name     string
		metadata map[string]string
	}{
		{name: "too many entries", metadata: tooMany},
		{name: "long key", metadata: map[string]string{strings.Repeat("界", 65): "value"}},
		{name: "long value", metadata: map[string]string{"key": strings.Repeat("界", 513)}},
		{name: "invalid key", metadata: map[string]string{"secret\xff": "value"}},
		{name: "invalid value", metadata: map[string]string{"key": "secret\xff"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.OpenAI.Metadata = generation.Some(tc.metadata)

			body, err := wire.EncodeRequest(request, false)

			assertOptionError(t, body, err, "metadata")
		})
	}
}

func TestRejectInvalidPrompt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt generation.OpenAIPrompt
		field  string
	}{
		{name: "missing ID", field: "prompt.id"},
		{name: "invalid version", prompt: generation.OpenAIPrompt{ID: "p", Version: generation.Some("secret\xff")}, field: "prompt.version"},
		{name: "null variable", prompt: promptWith(nil), field: "prompt.variables"},
		{name: "pointer variable", prompt: promptWith(&generation.Text{Text: "secret"}), field: "prompt.variables"},
		{name: "invalid string", prompt: promptWith(generation.PromptString("secret\xff")), field: "prompt.variables"},
		{name: "invalid image", prompt: promptWith(generation.ImageURL{URL: "file:///secret"}), field: "prompt.variables.image_url"},
		{name: "output text metadata", prompt: promptWith(generation.Text{Text: "secret", OpenAI: generation.OpenAITextData{Annotations: []generation.Annotation{{}}}}), field: "prompt.variables"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.OpenAI.Prompt = generation.Some(tc.prompt)

			body, err := wire.EncodeRequest(request, false)

			assertOptionError(t, body, err, tc.field)
		})
	}
}

func TestRejectInvalidReasoningAndModeration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options generation.OpenAIOptions
		field   string
	}{
		{name: "unknown effort", options: generation.OpenAIOptions{Reasoning: generation.Some(generation.ReasoningOptions{Effort: generation.Some("secret")})}, field: "reasoning.effort"},
		{name: "unknown context", options: generation.OpenAIOptions{Reasoning: generation.Some(generation.ReasoningOptions{Context: generation.Some("secret")})}, field: "reasoning.context"},
		{name: "unknown summary", options: generation.OpenAIOptions{Reasoning: generation.Some(generation.ReasoningOptions{GenerateSummary: generation.Some("secret")})}, field: "reasoning.generate_summary"},
		{name: "null mode", options: generation.OpenAIOptions{Reasoning: generation.Some(generation.ReasoningOptions{Mode: generation.Null[string]()})}, field: "reasoning.mode"},
		{name: "missing moderation model", options: generation.OpenAIOptions{Moderation: generation.Some(generation.OpenAIModerationOptions{})}, field: "moderation.model"},
		{
			name:    "missing moderation mode",
			options: generation.OpenAIOptions{Moderation: generation.Some(generation.OpenAIModerationOptions{Model: "m", Policy: generation.Some(generation.OpenAIModerationPolicy{Input: generation.Some(generation.OpenAIModerationRule{})})})},
			field:   "moderation.policy.input.mode",
		},
		{
			name:    "invalid moderation mode",
			options: generation.OpenAIOptions{Moderation: generation.Some(generation.OpenAIModerationOptions{Model: "m", Policy: generation.Some(generation.OpenAIModerationPolicy{Output: generation.Some(generation.OpenAIModerationRule{Mode: "secret"})})})},
			field:   "moderation.policy.output.mode",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.OpenAI = tc.options

			body, err := wire.EncodeRequest(request, false)

			assertOptionError(t, body, err, tc.field)
		})
	}
}

func TestCompactionTriggerMustBeLastInput(t *testing.T) {
	request := requestWith(generation.OpenAICompactionTrigger{}, generation.Message{Role: generation.User, Parts: []generation.Part{generation.Text{Text: "after"}}})

	body, err := wire.EncodeRequest(request, false)

	assertOptionError(t, body, err, "input[0]")
}

func promptWith(variable generation.PromptVariable) generation.OpenAIPrompt {
	return generation.OpenAIPrompt{ID: "p", Variables: generation.Some(map[string]generation.PromptVariable{"value": variable})}
}

func extendedOptions() generation.OpenAIOptions {
	return generation.OpenAIOptions{
		Metadata: generation.Some(map[string]string{"project": "clan"}), Conversation: generation.Some("conv_1"), User: generation.Some("user_1"),
		Prompt: generation.Some(generation.OpenAIPrompt{ID: "pmpt_1", Version: generation.Some("2"), Variables: generation.Some(map[string]generation.PromptVariable{
			"name": generation.PromptString("Привет"), "note": generation.Text{Text: "Read this"},
			"image": generation.ImageURL{URL: "https://example.com/image.png", Detail: "low"}, "image_file": generation.ImageFile{FileID: "img_1", Detail: "high"},
			"file": generation.FileID{ID: "file_1"}, "url": generation.FileURL{URL: "https://example.com/report.pdf"},
			"data": generation.FileData{Data: "YQ==", Options: generation.FileOptions{Filename: generation.Some("a.txt")}},
		})}),
		Moderation: generation.Some(generation.OpenAIModerationOptions{Model: "omni-moderation-latest", Policy: generation.Some(generation.OpenAIModerationPolicy{
			Input: generation.Some(generation.OpenAIModerationRule{Mode: "block"}), Output: generation.Some(generation.OpenAIModerationRule{Mode: "score"}),
		})}),
		ContextManagement:    generation.Some([]generation.OpenAIContextManagement{{Type: "compaction", CompactThreshold: generation.Some(int64(12000))}}),
		PromptCacheOptions:   generation.Some(generation.OpenAIPromptCacheOptions{Mode: generation.Some("explicit"), TTL: generation.Some("30m"), ComparisonResponseID: generation.Some("resp_1")}),
		PromptCacheRetention: generation.Some("in_memory"), StreamOptions: generation.Some(generation.OpenAIStreamOptions{IncludeObfuscation: generation.Some(false)}),
		Reasoning: generation.Some(generation.ReasoningOptions{Context: generation.Some("all_turns"), Effort: generation.Some("max"), Summary: generation.Some("detailed"), GenerateSummary: generation.Some("auto"), Mode: generation.Some("future_mode")}),
		Include:   generation.Some([]string{"reasoning.encrypted_content"}),
	}
}

func assertOptionError(t *testing.T, body []byte, err error, field string) {
	t.Helper()
	var input *wire.InputError
	var failure *generation.Failure
	if len(body) != 0 || !errors.As(err, &input) || input.Field != field || !errors.As(err, &failure) || failure.Kind != generation.InvalidRequest {
		t.Fatalf("body = %s; error = %v; want invalid request field %q", body, err, field)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error exposes request data: %v", err)
	}
}
