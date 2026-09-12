package openai_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/stretchr/testify/require"
)

func TestStreamShellCommandsInterleaveAndRecoverSuffixes(t *testing.T) {
	var logs bytes.Buffer
	initial := shellCallJSON("in_progress", nil)
	final := shellCallJSON("completed", []string{"printf hello", "pwd"})
	client := streamClient(t, &logs, created(), outputItemAdded(0, initial),
		`{"type":"response.shell_call_command.added","output_index":0,"command_index":0,"command":"printf "}`,
		`{"type":"response.shell_call_command.added","output_index":0,"command_index":1,"command":"p"}`,
		`{"type":"response.shell_call_command.delta","output_index":0,"command_index":0,"delta":"hel","sequence_number":3}`,
		`{"type":"response.shell_call_command.delta","output_index":0,"command_index":0,"delta":"hel","sequence_number":3}`,
		`{"type":"response.shell_call_command.done","output_index":0,"command_index":1,"command":"pwd"}`,
		`{"type":"response.completed","response":`+responseWithItems(final)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: shellCall("in_progress")},
		generation.ShellCommandDelta{ItemIndex: 0, CommandIndex: 0, Fragment: "printf "},
		generation.ShellCommandDelta{ItemIndex: 0, CommandIndex: 1, Fragment: "p"},
		generation.ShellCommandDelta{ItemIndex: 0, CommandIndex: 0, Fragment: "hel"},
		generation.ShellCommandDelta{ItemIndex: 0, CommandIndex: 1, Fragment: "wd"},
		generation.ShellCommandDelta{ItemIndex: 0, CommandIndex: 0, Fragment: "lo"},
		generation.ItemEnded{Index: 0, Item: shellCall("completed", "printf hello", "pwd")},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
	}
	require.Equal(t, want, events)
	require.Equal(t, 0, logs.Len())
}

func TestStreamShellOutputKeepsCommandAddressesAndFinalList(t *testing.T) {
	var logs bytes.Buffer
	first := shellOutputsJSON("out", "warn", `{"type":"exit","exit_code":0}`)
	second := shellOutputsJSON("", "timeout", `{"type":"timeout"}`)
	client := streamClient(t, &logs, created(), outputItemAdded(0, shellResultJSON("in_progress", `[]`)),
		`{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"out_1","command_index":1,"delta":{"stderr":"time"}}`,
		`{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"out_1","command_index":0,"delta":{"stdout":"ou"}}`,
		shellOutputDone(1, second), shellOutputDone(0, first), shellOutputDone(0, first),
		`{"type":"response.completed","response":`+responseWithItems(shellResultJSON("completed", joinOutputs(first, second)))+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	firstOutput := []generation.ShellOutput{{Stdout: "out", Stderr: "warn", Outcome: generation.ShellExit{ExitCode: 0}}}
	secondOutput := []generation.ShellOutput{{Stderr: "timeout", Outcome: generation.ShellTimeout{}}}
	start := shellResult("in_progress")
	end := shellResult("completed", firstOutput[0], secondOutput[0])
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: start},
		generation.ShellOutputDelta{ItemIndex: 0, CommandIndex: 1, Stderr: "time"},
		generation.ShellOutputDelta{ItemIndex: 0, CommandIndex: 0, Stdout: "ou"},
		generation.ShellOutputDelta{ItemIndex: 0, CommandIndex: 1, Stderr: "out"},
		generation.ShellOutputEnded{ItemIndex: 0, CommandIndex: 1, Output: secondOutput},
		generation.ShellOutputDelta{ItemIndex: 0, CommandIndex: 0, Stdout: "t", Stderr: "warn"},
		generation.ShellOutputEnded{ItemIndex: 0, CommandIndex: 0, Output: firstOutput},
		generation.ItemEnded{Index: 0, Item: end},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}},
	}
	require.Equal(t, want, events)
	require.Equal(t, 0, logs.Len())
}

