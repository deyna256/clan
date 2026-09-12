package wire_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestMCPToolConfiguration(t *testing.T) {
	tool := generation.OpenAIMCPTool{
		ServerLabel: "docs", Endpoint: generation.MCPServerURL("https://example.org/mcp"),
		Authorization: generation.Some("private-token"), ServerDescription: generation.Some("Search documents"),
		Headers: generation.Some(map[string]string{"X-Project": "private-project"}), AllowedCallers: generation.Some([]string{"direct", "programmatic"}),
		AllowedTools: generation.Some[generation.MCPAllowedTools](generation.MCPToolFilter{ReadOnly: generation.Some(false), ToolNames: []string{"search", "write"}}),
		RequireApproval: generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalFilter{
			Always: generation.Some(generation.MCPToolFilter{ToolNames: []string{"write"}}),
			Never:  generation.Some(generation.MCPToolFilter{ReadOnly: generation.Some(true)}),
		}), DeferLoading: generation.Some(false),
	}
	request := requestWith()
	request.Tools = []generation.Tool{tool}
	request.ToolChoice = generation.OpenAIMCPChoice{ServerLabel: "docs", Name: generation.Some("search")}

	body, err := wire.EncodeRequest(request, true)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":true,"input":[],"tools":[{"type":"mcp","server_label":"docs","server_url":"https://example.org/mcp","authorization":"private-token","server_description":"Search documents","headers":{"X-Project":"private-project"},"allowed_callers":["direct","programmatic"],"allowed_tools":{"read_only":false,"tool_names":["search","write"]},"require_approval":{"always":{"tool_names":["write"]},"never":{"read_only":true}},"defer_loading":false}],"tool_choice":{"type":"mcp","server_label":"docs","name":"search"}}`)
}

func TestMCPToolVariantsAndPresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool generation.OpenAIMCPTool
		want string
	}{
		{
			name: "connector",
			tool: generation.OpenAIMCPTool{ServerLabel: "drive", Endpoint: generation.MCPConnectorID("connector_googledrive")},
			want: `{"type":"mcp","server_label":"drive","connector_id":"connector_googledrive"}`,
		},
		{
			name: "tunnel",
			tool: generation.OpenAIMCPTool{ServerLabel: "private", Endpoint: generation.MCPTunnelID("tunnel_1"), DeferLoading: generation.Some(true)},
			want: `{"type":"mcp","server_label":"private","tunnel_id":"tunnel_1","defer_loading":true}`,
		},
		{
			name: "nullable",
			tool: generation.OpenAIMCPTool{
				ServerLabel:     "s",
				Endpoint:        generation.MCPTunnelID("t"),
				Headers:         generation.Null[map[string]string](),
				AllowedCallers:  generation.Null[[]string](),
				AllowedTools:    generation.Null[generation.MCPAllowedTools](),
				RequireApproval: generation.Null[generation.MCPApprovalPolicy](),
			},
			want: `{"type":"mcp","server_label":"s","tunnel_id":"t","headers":null,"allowed_callers":null,"allowed_tools":null,"require_approval":null}`,
		},
		{
			name: "empty collections",
			tool: generation.OpenAIMCPTool{
				ServerLabel:     "s",
				Endpoint:        generation.MCPTunnelID("t"),
				Headers:         generation.Some(map[string]string{}),
				AllowedCallers:  generation.Some([]string{}),
				AllowedTools:    generation.Some[generation.MCPAllowedTools](generation.MCPToolNames{}),
				RequireApproval: generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalFilter{}),
			},
			want: `{"type":"mcp","server_label":"s","tunnel_id":"t","headers":{},"allowed_callers":[],"allowed_tools":[],"require_approval":{}}`,
		},
		{
			name: "names and always",
			tool: generation.OpenAIMCPTool{
				ServerLabel:     "s",
				Endpoint:        generation.MCPTunnelID("t"),
				AllowedTools:    generation.Some[generation.MCPAllowedTools](generation.MCPToolNames{"read"}),
				RequireApproval: generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalMode("always")),
			},
			want: `{"type":"mcp","server_label":"s","tunnel_id":"t","allowed_tools":["read"],"require_approval":"always"}`,
		},
		{
			name: "empty filter and never",
			tool: generation.OpenAIMCPTool{
				ServerLabel:     "s",
				Endpoint:        generation.MCPTunnelID("t"),
				AllowedTools:    generation.Some[generation.MCPAllowedTools](generation.MCPToolFilter{ToolNames: []string{}}),
				RequireApproval: generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalMode("never")),
			},
			want: `{"type":"mcp","server_label":"s","tunnel_id":"t","allowed_tools":{"tool_names":[]},"require_approval":"never"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}
			request.ToolChoice = generation.OpenAIMCPChoice{ServerLabel: tc.tool.ServerLabel, Name: generation.Null[string]()}

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[],"tools":[`+tc.want+`],"tool_choice":{"type":"mcp","server_label":"`+tc.tool.ServerLabel+`","name":null}}`)
		})
	}
}

