package wire_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeCustomFormats(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format generation.CustomInputFormat
		field  string
	}{
		{name: "default"},
		{name: "text", format: generation.CustomTextFormat{}, field: `,"format":{"type":"text"}`},
		{name: "lark", format: generation.CustomGrammarFormat{Syntax: "lark", Definition: "start: /.+/\n"}, field: `,"format":{"type":"grammar","syntax":"lark","definition":"start: /.+/\n"}`},
		{name: "regex", format: generation.CustomGrammarFormat{Syntax: "regex", Definition: `\d{4}`}, field: `,"format":{"type":"grammar","syntax":"regex","definition":"\\d{4}"}`},
		{name: "space regex", format: generation.CustomGrammarFormat{Syntax: "regex", Definition: " "}, field: `,"format":{"type":"grammar","syntax":"regex","definition":" "}`},
		{name: "empty regex", format: generation.CustomGrammarFormat{Syntax: "regex"}, field: `,"format":{"type":"grammar","syntax":"regex","definition":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{generation.CustomTool{Name: "execute", Format: tc.format}}
			request.ToolChoice = generation.NamedCustomTool{Name: "execute"}

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tools":[{"type":"custom","name":"execute"`+tc.field+`}],"tool_choice":{"type":"custom","name":"execute"}}`)
		})
	}
}

func TestEncodeCustomOptionsAndHistory(t *testing.T) {
	request := customRequest()
	unchanged := customRequest()

	body, err := wire.EncodeRequest(request, true)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":true,
		"tools":[{"type":"custom","name":"execute","description":"Run text","async":false,"defer_loading":true,"allowed_callers":["direct","programmatic"]}],
		"input":[
			{"type":"custom_tool_call","id":"ct_1","call_id":"call_1","name":"execute","input":"print('Привет')\n","async":false,"namespace":"sandbox","caller":{"type":"program","caller_id":"program_1"}},
			{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":[{"type":"input_text","text":"Привет"},{"type":"input_image","file_id":"file_image","detail":"high"},{"type":"input_file","file_data":"YQ==","filename":"result.txt","prompt_cache_breakpoint":{"mode":"explicit"}}],"caller":{"type":"program","caller_id":"program_1"}}
		]}`)
	require.Equal(t, unchanged, request)
}

func TestEncodeCustomEmptyValues(t *testing.T) {
	request := requestWith(
		generation.CustomToolCall{CallID: "call_1", Name: "execute", OpenAI: generation.OpenAIToolCallData{Caller: generation.Null[generation.OpenAIToolCaller]()}},
		generation.CustomToolResult{CallID: "call_1", Output: generation.ToolTextOutput(""), Caller: generation.Some(generation.OpenAIToolCaller{Type: "direct"})},
		generation.CustomToolResult{CallID: "call_2", Output: generation.ToolPartsOutput{}},
	)
	request.Tools = []generation.Tool{generation.CustomTool{Name: "execute", OpenAI: generation.OpenAICustomToolOptions{AllowedCallers: generation.Some([]string{})}}}

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"tools":[{"type":"custom","name":"execute","allowed_callers":[]}],"input":[
		{"type":"custom_tool_call","call_id":"call_1","name":"execute","input":"","caller":null},
		{"type":"custom_tool_call_output","call_id":"call_1","output":"","caller":{"type":"direct"}},
		{"type":"custom_tool_call_output","call_id":"call_2","output":[]}
	]}`)
}

