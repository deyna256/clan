package wire_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeNamespaceAndFunctionOptions(t *testing.T) {
	request := requestWith()
	request.Tools = []generation.Tool{
		generation.FunctionTool{Name: "lookup", Parameters: json.RawMessage(`null`), Strict: generation.Null[bool](), Description: generation.Null[string](), OpenAI: generation.OpenAIFunctionToolOptions{
			Async: generation.Some(false), DeferLoading: generation.Some(true), AllowedCallers: generation.Some([]string{"direct", "programmatic"}), OutputSchema: json.RawMessage(`{"const":9007199254740993}`),
		}},
		generation.OpenAINamespaceTool{Name: "crm", Description: "Customer tools", Tools: []generation.Tool{
			generation.FunctionTool{Name: "omitted"},
			generation.FunctionTool{
				Name:       "boolean",
				Parameters: json.RawMessage(`false`),
				Strict:     generation.Some(false),
				OpenAI:     generation.OpenAIFunctionToolOptions{AllowedCallers: generation.Some([]string{}), OutputSchema: json.RawMessage(`null`)},
			},
			generation.FunctionTool{Name: "null", Parameters: json.RawMessage(`null`), OpenAI: generation.OpenAIFunctionToolOptions{AllowedCallers: generation.Null[[]string]()}},
			generation.CustomTool{Name: "query", Format: generation.CustomGrammarFormat{Syntax: "regex", Definition: ".*"}},
		}},
	}
	request.ToolChoice = generation.NamedTool{Name: "lookup"}

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tools":[
		{"type":"function","name":"lookup","parameters":null,"strict":null,"description":null,"async":false,"defer_loading":true,"allowed_callers":["direct","programmatic"],"output_schema":{"const":9007199254740993}},
		{"type":"namespace","name":"crm","description":"Customer tools","tools":[
			{"type":"function","name":"omitted"},
			{"type":"function","name":"boolean","parameters":false,"strict":false,"allowed_callers":[],"output_schema":null},
			{"type":"function","name":"null","parameters":null,"allowed_callers":null},
			{"type":"custom","name":"query","format":{"type":"grammar","syntax":"regex","definition":".*"}}
		]}
	],"tool_choice":{"type":"function","name":"lookup"}}`)
	if !strings.Contains(string(body), `"const":9007199254740993`) {
		t.Fatal("output schema lost integer precision")
	}
}

func TestEncodeEmptyNamespace(t *testing.T) {
	request := requestWith()
	request.Tools = []generation.Tool{generation.OpenAINamespaceTool{Name: "empty"}}

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tools":[{"type":"namespace","name":"empty","description":"","tools":[]}]}`)
}

func TestRejectInvalidNamespaceAndFunctionOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool generation.Tool
	}{
		{name: "namespace name", tool: generation.OpenAINamespaceTool{}},
		{name: "namespace description", tool: generation.OpenAINamespaceTool{Name: "crm", Description: "private\xff"}},
		{name: "hosted child", tool: generation.OpenAINamespaceTool{Name: "crm", Tools: []generation.Tool{generation.OpenAIComputerTool{}}}},
		{name: "nested namespace", tool: generation.OpenAINamespaceTool{Name: "crm", Tools: []generation.Tool{generation.OpenAINamespaceTool{Name: "inner"}}}},
		{name: "nil child", tool: generation.OpenAINamespaceTool{Name: "crm", Tools: []generation.Tool{nil}}},
		{name: "malformed nested parameters", tool: generation.OpenAINamespaceTool{Name: "crm", Tools: []generation.Tool{generation.FunctionTool{Name: "f", Parameters: json.RawMessage(`{"private":`)}}}},
		{name: "top level boolean parameters", tool: generation.FunctionTool{Name: "f", Parameters: json.RawMessage(`false`)}},
		{name: "null async", tool: generation.FunctionTool{Name: "f", Parameters: json.RawMessage(`{}`), OpenAI: generation.OpenAIFunctionToolOptions{Async: generation.Null[bool]()}}},
		{name: "null deferred", tool: generation.FunctionTool{Name: "f", Parameters: json.RawMessage(`{}`), OpenAI: generation.OpenAIFunctionToolOptions{DeferLoading: generation.Null[bool]()}}},
		{name: "caller enum", tool: generation.FunctionTool{Name: "f", Parameters: json.RawMessage(`{}`), OpenAI: generation.OpenAIFunctionToolOptions{AllowedCallers: generation.Some([]string{"private"})}}},
		{name: "nonobject output schema", tool: generation.FunctionTool{Name: "f", Parameters: json.RawMessage(`{}`), OpenAI: generation.OpenAIFunctionToolOptions{OutputSchema: json.RawMessage(`false`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}

			body, err := wire.EncodeRequest(request, false)

			assertInputError(t, body, err)
		})
	}
}
