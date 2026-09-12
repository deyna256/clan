package wire_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestEncodeInterpreterTool(t *testing.T) {
	request := interpreterRequest()
	unchanged := interpreterRequest()

	body, err := wire.EncodeRequest(request, true)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":true,"tools":[{"type":"code_interpreter","container":{"type":"auto","file_ids":["file_1"],"memory_limit":"4g","network_policy":{"type":"allowlist","allowed_domains":["example.com"],"domain_secrets":[{"domain":"example.com","name":"token","value":"secret-value"}]}},"allowed_callers":["direct","programmatic"]}],"tool_choice":{"type":"code_interpreter"}}`)
	if !reflect.DeepEqual(request, unchanged) {
		t.Fatal("encoding changed interpreter configuration")
	}
}

func TestEncodeInterpreterContainerModes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		container generation.InterpreterContainer
		want      string
	}{
		{"existing", generation.InterpreterContainerID("cntr_1"), `"cntr_1"`},
		{"auto", generation.InterpreterAutoContainer{}, `{"type":"auto"}`},
		{"disabled network", generation.InterpreterAutoContainer{NetworkPolicy: generation.InterpreterNetworkDisabled{}}, `{"type":"auto","network_policy":{"type":"disabled"}}`},
		{"empty allowlist", generation.InterpreterAutoContainer{NetworkPolicy: generation.InterpreterNetworkAllowlist{}}, `{"type":"auto","network_policy":{"type":"allowlist","allowed_domains":[]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{generation.OpenAICodeInterpreterTool{Container: tc.container}}

			body, err := wire.EncodeRequest(request, false)

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tools":[{"type":"code_interpreter","container":`+tc.want+`}]}`)
		})
	}
}

func TestEncodeInterpreterHistory(t *testing.T) {
	request := requestWith(
		generation.OpenAICodeInterpreterCall{ID: "ci_1", ContainerID: "cntr_1", Status: "completed", Code: generation.Some("print(1)"), Outputs: generation.Some([]generation.InterpreterOutput{generation.InterpreterLogs{Logs: "1\n"}, generation.InterpreterImage{URL: "https://example.com/plot.png"}})},
		generation.OpenAICodeInterpreterCall{ID: "ci_2", ContainerID: "cntr_1", Status: "failed", Code: generation.Null[string](), Outputs: generation.Null[[]generation.InterpreterOutput]()},
		generation.OpenAICodeInterpreterCall{ID: "ci_3", ContainerID: "cntr_1", Status: "incomplete", Code: generation.Some(""), Outputs: generation.Some([]generation.InterpreterOutput{})},
	)

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"code_interpreter_call","id":"ci_1","container_id":"cntr_1","status":"completed","code":"print(1)","outputs":[{"type":"logs","logs":"1\n"},{"type":"image","url":"https://example.com/plot.png"}]},
		{"type":"code_interpreter_call","id":"ci_2","container_id":"cntr_1","status":"failed","code":null,"outputs":null},
		{"type":"code_interpreter_call","id":"ci_3","container_id":"cntr_1","status":"incomplete","code":"","outputs":[]}
	]}`)
}

func TestEncodeInterpreterExplicitEmptyLists(t *testing.T) {
	request := requestWith()
	request.Tools = []generation.Tool{generation.OpenAICodeInterpreterTool{
		Container:      generation.InterpreterAutoContainer{FileIDs: []string{}, NetworkPolicy: generation.InterpreterNetworkAllowlist{Secrets: []generation.InterpreterDomainSecret{}}},
		AllowedCallers: generation.Some([]string{}),
	}}

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tools":[{"type":"code_interpreter","container":{"type":"auto","file_ids":[],"network_policy":{"type":"allowlist","allowed_domains":[],"domain_secrets":[]}},"allowed_callers":[]}]}`)
}

func TestRejectInvalidInterpreterConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool generation.OpenAICodeInterpreterTool
	}{
		{"missing container", generation.OpenAICodeInterpreterTool{}},
		{"empty container ID", generation.OpenAICodeInterpreterTool{Container: generation.InterpreterContainerID(" ")}},
		{"memory", generation.OpenAICodeInterpreterTool{Container: generation.InterpreterAutoContainer{MemoryLimit: generation.Some("2g")}}},
		{"file ID", generation.OpenAICodeInterpreterTool{Container: generation.InterpreterAutoContainer{FileIDs: []string{""}}}},
		{"caller", generation.OpenAICodeInterpreterTool{Container: generation.InterpreterAutoContainer{}, AllowedCallers: generation.Some([]string{"unknown"})}},
		{"secret UTF-8", generation.OpenAICodeInterpreterTool{Container: generation.InterpreterAutoContainer{NetworkPolicy: generation.InterpreterNetworkAllowlist{Domains: []string{"example.com"}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.com", Name: "token", Value: "secret-value\xff"}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}

			body, err := wire.EncodeRequest(request, false)

			var inputError *wire.InputError
			if body != nil || !errors.As(err, &inputError) {
				t.Fatalf("EncodeRequest = %s, %v", body, err)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("error leaked secret")
			}
		})
	}
}

func TestRejectInvalidInterpreterHistory(t *testing.T) {
	for _, tc := range []struct {
		name, status, code string
		outputs            []generation.InterpreterOutput
	}{
		{name: "status", status: "unknown"},
		{name: "code UTF-8", code: "private\xff"},
		{name: "logs UTF-8", outputs: []generation.InterpreterOutput{generation.InterpreterLogs{Logs: "private\xff"}}},
		{name: "empty image URL", outputs: []generation.InterpreterOutput{generation.InterpreterImage{}}},
		{name: "nil output", outputs: []generation.InterpreterOutput{nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			if status == "" {
				status = "completed"
			}
			request := requestWith(generation.OpenAICodeInterpreterCall{ID: "ci_1", ContainerID: "cntr_1", Status: status, Code: generation.Some(tc.code), Outputs: generation.Some(tc.outputs)})

			body, err := wire.EncodeRequest(request, false)

			var inputError *wire.InputError
			if body != nil || !errors.As(err, &inputError) {
				t.Fatalf("EncodeRequest = %s, %v", body, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked interpreter content")
			}
		})
	}
}

func interpreterRequest() generation.Request {
	request := requestWith()
	request.Tools = []generation.Tool{generation.OpenAICodeInterpreterTool{
		Container:      generation.InterpreterAutoContainer{FileIDs: []string{"file_1"}, MemoryLimit: generation.Some("4g"), NetworkPolicy: generation.InterpreterNetworkAllowlist{Domains: []string{"example.com"}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.com", Name: "token", Value: "secret-value"}}}},
		AllowedCallers: generation.Some([]string{"direct", "programmatic"}),
	}}
	request.ToolChoice = generation.OpenAICodeInterpreterChoice{}
	return request
}