func TestStreamShellOutputSupportsSeveralChunksPerCommand(t *testing.T) {
	chunks := joinOutputs(shellOutputsJSON("a", "", `{"type":"exit","exit_code":0}`), shellOutputsJSON("b", "", `{"type":"exit","exit_code":1}`))
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, shellResultJSON("in_progress", `[]`)),
		`{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"out_1","command_index":2,"delta":{"stdout":"a"}}`,
		shellOutputDone(2, chunks), `{"type":"response.completed","response":`+responseWithItems(shellResultJSON("completed", chunks))+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.ShellOutput{{Stdout: "a", Outcome: generation.ShellExit{ExitCode: 0}}, {Stdout: "b", Outcome: generation.ShellExit{ExitCode: 1}}}
	if delta := eventAt[generation.ShellOutputDelta](t, events, 3); delta.CommandIndex != 2 || delta.Stdout != "b" {
		t.Fatalf("recovered delta = %#v; want command 2 suffix b", events[3])
	}
	command := eventAt[generation.ShellOutputEnded](t, events, 4)
	item := eventAt[generation.ItemEnded](t, events, 5).Item.(generation.OpenAIShellResult)
	require.Equal(t, 2, command.CommandIndex)
	require.Equal(t, want, command.Output)
	require.Equal(t, want, item.Output)
}

func TestStreamShellRejectsConflictingCommandsAndPreservesUsage(t *testing.T) {
	for _, tc := range []struct{ name, final string }{
		{name: "changed command", final: shellCallJSON("completed", []string{"changed"})},
		{name: "missing command", final: shellCallJSON("completed", nil)},
		{name: "changed call ID", final: strings.Replace(shellCallJSON("completed", []string{"private"}), "call_1", "call_other", 1)},
		{name: "changed environment", final: strings.Replace(shellCallJSON("completed", []string{"private"}), `{"type":"local"}`, `{"type":"container_reference","container_id":"cntr_2"}`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := strings.TrimSuffix(responseWithItems(tc.final), "}") + `,"usage":{"output_tokens":8}}`
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, shellCallJSON("in_progress", []string{"private"})), `{"type":"response.completed","response":`+final+`}`)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
			last := eventAt[generation.UsageUpdated](t, events, len(events)-1)
			if last.Usage.Output != count(8) {
				t.Fatalf("events = %#v; want preserved usage", events)
			}
		})
	}
}

