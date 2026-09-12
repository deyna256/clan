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

func TestEncodeShellTools(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tool       generation.Tool
		choice     generation.ToolChoice
		want       string
		wantChoice string
	}{
		{name: "omitted", tool: generation.OpenAIShellTool{}, choice: generation.OpenAIShellChoice{}, wantChoice: `{"type":"shell"}`, want: `{"type":"shell"}`},
		{
			name:   "null",
			tool:   generation.OpenAIShellTool{Environment: generation.Null[generation.ShellEnvironment](), AllowedCallers: generation.Null[[]string]()},
			choice: generation.OpenAIShellChoice{}, wantChoice: `{"type":"shell"}`,
			want: `{"type":"shell","environment":null,"allowed_callers":null}`,
		},
		{
			name: "local skills",
			tool: generation.OpenAIShellTool{
				Environment:    generation.Some[generation.ShellEnvironment](generation.ShellLocalEnvironment{Skills: []generation.ShellLocalSkill{{Name: "review", Description: "Read source", Path: "../skills/review"}}}),
				AllowedCallers: generation.Some([]string{"direct", "programmatic"}),
			},
			choice: generation.OpenAIShellChoice{}, wantChoice: `{"type":"shell"}`,
			want: `{"type":"shell","allowed_callers":["direct","programmatic"],"environment":{"type":"local","skills":[{"name":"review","description":"Read source","path":"../skills/review"}]}}`,
		},
		{
			name:   "reference",
			tool:   generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellContainerReference{ContainerID: "cntr_1"})},
			choice: generation.OpenAIShellChoice{}, wantChoice: `{"type":"shell"}`,
			want: `{"type":"shell","environment":{"type":"container_reference","container_id":"cntr_1"}}`,
		},
		{
			name: "auto",
			tool: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{
				FileIDs:       []string{"file_1"},
				MemoryLimit:   generation.Null[string](),
				NetworkPolicy: generation.InterpreterNetworkAllowlist{Domains: []string{"example.org"}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.org", Name: "TOKEN", Value: "secret"}}},
				Skills:        []generation.ShellSkill{generation.ShellSkillReference{SkillID: "skill_1", Version: generation.Some("latest")}, generation.ShellInlineSkill{Name: "test", Description: "test skill", Data: "UEs="}},
			})},
			choice: generation.OpenAIShellChoice{}, wantChoice: `{"type":"shell"}`,
			want: `{"type":"shell","environment":{"type":"container_auto","file_ids":["file_1"],"memory_limit":null,"network_policy":{"type":"allowlist","allowed_domains":["example.org"],"domain_secrets":[{"domain":"example.org","name":"TOKEN","value":"secret"}]},"skills":[{"type":"skill_reference","skill_id":"skill_1","version":"latest"},{"type":"inline","name":"test","description":"test skill","source":{"type":"base64","media_type":"application/zip","data":"UEs="}}]}}`,
		},
		{name: "legacy", tool: generation.OpenAILocalShellTool{}, choice: generation.OpenAILocalShellChoice{}, wantChoice: `{"type":"local_shell"}`, want: `{"type":"local_shell"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}
			request.ToolChoice = tc.choice
			body, err := wire.EncodeRequest(request, false)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools  []json.RawMessage `json:"tools"`
				Choice json.RawMessage   `json:"tool_choice"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Tools) != 1 {
				t.Fatalf("tools = %s; want exactly one tool", got.Tools)
			}
			assertJSON(t, got.Tools[0], tc.want)
			assertJSON(t, got.Choice, tc.wantChoice)
		})
	}
}

