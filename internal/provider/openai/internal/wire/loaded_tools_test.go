package wire_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestLoadedToolDeclarationsAndReplay(t *testing.T) {
	catalog := `[
		{"type":"function","name":"lookup","parameters":{"const":9007199254740993},"description":null,"async":false,"defer_loading":true,"allowed_callers":null,"output_schema":{"const":9007199254740995}},
		{"type":"custom","name":"text","format":{"type":"text"},"allowed_callers":[]},
		{"type":"namespace","name":"crm","description":"","tools":[
			{"type":"function","name":"unset"},
			{"type":"function","name":"null","parameters":null,"strict":null},
			{"type":"function","name":"boolean","parameters":false,"strict":false},
			{"type":"custom","name":"grammar","description":"Parse text","format":{"type":"grammar","syntax":"regex","definition":""},"async":false,"defer_loading":false,"allowed_callers":null}
		]},
		{"type":"tool_search","description":null,"execution":"client","parameters":[9007199254740993]}
	]`
	want := []generation.Tool{
		generation.FunctionTool{
			Name:        "lookup",
			Parameters:  json.RawMessage(`{"const":9007199254740993}`),
			Description: generation.Null[string](),
			OpenAI:      generation.OpenAIFunctionToolOptions{Async: generation.Some(false), DeferLoading: generation.Some(true), AllowedCallers: generation.Null[[]string](), OutputSchema: json.RawMessage(`{"const":9007199254740995}`)},
		},
		generation.CustomTool{Name: "text", Format: generation.CustomTextFormat{}, OpenAI: generation.OpenAICustomToolOptions{AllowedCallers: generation.Some([]string{})}},
		generation.OpenAINamespaceTool{Name: "crm", Tools: []generation.Tool{
			generation.FunctionTool{Name: "unset"},
			generation.FunctionTool{Name: "null", Parameters: json.RawMessage(`null`), Strict: generation.Null[bool]()},
			generation.FunctionTool{Name: "boolean", Parameters: json.RawMessage(`false`), Strict: generation.Some(false)},
			generation.CustomTool{
				Name:        "grammar",
				Description: generation.Some("Parse text"),
				Format:      generation.CustomGrammarFormat{Syntax: "regex"},
				OpenAI:      generation.OpenAICustomToolOptions{Async: generation.Some(false), DeferLoading: generation.Some(false), AllowedCallers: generation.Null[[]string]()},
			},
		}},
		generation.OpenAIToolSearchTool{Description: generation.Null[string](), Execution: generation.Some("client"), Parameters: json.RawMessage(`[9007199254740993]`)},
	}
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"tool_search_output","id":"search_1","call_id":null,"execution":"server","status":"completed","tools":` + catalog + `}]}`))
	if err != nil {
		t.Fatal(err)
	}

	result, err := envelope.Result(nil)

	if err != nil {
		t.Fatal(err)
	}
	if len(result.Response.Output) != 1 {
		t.Fatalf("output = %#v; want 1 items", result.Response.Output)
	}
	loaded, ok := result.Response.Output[0].(generation.OpenAIToolSearchOutput)
	if !ok {
		t.Fatalf("output[0] = %T; want generation.OpenAIToolSearchOutput", result.Response.Output[0])
	}
	if !reflect.DeepEqual(loaded.Tools, want) {
		t.Fatalf("loaded tools = %#v; want %#v", loaded.Tools, want)
	}

	body, err := wire.EncodeRequest(requestWith(loaded), false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"tool_search_output","id":"search_1","call_id":null,"execution":"server","status":"completed","tools":`+catalog+`}]}`)
	if !strings.Contains(string(body), `"const":9007199254740995`) || !strings.Contains(string(body), `"const":9007199254740993`) {
		t.Fatal("loaded schemas lost integer precision on replay")
	}
}

func TestRejectMalformedLoadedToolDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		kind generation.FailureKind
	}{
		{name: "null", raw: `null`, kind: generation.ProtocolError},
		{name: "unknown variant", raw: `{"type":"future_private"}`, kind: generation.Unsupported},
		{name: "function missing parameters", raw: `{"type":"function","name":"f"}`, kind: generation.ProtocolError},
		{name: "function nonobject parameters", raw: `{"type":"function","name":"f","parameters":false}`, kind: generation.ProtocolError},
		{name: "function null async", raw: `{"type":"function","name":"f","parameters":null,"async":null}`, kind: generation.ProtocolError},
		{name: "function null caller entry", raw: `{"type":"function","name":"f","parameters":{},"allowed_callers":[null]}`, kind: generation.ProtocolError},
		{name: "function scalar output schema", raw: `{"type":"function","name":"f","parameters":{},"output_schema":false}`, kind: generation.ProtocolError},
		{name: "custom null description", raw: `{"type":"custom","name":"f","description":null}`, kind: generation.ProtocolError},
		{name: "custom null format", raw: `{"type":"custom","name":"f","format":null}`, kind: generation.ProtocolError},
		{name: "custom missing grammar", raw: `{"type":"custom","name":"f","format":{"type":"grammar","syntax":"regex"}}`, kind: generation.ProtocolError},
		{name: "custom null caller entry", raw: `{"type":"custom","name":"f","allowed_callers":[null]}`, kind: generation.ProtocolError},
		{name: "namespace missing description", raw: `{"type":"namespace","name":"n","tools":[]}`, kind: generation.ProtocolError},
		{name: "namespace null tools", raw: `{"type":"namespace","name":"n","description":"","tools":null}`, kind: generation.ProtocolError},
		{name: "namespace nested hosted", raw: `{"type":"namespace","name":"n","description":"","tools":[{"type":"computer"}]}`, kind: generation.ProtocolError},
		{name: "search null execution", raw: `{"type":"tool_search","execution":null}`, kind: generation.ProtocolError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"tool_search_output","id":"search_1","call_id":null,"execution":"server","status":"completed","tools":[` + tc.raw + `]}]}`))
			if err != nil {
				t.Fatal(err)
			}

			_, err = envelope.Result(nil)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != tc.kind {
				t.Fatalf("Result error = %v; want %s", err, tc.kind)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked tool definition")
			}
		})
	}
}
