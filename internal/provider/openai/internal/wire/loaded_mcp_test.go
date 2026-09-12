package wire_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestLoadedMCPDeclarationsAndReplay(t *testing.T) {
	catalog := `[
		{"type":"mcp","server_label":"crm","server_url":"https://example.com/mcp","authorization":"secret_token","server_description":"Customer tools","headers":{"X-Test":""},"allowed_callers":["direct","programmatic"],"defer_loading":false,"allowed_tools":{"read_only":false,"tool_names":[]},"require_approval":{"always":{"tool_names":["write"]},"never":{"read_only":true}}},
		{"type":"mcp","server_label":"mail","connector_id":"connector_gmail","headers":null,"allowed_callers":null,"allowed_tools":null,"require_approval":null},
		{"type":"mcp","server_label":"local","tunnel_id":"tunnel_1","headers":{},"allowed_tools":["read"],"require_approval":"never"}
	]`
	want := []generation.Tool{
		generation.OpenAIMCPTool{ServerLabel: "crm", Endpoint: generation.MCPServerURL("https://example.com/mcp"), Authorization: generation.Some("secret_token"), ServerDescription: generation.Some("Customer tools"), Headers: generation.Some(map[string]string{"X-Test": ""}), AllowedCallers: generation.Some([]string{"direct", "programmatic"}), DeferLoading: generation.Some(false), AllowedTools: generation.Some[generation.MCPAllowedTools](generation.MCPToolFilter{ReadOnly: generation.Some(false), ToolNames: []string{}}), RequireApproval: generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalFilter{Always: generation.Some(generation.MCPToolFilter{ToolNames: []string{"write"}}), Never: generation.Some(generation.MCPToolFilter{ReadOnly: generation.Some(true)})})},
		generation.OpenAIMCPTool{ServerLabel: "mail", Endpoint: generation.MCPConnectorID("connector_gmail"), Headers: generation.Null[map[string]string](), AllowedCallers: generation.Null[[]string](), AllowedTools: generation.Null[generation.MCPAllowedTools](), RequireApproval: generation.Null[generation.MCPApprovalPolicy]()},
		generation.OpenAIMCPTool{ServerLabel: "local", Endpoint: generation.MCPTunnelID("tunnel_1"), Headers: generation.Some(map[string]string{}), AllowedTools: generation.Some[generation.MCPAllowedTools](generation.MCPToolNames{"read"}), RequireApproval: generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalMode("never"))},
	}
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"tool_search_output","id":"search_1","call_id":null,"execution":"server","status":"completed","tools":` + catalog + `}]}`))
	if err != nil {
		t.Fatal(err)
	}

	result, err := envelope.Result(nil)

	if err != nil {
		t.Fatal(err)
	}
	loaded := result.Response.Output[0].(generation.OpenAIToolSearchOutput)
	if !reflect.DeepEqual(loaded.Tools, want) {
		t.Fatalf("loaded tools = %#v; want %#v", loaded.Tools, want)
	}

	body, err := wire.EncodeRequest(requestWith(loaded), false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"tool_search_output","id":"search_1","call_id":null,"execution":"server","status":"completed","tools":`+catalog+`}]}`)
}

func TestRejectMalformedLoadedMCPDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields string
	}{
		{name: "missing endpoint"},
		{name: "null endpoint", fields: `,"server_url":null`},
		{name: "ambiguous endpoint", fields: `,"server_url":"https://example.com","connector_id":"connector_gmail"`},
		{name: "invalid URL", fields: `,"server_url":"private"`},
		{name: "null authorization", fields: `,"tunnel_id":"t","authorization":null`},
		{name: "null header entry", fields: `,"tunnel_id":"t","headers":{"X-Test":null}`},
		{name: "header injection", fields: `,"tunnel_id":"t","headers":{"X-Test":"private\r\n"}`},
		{name: "null allowed name", fields: `,"tunnel_id":"t","allowed_tools":[null]`},
		{name: "null filter names", fields: `,"tunnel_id":"t","allowed_tools":{"tool_names":null}`},
		{name: "null read only", fields: `,"tunnel_id":"t","allowed_tools":{"read_only":null}`},
		{name: "null approval filter", fields: `,"tunnel_id":"t","require_approval":{"always":null}`},
		{name: "approval enum", fields: `,"tunnel_id":"t","require_approval":"private"`},
		{name: "null deferred", fields: `,"tunnel_id":"t","defer_loading":null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"tool_search_output","id":"search_1","call_id":null,"execution":"server","status":"completed","tools":[{"type":"mcp","server_label":"crm"` + tc.fields + `}]}]}`))
			if err != nil {
				t.Fatal(err)
			}

			_, err = envelope.Result(nil)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("Result error = %v; want protocol error", err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked MCP configuration")
			}
		})
	}
}