func TestStreamShellRejectsBadEventAddressesAndPayloads(t *testing.T) {
	for _, tc := range []struct{ name, initial, event string }{
		{name: "missing command index", initial: shellCallJSON("in_progress", nil), event: `{"type":"response.shell_call_command.delta","output_index":0,"delta":"private"}`},
		{name: "negative command index", initial: shellCallJSON("in_progress", nil), event: `{"type":"response.shell_call_command.delta","output_index":0,"command_index":-1,"delta":"private"}`},
		{name: "missing command", initial: shellCallJSON("in_progress", nil), event: `{"type":"response.shell_call_command.done","output_index":0,"command_index":0}`},
		{name: "wrong item kind", initial: shellCallJSON("in_progress", nil), event: shellOutputDone(0, `[]`)},
		{name: "wrong item ID", initial: shellResultJSON("in_progress", `[]`), event: `{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"other","command_index":0,"delta":{"stdout":"private"}}`},
		{name: "null stdout", initial: shellResultJSON("in_progress", `[]`), event: `{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"out_1","command_index":0,"delta":{"stdout":null}}`},
		{name: "null output delta", initial: shellResultJSON("in_progress", `[]`), event: `{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"out_1","command_index":0,"delta":null}`},
		{name: "string output delta", initial: shellResultJSON("in_progress", `[]`), event: `{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"out_1","command_index":0,"delta":"private"}`},
		{name: "missing exit code", initial: shellResultJSON("in_progress", `[]`), event: shellOutputDone(0, shellOutputsJSON("", "", `{"type":"exit"}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, tc.initial), tc.event)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamShellOutputRejectsConflictingSnapshot(t *testing.T) {
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, shellResultJSON("in_progress", `[]`)),
		`{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"out_1","command_index":0,"delta":{"stdout":"private"}}`,
		shellOutputDone(0, shellOutputsJSON("changed", "", `{"type":"exit","exit_code":0}`)))

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamShellFinalOnlyPreservesIncompleteCommands(t *testing.T) {
	final := `{"id":"resp_1","model":"test-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + shellCallJSON("incomplete", []string{"printf '"}) + `]}`
	client := streamClient(t, io.Discard, `{"type":"response.incomplete","response":`+final+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := shellCall("incomplete", "printf '")
	require.Equal(t, want, eventAt[generation.ItemEnded](t, events, len(events)-2).Item)
	if finish := eventAt[generation.ResponseEnded](t, events, len(events)-1).Finish; finish.Status != "incomplete" || finish.Reason != "max_output_tokens" {
		t.Fatalf("finish = %#v; want max_output_tokens", finish)
	}
}

func TestStreamShellBoundsAccumulatedOutput(t *testing.T) {
	delta, _ := json.Marshal(strings.Repeat("x", 600_000))
	frame := `{"type":"response.shell_call_output_content.delta","output_index":0,"item_id":"out_1","command_index":0,"delta":{"stdout":` + string(delta) + `}}`
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, shellResultJSON("in_progress", `[]`)), frame, frame)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamShellNeedsTerminalResponse(t *testing.T) {
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, shellCallJSON("completed", []string{"pwd"})))

	events, err := readStream(t, client)

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v; want unexpected EOF", err)
	}
	assertNoResponseEnd(t, events)
}

func TestGenerateShellVariants(t *testing.T) {
	local := `{"type":"local_shell_call","id":"ls_1","call_id":"call_2","status":"completed","action":{"type":"exec","command":["printf","hello world"],"env":{"MODE":""},"working_directory":null}}`
	localResult := `{"type":"local_shell_call_output","id":"call_2","output":"hello world","status":null}`
	body := responseWithItems(shellCallJSON("completed", []string{"pwd"}) + "," + local + "," + localResult)
	client := testClient(t, staticJSON(body), io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	require.NoError(t, err)
	want := []generation.Item{
		shellCall("completed", "pwd"),
		generation.OpenAILocalShellCall{ID: "ls_1", CallID: "call_2", Status: "completed", Action: generation.LocalShellAction{Command: []string{"printf", "hello world"}, Env: map[string]string{"MODE": ""}, WorkingDirectory: generation.Null[string]()}},
		generation.OpenAILocalShellResult{ID: "call_2", Output: "hello world", Status: generation.Null[string]()},
	}
	require.Equal(t, want, result.Response.Output)
	require.Equal(t, "tool_calls", result.Response.Finish.Reason)
}

func TestStreamLocalShellPreservesArgumentsAndSnapshotOwnership(t *testing.T) {
	initial := `{"type":"local_shell_call","id":"ls_1","call_id":"call_2","status":"in_progress","action":{"type":"exec","command":["printf","hel"],"env":{"MODE":""}}}`
	final := strings.Replace(strings.Replace(initial, "in_progress", "completed", 1), `"hel"`, `"hello world"`, 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(final)+`}`)
	stream, err := client.GenerateStream(t.Context(), testAttempt(), textRequest())
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	var end generation.OpenAILocalShellCall
	starts := 0
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		switch value := event.(type) {
		case generation.ItemStarted:
			starts++
			call := value.Item.(generation.OpenAILocalShellCall)
			call.Action.Command[0] = "mutated"
			call.Action.Env["MODE"] = "mutated"
		case generation.ItemEnded:
			end = value.Item.(generation.OpenAILocalShellCall)
		}
	}

	want := generation.OpenAILocalShellCall{ID: "ls_1", CallID: "call_2", Status: "completed", Action: generation.LocalShellAction{Command: []string{"printf", "hello world"}, Env: map[string]string{"MODE": ""}}}
	require.Equal(t, 1, starts)
	require.Equal(t, want, end)
}

func TestStreamLocalShellRetainsOmittedResultStatus(t *testing.T) {
	initial := `{"type":"local_shell_call_output","id":"call_1","output":"ok","status":"completed"}`
	final := `{"type":"local_shell_call_output","id":"call_1","output":"okay"}`
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(final)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := generation.OpenAILocalShellResult{ID: "call_1", Output: "okay", Status: generation.Some("completed")}
	require.Equal(t, want, eventAt[generation.ItemEnded](t, events, len(events)-2).Item)
}

func TestStreamShellPreservesLateMetadata(t *testing.T) {
	initial := shellCallJSON("in_progress", nil)
	completed := shellCallJSON("completed", []string{"pwd"})
	withMetadata := strings.TrimSuffix(completed, "}") + `,"caller":{"type":"program","caller_id":"program_1"},"created_by":"program_1"}`
	withMetadata = strings.Replace(withMetadata, `"timeout_ms":null`, `"timeout_ms":5000`, 1)
	final := strings.Replace(completed, `"timeout_ms":null`, `"timeout_ms":5000`, 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial),
		`{"type":"response.output_item.done","output_index":0,"item":`+withMetadata+`}`,
		`{"type":"response.completed","response":`+responseWithItems(final)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := shellCall("completed", "pwd")
	want.Action.TimeoutMs = generation.Some[int64](5000)
	want.Caller = generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"})
	want.CreatedBy = generation.Some("program_1")
	require.Equal(t, want, eventAt[generation.ItemEnded](t, events, len(events)-2).Item)
}

func TestStreamShellRetainsChunkCreator(t *testing.T) {
	output := shellOutputsJSON("ok", "", `{"type":"exit","exit_code":0}`)
	withCreator := strings.TrimSuffix(output, "}]") + `,"created_by":"program_1"}]`
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, shellResultJSON("in_progress", withCreator)),
		shellOutputDone(0, withCreator), shellOutputDone(0, output),
		`{"type":"response.completed","response":`+responseWithItems(shellResultJSON("completed", output))+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	ends := 0
	for _, event := range events {
		if end, ok := event.(generation.ShellOutputEnded); ok {
			ends++
			if end.Output[0].CreatedBy != generation.Some("program_1") {
				t.Fatalf("command output = %#v; want preserved creator", end.Output)
			}
		}
	}
	end := eventAt[generation.ItemEnded](t, events, len(events)-2).Item.(generation.OpenAIShellResult)
	if ends != 1 || end.Output[0].CreatedBy != generation.Some("program_1") {
		t.Fatalf("command ends = %d; item output = %#v; want one end and preserved creator", ends, end.Output)
	}
}

func TestStreamShellRejectsChangedChunkCreator(t *testing.T) {
	output := shellOutputsJSON("ok", "", `{"type":"exit","exit_code":0}`)
	first := strings.TrimSuffix(output, "}]") + `,"created_by":"program_1"}]`
	second := strings.Replace(first, "program_1", "program_other", 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, shellResultJSON("in_progress", `[]`)), shellOutputDone(0, first), shellOutputDone(0, second))

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func shellCall(status string, commands ...string) generation.OpenAIShellCall {
	return generation.OpenAIShellCall{
		ID: generation.Some("sh_1"), CallID: "call_1", Status: generation.Some(status),
		Action:      generation.ShellAction{Commands: commands, TimeoutMs: generation.Null[int64](), MaxOutputLength: generation.Null[int64]()},
		Environment: generation.Some[generation.ShellEnvironment](generation.ShellLocalEnvironment{}),
	}
}

func shellResult(status string, output ...generation.ShellOutput) generation.OpenAIShellResult {
	return generation.OpenAIShellResult{ID: generation.Some("out_1"), CallID: "call_1", Status: generation.Some(status), MaxOutputLength: generation.Null[int64](), Output: output}
}

func shellCallJSON(status string, commands []string) string {
	if commands == nil {
		commands = []string{}
	}
	encoded, _ := json.Marshal(commands)
	return fmt.Sprintf(`{"type":"shell_call","id":"sh_1","call_id":"call_1","status":"%s","action":{"commands":%s,"timeout_ms":null,"max_output_length":null},"environment":{"type":"local"}}`, status, encoded)
}

func shellResultJSON(status, output string) string {
	return fmt.Sprintf(`{"type":"shell_call_output","id":"out_1","call_id":"call_1","status":"%s","max_output_length":null,"output":%s}`, status, output)
}

func shellOutputsJSON(stdout, stderr, outcome string) string {
	out, _ := json.Marshal(stdout)
	err, _ := json.Marshal(stderr)
	return fmt.Sprintf(`[{"stdout":%s,"stderr":%s,"outcome":%s}]`, out, err, outcome)
}

func shellOutputDone(command int, output string) string {
	return fmt.Sprintf(`{"type":"response.shell_call_output_content.done","output_index":0,"item_id":"out_1","command_index":%d,"output":%s}`, command, output)
}

func joinOutputs(first, second string) string {
	return strings.TrimSuffix(first, "]") + "," + strings.TrimPrefix(second, "[")
}