func TestEncodeShellInputPresence(t *testing.T) {
	request := requestWith(
		generation.OpenAIShellCall{
			CallID:      "call_1",
			Action:      generation.ShellAction{Commands: []string{"echo x", "echo y"}, TimeoutMs: generation.Null[int64](), MaxOutputLength: generation.Some(int64(0))},
			ID:          generation.Null[string](),
			Status:      generation.Null[string](),
			Environment: generation.Some[generation.ShellEnvironment](generation.ShellLocalEnvironment{Skills: []generation.ShellLocalSkill{}}),
			Caller:      generation.Null[generation.OpenAIToolCaller](),
		},
		generation.OpenAIShellResult{
			CallID:          "call_1",
			MaxOutputLength: generation.Null[int64](),
			Output:          []generation.ShellOutput{{Stdout: "", Stderr: "", Outcome: generation.ShellExit{ExitCode: 0}}, {Stdout: "partial", Stderr: "timed out", Outcome: generation.ShellTimeout{}}},
		},
		generation.OpenAILocalShellCall{
			ID:     "ls_1",
			CallID: "local_call",
			Status: "incomplete",
			Action: generation.LocalShellAction{Command: []string{"bash", "-lc", "echo x"}, Env: map[string]string{"X": ""}, TimeoutMs: generation.Null[int64](), User: generation.Some(""), WorkingDirectory: generation.Null[string]()},
		},
		generation.OpenAILocalShellResult{ID: "local_call", Output: "plain output", Status: generation.Null[string]()},
	)
	body, err := wire.EncodeRequest(request, false)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
	{"type":"shell_call","id":null,"call_id":"call_1","status":null,"action":{"commands":["echo x","echo y"],"timeout_ms":null,"max_output_length":0},"environment":{"type":"local","skills":[]},"caller":null},
	{"type":"shell_call_output","call_id":"call_1","max_output_length":null,"output":[{"stdout":"","stderr":"","outcome":{"type":"exit","exit_code":0}},{"stdout":"partial","stderr":"timed out","outcome":{"type":"timeout"}}]},
	{"type":"local_shell_call","id":"ls_1","call_id":"local_call","status":"incomplete","action":{"type":"exec","command":["bash","-lc","echo x"],"env":{"X":""},"timeout_ms":null,"user":"","working_directory":null}},
	{"type":"local_shell_call_output","id":"local_call","output":"plain output","status":null}
	]}`)
}

func TestDecodeShellResponseAndReplay(t *testing.T) {
	output := `[
	{"type":"shell_call","id":"sh_1","call_id":"call_1","status":"completed","action":{"commands":["echo привет"],"timeout_ms":null,"max_output_length":0},"environment":{"type":"container_reference","container_id":"cntr_1"},"caller":{"type":"program","caller_id":"program_1"},"created_by":"actor"},
	{"type":"shell_call_output","id":"out_1","call_id":"call_1","status":"completed","max_output_length":null,"output":[{"stdout":"привет","stderr":"","outcome":{"type":"exit","exit_code":0},"created_by":"actor"}],"caller":null,"created_by":"actor"}
	]`
	result, err := shellResponse(output)
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Finish.Reason != "stop" {
		t.Fatalf("hosted finish = %s", result.Response.Finish.Reason)
	}
	if len(result.Response.Output) != 2 {
		t.Fatalf("output = %#v; want 2 items", result.Response.Output)
	}
	call, ok := result.Response.Output[0].(generation.OpenAIShellCall)
	if !ok {
		t.Fatalf("output[0] = %T; want generation.OpenAIShellCall", result.Response.Output[0])
	}
	if !call.Action.TimeoutMs.IsNull() {
		t.Fatal("lost null timeout")
	}
	if id, _ := call.ID.Value(); id != "sh_1" {
		t.Fatalf("ID = %q", id)
	}
	got, ok := result.Response.Output[1].(generation.OpenAIShellResult)
	if !ok {
		t.Fatalf("output[1] = %T; want generation.OpenAIShellResult", result.Response.Output[1])
	}
	if actor, _ := call.CreatedBy.Value(); actor != "actor" {
		t.Fatalf("call creator = %q", actor)
	}
	if actor, _ := got.CreatedBy.Value(); actor != "actor" {
		t.Fatalf("result creator = %q", actor)
	}
	if len(got.Output) != 1 {
		t.Fatalf("shell chunks = %#v; want one chunk", got.Output)
	}
	if actor, _ := got.Output[0].CreatedBy.Value(); actor != "actor" {
		t.Fatalf("chunk creator = %q", actor)
	}
	if !got.Caller.IsNull() || !got.MaxOutputLength.IsNull() {
		t.Fatal("lost null output metadata")
	}
	if !reflect.DeepEqual(got.Output[0].Outcome, generation.ShellExit{ExitCode: 0}) {
		t.Fatalf("outcome = %#v", got.Output[0].Outcome)
	}
	body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)
	if err != nil {
		t.Fatal(err)
	}
	var replay struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, replay.Input, `[
	{"type":"shell_call","id":"sh_1","call_id":"call_1","status":"completed","action":{"commands":["echo привет"],"timeout_ms":null,"max_output_length":0},"environment":{"type":"container_reference","container_id":"cntr_1"},"caller":{"type":"program","caller_id":"program_1"}},
	{"type":"shell_call_output","id":"out_1","call_id":"call_1","status":"completed","max_output_length":null,"output":[{"stdout":"привет","stderr":"","outcome":{"type":"exit","exit_code":0}}],"caller":null}
	]`)
}

func TestDecodeShellClientHandoffs(t *testing.T) {
	nullEnvironment := shellHandoffCall()
	nullEnvironment.Environment = generation.Null[generation.ShellEnvironment]()
	localEnvironment := shellHandoffCall()
	localEnvironment.Environment = generation.Some[generation.ShellEnvironment](generation.ShellLocalEnvironment{})
	for _, tc := range []struct {
		name, raw string
		want      []generation.Item
	}{
		{
			name: "null environment",
			raw:  `[{"type":"shell_call","id":"sh_1","call_id":"call_1","status":"in_progress","action":{"commands":[],"timeout_ms":null,"max_output_length":null},"environment":null}]`,
			want: []generation.Item{nullEnvironment},
		},
		{
			name: "local environment",
			raw:  `[{"type":"shell_call","id":"sh_1","call_id":"call_1","status":"in_progress","action":{"commands":[],"timeout_ms":null,"max_output_length":null},"environment":{"type":"local"}}]`,
			want: []generation.Item{localEnvironment},
		},
		{
			name: "legacy local shell",
			raw:  `[{"type":"local_shell_call","id":"ls_1","call_id":"call_1","status":"in_progress","action":{"type":"exec","command":[],"env":{},"timeout_ms":null}},{"type":"local_shell_call_output","id":"call_1","output":"not JSON","status":null}]`,
			want: []generation.Item{
				generation.OpenAILocalShellCall{
					ID: "ls_1", CallID: "call_1", Status: "in_progress",
					Action: generation.LocalShellAction{Command: []string{}, Env: map[string]string{}, TimeoutMs: generation.Null[int64]()},
				},
				generation.OpenAILocalShellResult{ID: "call_1", Output: "not JSON", Status: generation.Null[string]()},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := shellResponse(tc.raw)

			if err != nil || !reflect.DeepEqual(result.Response.Output, tc.want) {
				t.Fatalf("output = %#v, %v; want %#v", result.Response.Output, err, tc.want)
			}
			if result.Response.Finish.Reason != "tool_calls" {
				t.Fatalf("finish = %s; want tool_calls", result.Response.Finish.Reason)
			}

			body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":`+tc.raw+`}`)
		})
	}
}

