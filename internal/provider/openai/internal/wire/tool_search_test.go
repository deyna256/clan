package wire_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeToolSearchDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool generation.OpenAIToolSearchTool
		want string
	}{
		{name: "default", want: `{"type":"tool_search"}`},
		{name: "server", tool: generation.OpenAIToolSearchTool{Execution: generation.Some("server")}, want: `{"type":"tool_search","execution":"server"}`},
		{
			name: "client",
			tool: generation.OpenAIToolSearchTool{Execution: generation.Some("client"), Description: generation.Some("Find tools"), Parameters: json.RawMessage(`{"type":"object"}`)},
			want: `{"type":"tool_search","execution":"client","description":"Find tools","parameters":{"type":"object"}}`,
		},
		{
			name: "explicit null",
			tool: generation.OpenAIToolSearchTool{Description: generation.Null[string](), Parameters: json.RawMessage(`null`)},
			want: `{"type":"tool_search","description":null,"parameters":null}`,
		},
		{name: "boolean schema", tool: generation.OpenAIToolSearchTool{Parameters: json.RawMessage(`false`)}, want: `{"type":"tool_search","parameters":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tools":[`+tc.want+`]}`)
		})
	}
}

func TestEncodeToolSearchInputPresence(t *testing.T) {
	request := requestWith(
		generation.OpenAIToolSearchCall{Arguments: json.RawMessage(`null`)},
		generation.OpenAIToolSearchCall{
			ID:        generation.Null[string](),
			CallID:    generation.Null[string](),
			Status:    generation.Null[generation.ItemStatus](),
			Execution: generation.Some("client"),
			Arguments: json.RawMessage(`[9007199254740993]`),
			CreatedBy: generation.Some("creator_1"),
		},
		generation.OpenAIToolSearchOutput{ID: generation.Null[string](), CallID: generation.Null[string](), Status: generation.Null[generation.ItemStatus](), CreatedBy: generation.Some("creator_2")},
		generation.OpenAIAdditionalTools{ID: generation.Null[string](), Role: "developer"},
	)

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"tool_search_call","arguments":null},
		{"type":"tool_search_call","id":null,"call_id":null,"status":null,"execution":"client","arguments":[9007199254740993]},
		{"type":"tool_search_output","id":null,"call_id":null,"status":null,"tools":[]},
		{"type":"additional_tools","id":null,"role":"developer","tools":[]}
	]}`)
}

func TestToolSearchReturnedMetadataAndReplay(t *testing.T) {
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[
		{"type":"tool_search_call","id":"search_1","call_id":null,"execution":"server","status":"completed","arguments":false,"created_by":"creator_1"},
		{"type":"tool_search_output","id":"loaded_1","call_id":null,"execution":"server","status":"completed","tools":[],"created_by":"creator_2"},
		{"type":"additional_tools","id":"extra_1","role":"developer","tools":[]}
	]}`))
	require.NoError(t, err)
	want := []generation.Item{
		generation.OpenAIToolSearchCall{
			ID:        generation.Some("search_1"),
			CallID:    generation.Null[string](),
			Execution: generation.Some("server"),
			Status:    generation.Some(generation.ItemCompleted),
			Arguments: json.RawMessage(`false`),
			CreatedBy: generation.Some("creator_1"),
		},
		generation.OpenAIToolSearchOutput{
			ID:        generation.Some("loaded_1"),
			CallID:    generation.Null[string](),
			Execution: generation.Some("server"),
			Status:    generation.Some(generation.ItemCompleted),
			Tools:     []generation.Tool{},
			CreatedBy: generation.Some("creator_2"),
		},
		generation.OpenAIAdditionalTools{ID: generation.Some("extra_1"), Role: "developer", Tools: []generation.Tool{}},
	}

	result, err := envelope.Result(nil)

	require.NoError(t, err)
	require.Equal(t, want, result.Response.Output)

	body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"tool_search_call","id":"search_1","call_id":null,"execution":"server","status":"completed","arguments":false},
		{"type":"tool_search_output","id":"loaded_1","call_id":null,"execution":"server","status":"completed","tools":[]},
		{"type":"additional_tools","id":"extra_1","role":"developer","tools":[]}
	]}`)
}

func TestRejectInvalidToolSearchDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool generation.OpenAIToolSearchTool
	}{
		{name: "null execution", tool: generation.OpenAIToolSearchTool{Execution: generation.Null[string]()}},
		{name: "unknown execution", tool: generation.OpenAIToolSearchTool{Execution: generation.Some("private")}},
		{name: "description UTF8", tool: generation.OpenAIToolSearchTool{Description: generation.Some("private\xff")}},
		{name: "malformed schema", tool: generation.OpenAIToolSearchTool{Parameters: json.RawMessage(`{"private":`)}},
		{name: "schema UTF8", tool: generation.OpenAIToolSearchTool{Parameters: json.RawMessage("\"private\xff\"")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}

			body, err := wire.EncodeRequest(request, false)

			assertInputError(t, body, err)
		})
	}
}

func TestRejectInvalidToolSearchInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		item generation.Item
	}{
		{name: "missing arguments", item: generation.OpenAIToolSearchCall{}},
		{name: "malformed arguments", item: generation.OpenAIToolSearchCall{Arguments: json.RawMessage(`{"private":`)}},
		{name: "arguments UTF8", item: generation.OpenAIToolSearchCall{Arguments: json.RawMessage("\"private\xff\"")}},
		{name: "ID UTF8", item: generation.OpenAIToolSearchOutput{ID: generation.Some("private\xff")}},
		{name: "call ID UTF8", item: generation.OpenAIToolSearchOutput{CallID: generation.Some("private\xff")}},
		{name: "status enum", item: generation.OpenAIToolSearchOutput{Status: generation.Some(generation.ItemStatus("private"))}},
		{name: "empty status", item: generation.OpenAIToolSearchOutput{Status: generation.Some(generation.ItemStatus(""))}},
		{name: "null execution", item: generation.OpenAIToolSearchOutput{Execution: generation.Null[string]()}},
		{name: "invalid catalog", item: generation.OpenAIToolSearchOutput{Tools: []generation.Tool{nil}}},
		{name: "additional role", item: generation.OpenAIAdditionalTools{Role: "private"}},
		{name: "additional returned role", item: generation.OpenAIAdditionalTools{Role: "assistant"}},
		{name: "additional ID UTF8", item: generation.OpenAIAdditionalTools{ID: generation.Some("private\xff"), Role: "developer"}},
		{name: "additional invalid catalog", item: generation.OpenAIAdditionalTools{Role: "developer", Tools: []generation.Tool{nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(tc.item)

			body, err := wire.EncodeRequest(request, false)

			assertInputError(t, body, err)
		})
	}
}

func TestRejectMalformedToolSearchReturnedItems(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "missing ID", raw: `{"type":"tool_search_call","call_id":null,"execution":"server","status":"completed","arguments":{}}`},
		{name: "null ID", raw: `{"type":"tool_search_call","id":null,"call_id":null,"execution":"server","status":"completed","arguments":{}}`},
		{name: "missing call ID", raw: `{"type":"tool_search_call","id":"s","execution":"server","status":"completed","arguments":{}}`},
		{name: "missing execution", raw: `{"type":"tool_search_call","id":"s","call_id":null,"status":"completed","arguments":{}}`},
		{name: "null execution", raw: `{"type":"tool_search_call","id":"s","call_id":null,"execution":null,"status":"completed","arguments":{}}`},
		{name: "missing status", raw: `{"type":"tool_search_call","id":"s","call_id":null,"execution":"server","arguments":{}}`},
		{name: "null status", raw: `{"type":"tool_search_call","id":"s","call_id":null,"execution":"server","status":null,"arguments":{}}`},
		{name: "null creator", raw: `{"type":"tool_search_call","id":"s","call_id":null,"execution":"server","status":"completed","arguments":{},"created_by":null}`},
		{name: "missing arguments", raw: `{"type":"tool_search_call","id":"s","call_id":null,"execution":"server","status":"completed"}`},
		{name: "missing tools", raw: `{"type":"tool_search_output","id":"s","call_id":null,"execution":"server","status":"completed"}`},
		{name: "null tools", raw: `{"type":"tool_search_output","id":"s","call_id":null,"execution":"server","status":"completed","tools":null}`},
		{name: "nonarray tools", raw: `{"type":"tool_search_output","id":"s","call_id":null,"execution":"server","status":"completed","tools":{}}`},
		{name: "additional missing ID", raw: `{"type":"additional_tools","role":"developer","tools":[]}`},
		{name: "additional null ID", raw: `{"type":"additional_tools","id":null,"role":"developer","tools":[]}`},
		{name: "additional missing role", raw: `{"type":"additional_tools","id":"extra_1","tools":[]}`},
		{name: "additional null role", raw: `{"type":"additional_tools","id":"extra_1","role":null,"tools":[]}`},
		{name: "additional unknown role", raw: `{"type":"additional_tools","id":"extra_1","role":"future","tools":[]}`},
		{name: "additional null tools", raw: `{"type":"additional_tools","id":"extra_1","role":"developer","tools":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[` + tc.raw + `]}`))
			require.NoError(t, err)

			_, err = envelope.Result(nil)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("Result error = %v; want protocol error", err)
			}
		})
	}
}
