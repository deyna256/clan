package wire_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/testutil"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeImagesAndStructuredOutput(t *testing.T) {
	request := imageRequest()
	unchanged := imageRequest()

	body, err := wire.EncodeRequest(request, true)

	require.NoError(t, err)
	assertJSON(t, body, `{
		"model":"test-model","stream":true,
		"input":[{"type":"message","role":"user","content":[
			{"type":"input_text","text":"Compare these images 👋"},
			{"type":"input_image","image_url":"https://images.example/a.png","detail":"high"},
			{"type":"input_image","image_url":"data:image/png;base64,YQ==","detail":"low"},
			{"type":"input_image","file_id":"file_1","detail":"auto"}
		]}],
		"temperature":0,"parallel_tool_calls":false,"store":false,
		"text":{"format":{"type":"json_schema","name":"comparison","strict":false,
			"schema":{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}},"required":["id"],"additionalProperties":false,"x-note":"keep me"}}}
	}`)
	require.Equal(t, unchanged, request)
}

func TestEncodeReasoningAndToolHistory(t *testing.T) {
	request := historyRequest()

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{
		"model":"test-model","stream":false,
		"input":[
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Checking"}],
			 "content":[{"type":"reasoning_text","text":"Work"}],"encrypted_content":"opaque+/=","status":"completed"},
			{"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":"commentary",
			 "content":[{"type":"output_text","text":"Looking it up","annotations":[]}]},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"id\":9007199254740993}","status":"completed"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"found\":true}"}
		],
		"tools":[{"type":"function","name":"lookup","description":"Find an item","parameters":{"type":"object","properties":{"id":{"type":"integer"}}},"strict":null}],
		"tool_choice":{"type":"function","name":"lookup"},
		"reasoning":{"effort":"high","summary":"auto"},
		"include":["reasoning.encrypted_content"]
	}`)
}

func TestEncodeParameterPresence(t *testing.T) {
	tests := []struct {
		name  string
		store generation.Optional[bool]
		text  generation.Optional[string]
		want  string
	}{
		{name: "omitted", want: `{"model":"test-model","input":[],"stream":false}`},
		{name: "explicit null", store: generation.Null[bool](), text: generation.Null[string](),
			want: `{"model":"test-model","input":[],"stream":false,"store":null,"instructions":null}`},
		{name: "zero values", store: generation.Some(false), text: generation.Some(""),
			want: `{"model":"test-model","input":[],"stream":false,"store":false,"instructions":""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := generation.Request{Model: "test-model", Instructions: tt.text}
			request.OpenAI.Store = tt.store

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, tt.want)
		})
	}
}

func TestEncodeRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name    string
		request generation.Request
		field   string
	}{
		{name: "missing model", field: "model"},
		{name: "background", request: generation.Request{Model: "test-model", OpenAI: generation.OpenAIOptions{Background: generation.Some(true)}}, field: "background"},
		{name: "nonfinite sampling", request: generation.Request{Model: "test-model", Temperature: generation.Some(math.NaN())}, field: "temperature"},
		{name: "zero token cap", request: generation.Request{Model: "test-model", MaxOutputTokens: generation.Some(int64(0))}, field: "max_output_tokens"},
		{name: "invalid model encoding", request: generation.Request{Model: "secret\xff"}, field: "model"},
		{name: "nil item", request: requestWith(nil), field: "input[0]"},
		{name: "typed nil item", request: requestWith((*generation.Message)(nil)), field: "input[0]"},
		{name: "pointer item", request: requestWith(&generation.Message{Role: generation.User}), field: "input[0]"},
		{name: "invalid role", request: requestWith(generation.Message{Role: "secret"}), field: "input[0].role"},
		{name: "missing content", request: requestWith(generation.Message{Role: generation.User}), field: "input[0].content"},
		{name: "reasoning in message", request: requestWith(message(generation.ReasoningText{Text: "secret"})), field: "input[0].content[0]"},
		{name: "invalid image source", request: requestWith(message(generation.ImageURL{URL: "file:///secret"})), field: "input[0].content[0].image_url"},
		{name: "invalid image detail", request: requestWith(message(generation.ImageFile{FileID: "file_1", Detail: "secret"})), field: "input[0].content[0].detail"},
		{name: "missing tool identity", request: requestWith(generation.ToolCall{Name: "secret"}), field: "input[0].call_id"},
		{name: "missing tool output", request: requestWith(generation.ToolResult{CallID: generation.Some("call_1")}), field: "input[0].output"},
		{name: "invalid text encoding", request: requestWith(message(generation.Text{Text: "secret\xff"})), field: "input[0].content[0].text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := wire.EncodeRequest(tt.request, false)

			var input *wire.InputError
			if body != nil || !errors.As(err, &input) || input.Field != tt.field {
				t.Fatalf("EncodeRequest = %q, %v; want no body and error at %s", body, err, tt.field)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error includes request contents")
			}
		})
	}
}