func shellHandoffCall() generation.OpenAIShellCall {
	return generation.OpenAIShellCall{
		ID: generation.Some("sh_1"), CallID: "call_1", Status: generation.Some("in_progress"),
		Action: generation.ShellAction{Commands: []string{}, TimeoutMs: generation.Null[int64](), MaxOutputLength: generation.Null[int64]()},
	}
}

func TestRejectMalformedShellResponses(t *testing.T) {
	call := `{"type":"shell_call","id":"sh_1","call_id":"call_1","status":"completed","action":{"commands":["x"],"timeout_ms":null,"max_output_length":null},"environment":null}`
	output := `{"type":"shell_call_output","id":"out_1","call_id":"call_1","status":"completed","max_output_length":null,"output":[{"stdout":"","stderr":"","outcome":{"type":"exit","exit_code":0}}]}`
	local := `{"type":"local_shell_call","id":"ls_1","call_id":"call_1","status":"completed","action":{"type":"exec","command":["x"],"env":{}}}`
	for _, tc := range []struct{ name, raw string }{
		{name: "missing id", raw: strings.Replace(call, `"id":"sh_1",`, "", 1)},
		{name: "null id", raw: strings.Replace(call, `"id":"sh_1"`, `"id":null`, 1)},
		{name: "missing environment", raw: strings.Replace(call, `,"environment":null`, "", 1)},
		{name: "missing limit", raw: strings.Replace(call, `"timeout_ms":null,`, "", 1)},
		{name: "null commands", raw: strings.Replace(call, `["x"]`, `null`, 1)},
		{name: "null command entry", raw: strings.Replace(call, `["x"]`, `[null]`, 1)},
		{name: "status", raw: strings.Replace(call, `"completed"`, `"failed"`, 1)},
		{name: "auto call", raw: strings.Replace(call, `"environment":null`, `"environment":{"type":"container_auto"}`, 1)},
		{name: "missing output", raw: strings.Replace(output, `"stdout":"",`, "", 1)},
		{name: "null stdout", raw: strings.Replace(output, `"stdout":""`, `"stdout":null`, 1)},
		{name: "missing exit", raw: strings.Replace(output, `,"exit_code":0`, "", 1)},
		{name: "null exit", raw: strings.Replace(output, `"exit_code":0`, `"exit_code":null`, 1)},
		{name: "null chunks", raw: strings.Replace(output, `[{"stdout":"","stderr":"","outcome":{"type":"exit","exit_code":0}}]`, `null`, 1)},
		{name: "null output status", raw: strings.Replace(output, `"status":"completed"`, `"status":null`, 1)},
		{name: "local no env", raw: strings.Replace(local, `,"env":{}`, "", 1)},
		{name: "local null env value", raw: strings.Replace(local, `"env":{}`, `"env":{"X":null}`, 1)},
		{name: "local null command", raw: strings.Replace(local, `["x"]`, `[null]`, 1)},
		{name: "local wrong action", raw: strings.Replace(local, `"exec"`, `"shell"`, 1)},
		{name: "local missing output", raw: `{"type":"local_shell_call_output","id":"call_1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := shellResponse("[" + tc.raw + "]")
			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
		})
	}
}

func TestRejectInvalidShellInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		item generation.Item
		tool generation.Tool
	}{
		{name: "missing call ID", item: generation.OpenAIShellCall{}},
		{name: "invalid status", item: generation.OpenAIShellCall{CallID: "c", Status: generation.Some("failed")}},
		{name: "command UTF-8", item: generation.OpenAIShellCall{CallID: "c", Action: generation.ShellAction{Commands: []string{"private\xff"}}}},
		{name: "negative timeout", item: generation.OpenAIShellCall{CallID: "c", Action: generation.ShellAction{TimeoutMs: generation.Some(int64(-1))}}},
		{name: "auto call", item: generation.OpenAIShellCall{CallID: "c", Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{})}},
		{name: "caller missing ID", item: generation.OpenAIShellResult{CallID: "c", Caller: generation.Some(generation.OpenAIToolCaller{Type: "program"})}},
		{name: "missing outcome", item: generation.OpenAIShellResult{CallID: "c", Output: []generation.ShellOutput{{Stdout: "private"}}}},
		{name: "local missing item ID", item: generation.OpenAILocalShellCall{CallID: "c", Status: "completed"}},
		{name: "local output UTF-8", item: generation.OpenAILocalShellResult{ID: "c", Output: "private\xff"}},
		{name: "nil environment", tool: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](nil)}},
		{name: "bad callers", tool: generation.OpenAIShellTool{AllowedCallers: generation.Some([]string{"program"})}},
		{name: "missing container ID", tool: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellContainerReference{})}},
		{name: "bad memory", tool: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{MemoryLimit: generation.Some("8g")})}},
		{
			name: "missing local skill path",
			tool: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellLocalEnvironment{Skills: []generation.ShellLocalSkill{{Name: "test"}}})},
		},
		{
			name: "bad inline data",
			tool: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{Skills: []generation.ShellSkill{generation.ShellInlineSkill{Name: "test", Data: "private-invalid"}}})},
		},
		{
			name: "invalid skill version",
			tool: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{Skills: []generation.ShellSkill{generation.ShellSkillReference{SkillID: "skill_1", Version: generation.Some("0")}}})},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			if tc.item != nil {
				request.Input = []generation.Item{tc.item}
			}
			if tc.tool != nil {
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

func TestDecodeShellOutputs(t *testing.T) {
	want := []generation.ShellOutput{
		{Stdout: "", Stderr: "", Outcome: generation.ShellExit{ExitCode: 0}},
		{Stdout: "partial", Stderr: "timeout", Outcome: generation.ShellTimeout{}, CreatedBy: generation.Some("actor")},
	}
	got, err := wire.DecodeShellOutputs(json.RawMessage(`[{"stdout":"","stderr":"","outcome":{"type":"exit","exit_code":0}},{"stdout":"partial","stderr":"timeout","outcome":{"type":"timeout"},"created_by":"actor"}]`))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeShellOutputs = %#v, %v", got, err)
	}
}

func TestRejectMalformedShellOutputs(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{name: "null outputs", raw: `null`},
		{name: "object outputs", raw: `{}`},
		{name: "missing fields", raw: `[{}]`},
		{name: "timeout with exit code", raw: `[{"stdout":"","stderr":"","outcome":{"type":"timeout","exit_code":1}}]`},
		{name: "fractional exit code", raw: `[{"stdout":"","stderr":"","outcome":{"type":"exit","exit_code":0.5}}]`},
		{name: "overflowing exit code", raw: `[{"stdout":"","stderr":"","outcome":{"type":"exit","exit_code":9223372036854775808}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputs, err := wire.DecodeShellOutputs(json.RawMessage(tc.raw))

			var failure *generation.Failure
			if outputs != nil || !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("outputs = %#v, %v; want no outputs and protocol error", outputs, err)
			}
		})
	}
}

func shellResponse(output string) (generation.Result, error) {
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":` + output + `}`))
	if err != nil {
		return generation.Result{}, err
	}
	return envelope.Result(func(string) {})
}