func TestMCPItemsPreserveRawValuesAndApprovalDenial(t *testing.T) {
	output := `[
	{"type":"mcp_list_tools","id":"list_1","server_label":"docs","tools":[{"name":"find","input_schema":{"type":"object","minimum":9007199254740993},"annotations":{"large":9007199254740995},"description":null},{"name":"anything","input_schema":true,"annotations":null,"description":""},{"name":"unknown","input_schema":null}],"error":null},
	{"type":"mcp_call","id":"call_1","name":"find","server_label":"docs","arguments":"{\"limit\":9007199254740993}","status":"completed","approval_request_id":null,"output":"","error":null},
	{"type":"mcp_approval_request","id":"approve_1","name":"write","server_label":"docs","arguments":"{}"},
	{"type":"mcp_approval_response","id":"decision_1","approval_request_id":"approve_1","approve":false,"reason":"Declined"}
	]`

	result, err := mcpResponse(output)

	require.NoError(t, err)
	if result.Response.Finish.Reason != "tool_calls" {
		t.Fatalf("approval finish = %s", result.Response.Finish.Reason)
	}
	if len(result.Response.Output) != 4 {
		t.Fatalf("output = %#v; want 4 items", result.Response.Output)
	}
	list, ok := result.Response.Output[0].(generation.OpenAIMCPListTools)
	if !ok {
		t.Fatalf("output[0] = %T; want generation.OpenAIMCPListTools", result.Response.Output[0])
	}
	if len(list.Tools) != 3 {
		t.Fatalf("tools = %#v; want three tools", list.Tools)
	}
	if string(list.Tools[0].InputSchema) != `{"type":"object","minimum":9007199254740993}` {
		t.Fatalf("schema = %s", list.Tools[0].InputSchema)
	}
	if !list.Tools[1].Annotations.IsNull() || !list.Tools[2].Annotations.IsZero() {
		t.Fatal("lost annotation presence")
	}
	decision, ok := result.Response.Output[3].(generation.OpenAIMCPApprovalResponse)
	if !ok {
		t.Fatalf("output[3] = %T; want generation.OpenAIMCPApprovalResponse", result.Response.Output[3])
	}
	if decision.Approve || decision.ApprovalRequestID != "approve_1" {
		t.Fatalf("decision = %#v", decision)
	}

	body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)

	require.NoError(t, err)
	var replay struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, replay.Input, output)
}