func TestRejectInvalidCustomTools(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool generation.CustomTool
	}{
		{name: "name"},
		{name: "null description", tool: generation.CustomTool{Name: "execute", Description: generation.Null[string]()}},
		{name: "null async", tool: generation.CustomTool{Name: "execute", OpenAI: generation.OpenAICustomToolOptions{Async: generation.Null[bool]()}}},
		{name: "null deferred", tool: generation.CustomTool{Name: "execute", OpenAI: generation.OpenAICustomToolOptions{DeferLoading: generation.Null[bool]()}}},
		{name: "description", tool: generation.CustomTool{Name: "execute", Description: generation.Some("private\xff")}},
		{name: "syntax", tool: generation.CustomTool{Name: "execute", Format: generation.CustomGrammarFormat{Syntax: "go", Definition: "private"}}},
		{name: "grammar UTF-8", tool: generation.CustomTool{Name: "execute", Format: generation.CustomGrammarFormat{Syntax: "regex", Definition: "private\xff"}}},
		{name: "caller", tool: generation.CustomTool{Name: "execute", OpenAI: generation.OpenAICustomToolOptions{AllowedCallers: generation.Some([]string{"program"})}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}

			body, err := wire.EncodeRequest(request, false)

			assertInputError(t, body, err)
		})
	}
}

func TestRejectInvalidCustomHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		item generation.Item
	}{
		{name: "null async", item: generation.CustomToolCall{CallID: "c", Name: "f", OpenAI: generation.OpenAIToolCallData{Async: generation.Null[bool]()}}},
		{name: "null namespace", item: generation.CustomToolCall{CallID: "c", Name: "f", OpenAI: generation.OpenAIToolCallData{Namespace: generation.Null[string]()}}},
		{name: "call ID", item: generation.CustomToolCall{Name: "execute"}},
		{name: "name", item: generation.CustomToolCall{CallID: "call_1"}},
		{name: "input UTF-8", item: generation.CustomToolCall{CallID: "call_1", Name: "execute", Input: "private\xff"}},
		{name: "missing program ID", item: generation.CustomToolResult{CallID: "call_1", Output: generation.ToolTextOutput(""), Caller: generation.Some(generation.OpenAIToolCaller{Type: "program"})}},
		{
			name: "direct with program ID",
			item: generation.CustomToolResult{CallID: "call_1", Output: generation.ToolTextOutput(""), Caller: generation.Some(generation.OpenAIToolCaller{Type: "direct", CallerID: "private"})},
		},
		{name: "missing output", item: generation.CustomToolResult{CallID: "call_1"}},
		{name: "output UTF-8", item: generation.CustomToolResult{CallID: "call_1", Output: generation.ToolTextOutput("private\xff")}},
		{name: "output part", item: generation.CustomToolResult{CallID: "call_1", Output: generation.ToolPartsOutput{generation.Refusal{Text: "private"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(tc.item)

			body, err := wire.EncodeRequest(request, false)

			assertInputError(t, body, err)
		})
	}
}

func assertInputError(t *testing.T, body []byte, err error) {
	t.Helper()
	var inputError *wire.InputError
	var failure *generation.Failure
	if body != nil || !errors.As(err, &inputError) || !errors.As(err, &failure) || failure.Kind != generation.InvalidRequest {
		t.Fatalf("EncodeRequest = %s, %v; want input error", body, err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("error leaked input content")
	}
}

func customRequest() generation.Request {
	caller := generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"})
	request := requestWith(
		generation.CustomToolCall{
			ID:     "ct_1",
			CallID: "call_1",
			Name:   "execute",
			Input:  "print('Привет')\n",
			Status: "completed",
			OpenAI: generation.OpenAIToolCallData{Async: generation.Some(false), Namespace: generation.Some("sandbox"), Caller: caller},
		},
		generation.CustomToolResult{ID: "out_1", CallID: "call_1", Status: "completed", Caller: caller, Output: generation.ToolPartsOutput{
			generation.Text{Text: "Привет"}, generation.ImageFile{FileID: "file_image", Detail: "high"},
			generation.FileData{Data: "YQ==", Options: generation.FileOptions{Filename: generation.Some("result.txt"), OpenAI: generation.OpenAIFileOptions{PromptCacheBreakpoint: true}}},
		}},
	)
	request.Tools = []generation.Tool{generation.CustomTool{
		Name:        "execute",
		Description: generation.Some("Run text"),
		OpenAI:      generation.OpenAICustomToolOptions{Async: generation.Some(false), DeferLoading: generation.Some(true), AllowedCallers: generation.Some([]string{"direct", "programmatic"})},
	}}
	return request
}