func TestEncodeRejectsInvalidSchema(t *testing.T) {
	for _, tc := range []struct{ name, schema string }{
		{name: "empty", schema: ""},
		{name: "null", schema: "null"},
		{name: "array", schema: "[]"},
		{name: "incomplete object", schema: `{"type":`},
		{name: "invalid UTF-8", schema: "{\"secret\":\"\xff\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := generation.Request{Model: "test-model", OutputFormat: generation.JSONSchemaFormat{Name: "answer", Schema: json.RawMessage(tc.schema)}}

			body, err := wire.EncodeRequest(request, false)

			var input *wire.InputError
			if body != nil || !errors.As(err, &input) || input.Field != "text.format.schema" {
				t.Fatalf("EncodeRequest = %q, %v; want schema error", body, err)
			}
		})
	}
}

func TestEncodePreservesIncompleteArgumentsAndEmptyReasoning(t *testing.T) {
	request := requestWith(
		generation.Reasoning{ID: "rs_1", OpenAI: generation.OpenAIReasoningData{EncryptedContent: generation.Some("opaque")}},
		generation.ToolCall{CallID: "call_1", Name: "lookup", Arguments: `{"id":`, Status: generation.ItemIncomplete},
	)

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"},
		{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"id\":","status":"incomplete"}
	]}`)
}

func TestEncodeToolResultPartsAndRefusal(t *testing.T) {
	request := requestWith(
		generation.ToolResult{CallID: generation.Some("call_1"), Output: generation.ToolPartsOutput{
			generation.Text{Text: "Screenshot"}, generation.ImageFile{FileID: "file_1", Detail: "high"},
		}},
		generation.Message{ID: "msg_1", Role: generation.Assistant, Status: generation.ItemCompleted,
			Parts: []generation.Part{generation.Refusal{Text: "Cannot help with that"}}},
	)

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"function_call_output","call_id":"call_1","output":[
			{"type":"input_text","text":"Screenshot"},
			{"type":"input_image","file_id":"file_1","detail":"high"}]},
		{"type":"message","id":"msg_1","role":"assistant","status":"completed",
		 "content":[{"type":"refusal","refusal":"Cannot help with that"}]}
	]}`)
}