func TestMCPErrorsRemainItemData(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      generation.MCPCallError
	}{
		{name: "protocol", raw: `{"type":"mcp_protocol_error","code":-32601,"message":"not found"}`, want: generation.MCPProtocolError{Code: -32601, Message: "not found"}},
		{name: "http", raw: `{"type":"http_error","code":0,"message":""}`, want: generation.MCPHTTPError{Code: 0, Message: ""}},
		{name: "execution", raw: `{"type":"mcp_tool_execution_error","content":[{"value":9007199254740993}]}`, want: generation.MCPExecutionError{Content: json.RawMessage(`[{"value":9007199254740993}]`)}},
		{name: "execution null", raw: `{"type":"mcp_tool_execution_error","content":null}`, want: generation.MCPExecutionError{Content: json.RawMessage(`null`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := `[{"type":"mcp_call","id":"call_1","name":"find","server_label":"docs","arguments":"{unfinished","status":"failed","error":` + tc.raw + `}]`

			result, err := mcpResponse(output)

			require.NoError(t, err)
			if len(result.Response.Output) != 1 {
				t.Fatalf("output = %#v; want 1 items", result.Response.Output)
			}
			call, ok := result.Response.Output[0].(generation.OpenAIMCPCall)
			if !ok {
				t.Fatalf("output[0] = %T; want generation.OpenAIMCPCall", result.Response.Output[0])
			}
			got, ok := call.Error.Value()
			require.True(t, ok)
			require.Equal(t, tc.want, got)
			if result.Response.Finish.Reason != "stop" {
				t.Fatalf("hosted error finish = %s", result.Response.Finish.Reason)
			}
			if !call.Output.IsZero() {
				t.Fatal("invented output")
			}

			body, err := wire.EncodeRequest(requestWith(call), false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":`+output+`}`)
		})
	}
}

func TestMCPListFailureRemainsItemData(t *testing.T) {
	output := `[{"type":"mcp_list_tools","id":"list_1","server_label":"docs","tools":[],"error":"connection refused"}]`

	result, err := mcpResponse(output)

	require.NoError(t, err)
	if len(result.Response.Output) != 1 {
		t.Fatalf("output = %#v; want 1 items", result.Response.Output)
	}
	list, ok := result.Response.Output[0].(generation.OpenAIMCPListTools)
	if !ok {
		t.Fatalf("output[0] = %T; want generation.OpenAIMCPListTools", result.Response.Output[0])
	}
	if message, ok := list.Error.Value(); !ok || message != "connection refused" {
		t.Fatalf("list error = %q, %v", message, ok)
	}
	if result.Response.Finish.Reason != "stop" {
		t.Fatalf("list failure finish = %s", result.Response.Finish.Reason)
	}

	body, err := wire.EncodeRequest(requestWith(list), false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":`+output+`}`)
}

func TestMCPApprovalResponseInputPresence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		id, reason generation.Optional[string]
		fields     string
	}{
		{name: "omitted"},
		{name: "null", id: generation.Null[string](), reason: generation.Null[string](), fields: `,"id":null,"reason":null`},
		{name: "empty reason", reason: generation.Some(""), fields: `,"reason":""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(generation.OpenAIMCPApprovalResponse{ID: tc.id, ApprovalRequestID: "approve_1", Approve: false, Reason: tc.reason})

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"mcp_approval_response","approval_request_id":"approve_1","approve":false`+tc.fields+`}]}`)
		})
	}
}

func TestRejectMalformedMCPResponses(t *testing.T) {
	call := `{"type":"mcp_call","id":"call_1","name":"find","server_label":"docs","arguments":""}`
	list := `{"type":"mcp_list_tools","id":"list_1","server_label":"docs","tools":[{"name":"find","input_schema":{}}]}`
	approval := `{"type":"mcp_approval_response","id":"decision_1","approval_request_id":"approve_1","approve":false}`
	for _, tc := range []struct{ name, raw string }{
		{name: "missing name", raw: strings.Replace(call, `"name":"find",`, "", 1)},
		{name: "missing arguments", raw: strings.Replace(call, `,"arguments":""`, "", 1)},
		{name: "null arguments", raw: strings.Replace(call, `"arguments":""`, `"arguments":null`, 1)},
		{name: "null status", raw: strings.Replace(call, `"arguments":""`, `"arguments":"","status":null`, 1)},
		{name: "bad status", raw: strings.Replace(call, `"arguments":""`, `"arguments":"","status":"waiting"`, 1)},
		{name: "missing tools", raw: strings.Replace(list, `,"tools":[{"name":"find","input_schema":{}}]`, "", 1)},
		{name: "null tools", raw: strings.Replace(list, `[{"name":"find","input_schema":{}}]`, `null`, 1)},
		{name: "missing schema", raw: strings.Replace(list, `,"input_schema":{}`, "", 1)},
		{name: "null tool", raw: strings.Replace(list, `[{"name":"find","input_schema":{}}]`, `[null]`, 1)},
		{name: "duplicate tool names", raw: strings.Replace(list, `[{"name":"find","input_schema":{}}]`, `[{"name":"find","input_schema":{}},{"name":"find","input_schema":false}]`, 1)},
		{name: "missing approval boolean", raw: strings.Replace(approval, `,"approve":false`, "", 1)},
		{name: "null approval boolean", raw: strings.Replace(approval, `"approve":false`, `"approve":null`, 1)},
		{name: "missing returned decision ID", raw: strings.Replace(approval, `"id":"decision_1",`, "", 1)},
		{name: "null returned decision ID", raw: strings.Replace(approval, `"id":"decision_1"`, `"id":null`, 1)},
		{name: "protocol missing code", raw: strings.Replace(call, `"arguments":""`, `"arguments":"","error":{"type":"mcp_protocol_error","message":"private"}`, 1)},
		{name: "http null message", raw: strings.Replace(call, `"arguments":""`, `"arguments":"","error":{"type":"http_error","code":500,"message":null}`, 1)},
		{name: "execution missing content", raw: strings.Replace(call, `"arguments":""`, `"arguments":"","error":{"type":"mcp_tool_execution_error"}`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mcpResponse("[" + tc.raw + "]")

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked content")
			}
		})
	}
}

func TestRejectInvalidMCPInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		item generation.Item
		tool generation.OpenAIMCPTool
	}{
		{name: "missing endpoint", tool: generation.OpenAIMCPTool{ServerLabel: "docs"}},
		{name: "invalid endpoint", tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPServerURL("file:///private")}},
		{name: "invalid connector", tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPConnectorID("unknown")}},
		{name: "null authorization", tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPTunnelID("t"), Authorization: generation.Null[string]()}},
		{
			name: "header injection",
			tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPTunnelID("t"), Headers: generation.Some(map[string]string{"X-Token": "private\r\nInjected: value"})},
		},
		{name: "invalid header name", tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPTunnelID("t"), Headers: generation.Some(map[string]string{"private:bad": "secret"})}},
		{name: "null defer", tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPTunnelID("t"), DeferLoading: generation.Null[bool]()}},
		{name: "nil allowed tools", tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPTunnelID("t"), AllowedTools: generation.Some[generation.MCPAllowedTools](nil)}},
		{
			name: "null read only",
			tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPTunnelID("t"), AllowedTools: generation.Some[generation.MCPAllowedTools](generation.MCPToolFilter{ReadOnly: generation.Null[bool]()})},
		},
		{
			name: "null approval filter",
			tool: generation.OpenAIMCPTool{
				ServerLabel:     "docs",
				Endpoint:        generation.MCPTunnelID("t"),
				RequireApproval: generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalFilter{Always: generation.Null[generation.MCPToolFilter]()}),
			},
		},
		{
			name: "invalid approval mode",
			tool: generation.OpenAIMCPTool{ServerLabel: "docs", Endpoint: generation.MCPTunnelID("t"), RequireApproval: generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalMode("automatic"))},
		},
		{name: "nil error", item: generation.OpenAIMCPCall{ID: "call_1", Name: "find", ServerLabel: "docs", Error: generation.Some[generation.MCPCallError](nil)}},
		{
			name: "invalid raw error",
			item: generation.OpenAIMCPCall{ID: "call_1", Name: "find", ServerLabel: "docs", Error: generation.Some[generation.MCPCallError](generation.MCPExecutionError{Content: json.RawMessage(`private`)})},
		},
		{name: "missing schema", item: generation.OpenAIMCPListTools{ID: "list_1", ServerLabel: "docs", Tools: []generation.MCPListedTool{{Name: "find"}}}},
		{
			name: "duplicate tools",
			item: generation.OpenAIMCPListTools{ID: "list_1", ServerLabel: "docs", Tools: []generation.MCPListedTool{{Name: "find", InputSchema: json.RawMessage(`{}`)}, {Name: "find", InputSchema: json.RawMessage(`true`)}}},
		},
		{name: "invalid arguments UTF8", item: generation.OpenAIMCPApprovalRequest{ID: "approve_1", Name: "write", ServerLabel: "docs", Arguments: "private\xff"}},
		{name: "missing approval link", item: generation.OpenAIMCPApprovalResponse{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			if tc.item != nil {
				request.Input = []generation.Item{tc.item}
			} else {
				request.Tools = []generation.Tool{tc.tool}
			}

			body, err := wire.EncodeRequest(request, false)

			var inputError *wire.InputError
			if body != nil || !errors.As(err, &inputError) {
				t.Fatalf("EncodeRequest = %s, %v; want input error", body, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked content")
			}
		})
	}
}

func mcpResponse(output string) (generation.Result, error) {
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":` + output + `}`))
	if err != nil {
		return generation.Result{}, err
	}
	return envelope.Result(func(string) {})
}