func TestEncodeOptions(t *testing.T) {
	request := generation.Request{
		Model: "test-model", Instructions: generation.Some("Be brief"),
		MaxOutputTokens: generation.Some(int64(200)), TopP: generation.Some(0.5),
		ToolChoice: generation.ToolNone, OutputFormat: generation.TextFormat{},
		OpenAI: generation.OpenAIOptions{
			Background: generation.Some(false), PreviousResponseID: generation.Some("resp_1"),
			Verbosity: generation.Some("low"), MaxToolCalls: generation.Some(int64(2)),
			ServiceTier: generation.Some("auto"), Truncation: generation.Some("disabled"),
			PromptCacheKey: generation.Some("cache-1"), PromptCacheRetention: generation.Some("24h"),
			SafetyIdentifier: generation.Some("user-hash"),
		},
	}

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{
		"model":"test-model","input":[],"stream":false,"instructions":"Be brief",
		"max_output_tokens":200,"top_p":0.5,"tool_choice":"none",
		"text":{"format":{"type":"text"},"verbosity":"low"},
		"background":false,"previous_response_id":"resp_1","max_tool_calls":2,
		"service_tier":"auto","truncation":"disabled","prompt_cache_key":"cache-1",
		"prompt_cache_retention":"24h","safety_identifier":"user-hash"
	}`)
}

func TestEncodeRejectsInvalidTools(t *testing.T) {
	tests := []struct {
		name  string
		tool  generation.Tool
		field string
	}{
		{name: "nil tool", field: "tools[0]"},
		{name: "tool pointer", tool: (*generation.FunctionTool)(nil), field: "tools[0]"},
		{name: "missing name", tool: generation.FunctionTool{}, field: "tools[0].name"},
		{name: "missing parameters", tool: generation.FunctionTool{Name: "lookup"}, field: "tools[0].parameters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := generation.Request{Model: "test-model", Tools: []generation.Tool{tt.tool}}

			body, err := wire.EncodeRequest(request, false)

			var input *wire.InputError
			if body != nil || !errors.As(err, &input) || input.Field != tt.field {
				t.Fatalf("EncodeRequest = %q, %v; want error at %s", body, err, tt.field)
			}
		})
	}
}

func TestEncodeChoiceAndFormatVariants(t *testing.T) {
	tests := []struct {
		name   string
		choice generation.ToolChoice
		format generation.OutputFormat
		want   string
	}{
		{name: "automatic with JSON object", choice: generation.ToolAuto, format: generation.JSONObjectFormat{},
			want: `{"model":"test-model","input":[],"stream":false,"tool_choice":"auto","text":{"format":{"type":"json_object"}}}`},
		{name: "required", choice: generation.ToolRequired,
			want: `{"model":"test-model","input":[],"stream":false,"tool_choice":"required"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := generation.Request{Model: "test-model", ToolChoice: tt.choice, OutputFormat: tt.format}

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, tt.want)
		})
	}
}

func imageRequest() generation.Request {
	request := requestWith(message(
		generation.Text{Text: "Compare these images 👋"},
		generation.ImageURL{URL: "https://images.example/a.png", Detail: "high"},
		generation.ImageURL{URL: "data:image/png;base64,YQ==", Detail: "low"},
		generation.ImageFile{FileID: "file_1", Detail: "auto"},
	))
	request.Temperature = generation.Some(0.0)
	request.ParallelToolCalls = generation.Some(false)
	request.OpenAI.Store = generation.Some(false)
	request.OutputFormat = generation.JSONSchemaFormat{
		Name: "comparison", Strict: generation.Some(false),
		Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}},"required":["id"],"additionalProperties":false,"x-note":"keep me"}`),
	}
	return request
}

func historyRequest() generation.Request {
	request := requestWith(
		generation.Reasoning{ID: "rs_1", Status: generation.ItemCompleted,
			Parts:  []generation.Part{generation.ReasoningSummary{Text: "Checking"}, generation.ReasoningText{Text: "Work"}},
			OpenAI: generation.OpenAIReasoningData{EncryptedContent: generation.Some("opaque+/=")}},
		generation.Message{ID: "msg_1", Role: generation.Assistant, Status: generation.ItemCompleted,
			Parts:  []generation.Part{generation.Text{Text: "Looking it up"}},
			OpenAI: generation.OpenAIMessageData{Phase: generation.Some("commentary")}},
		generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "lookup", Arguments: `{"id":9007199254740993}`, Status: generation.ItemCompleted},
		generation.ToolResult{CallID: generation.Some("call_1"), Output: generation.ToolTextOutput(`{"found":true}`)},
	)
	request.Tools = []generation.Tool{generation.FunctionTool{
		Name: "lookup", Description: generation.Some("Find an item"), Strict: generation.Null[bool](),
		Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`),
	}}
	request.ToolChoice = generation.NamedTool{Name: "lookup"}
	request.OpenAI.Reasoning = generation.Some(generation.ReasoningOptions{Effort: generation.Some("high"), Summary: generation.Some("auto")})
	request.OpenAI.Include = generation.Some([]string{"reasoning.encrypted_content"})
	return request
}

func requestWith(items ...generation.Item) generation.Request {
	return generation.Request{Model: "test-model", Input: items}
}

func message(parts ...generation.Part) generation.Message {
	return generation.Message{Role: generation.User, Parts: parts}
}

func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	testutil.EqualJSON(t, string(got), want)
}
