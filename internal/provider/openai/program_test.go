package openai_test

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/stretchr/testify/require"
)

func TestStreamProgramAndNestedFunction(t *testing.T) {
	program := programJSON("prog_1", "text(await tools.lookup({}));", `,"call_id":"call_prog","fingerprint":"opaque"`)
	call := functionCallJSON("fc_1", "call_1", "{}", `,"caller":{"type":"program","caller_id":"call_prog"}`)
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, programJSON("prog_1", "text(", "")),
		outputItemAdded(1, functionCallJSON("fc_1", "call_1", "", `,"caller":{"type":"program","caller_id":"call_prog"}`)),
		outputItemAdded(0, program),
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{}"}`,
		`{"type":"response.completed","response":`+responseWithItems(program+","+call)+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	wantProgram := generation.OpenAIProgram{ID: "prog_1", CallID: "call_prog", Code: "text(await tools.lookup({}));", Fingerprint: generation.Some("opaque")}
	wantCall := generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "lookup", Arguments: "{}", OpenAI: generation.OpenAIToolCallData{Caller: generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "call_prog"})}}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.OpenAIProgram{ID: "prog_1"}},
		generation.ItemStarted{Index: 1, Item: generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "lookup", OpenAI: generation.OpenAIToolCallData{Caller: generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "call_prog"})}}},
		generation.ArgumentsDelta{ItemIndex: 1, Fragment: "{}"},
		generation.ItemEnded{Index: 0, Item: wantProgram},
		generation.ItemEnded{Index: 1, Item: wantCall},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
	}
	require.Equal(t, want, events)
}

func TestStreamProgramOutputSnapshots(t *testing.T) {
	output := programOutputJSON("out_1", "partial result", `,"call_id":"call_prog","status":"incomplete"`)
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, programOutputJSON("out_1", "", "")),
		outputItemAdded(0, programOutputJSON("out_1", "partial", `,"call_id":"call_prog","status":"incomplete"`)),
		outputItemAdded(0, programOutputJSON("out_1", "partial result", "")),
		`{"type":"response.completed","response":`+responseWithItems(output)+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.OpenAIProgramOutput{ID: "out_1"}},
		generation.ItemEnded{Index: 0, Item: generation.OpenAIProgramOutput{ID: "out_1", CallID: "call_prog", Result: "partial result", Status: generation.ItemIncomplete}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}},
	}
	require.Equal(t, want, events)
}

func TestStreamProgramFingerprintPresence(t *testing.T) {
	for _, fingerprint := range []string{"", "opaque"} {
		t.Run(fingerprint, func(t *testing.T) {
			encoded, _ := json.Marshal(fingerprint)
			program := programJSON("prog_1", "", `,"call_id":"call_prog","fingerprint":`+string(encoded))
			client := streamClient(t, io.Discard, created(),
				outputItemAdded(0, program),
				outputItemAdded(0, programJSON("prog_1", "", "")),
				`{"type":"response.completed","response":`+responseWithItems(program)+`}`,
			)

			events, err := readStream(t, client)

			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			got := eventAt[generation.ItemEnded](t, events, len(events)-2).Item.(generation.OpenAIProgram)
			want := generation.OpenAIProgram{ID: "prog_1", CallID: "call_prog", Fingerprint: generation.Some(fingerprint)}
			require.Equal(t, want, got)
		})
	}
}

func TestStreamProgramRejectsConflicts(t *testing.T) {
	program := programJSON("prog_1", "text(1)", `,"call_id":"call_prog","fingerprint":"opaque"`)
	output := programOutputJSON("out_1", "done", `,"call_id":"call_prog","status":"completed"`)
	for _, tc := range []struct{ name, before, after string }{
		{name: "code", before: program, after: strings.Replace(program, "text(1)", "text(2)", 1)},
		{name: "fingerprint", before: program, after: strings.Replace(program, "opaque", "different", 1)},
		{name: "empty fingerprint", before: program, after: strings.Replace(program, "opaque", "", 1)},
		{name: "program call ID", before: program, after: strings.Replace(program, "call_prog", "call_other", 1)},
		{name: "result", before: output, after: strings.Replace(output, "done", "changed", 1)},
		{name: "output call ID", before: output, after: strings.Replace(output, "call_prog", "call_other", 1)},
		{name: "output status", before: output, after: strings.Replace(output, "completed", "incomplete", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := strings.Replace(responseWithItems(tc.after), `"output":`, `"usage":{"output_tokens":8},"output":`, 1)
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, tc.before), `{"type":"response.completed","response":`+final+`}`)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
			last := eventAt[generation.UsageUpdated](t, events, len(events)-1)
			if last.Usage.Output != count(8) {
				t.Fatalf("last event = %#v; want output usage 8", events[len(events)-1])
			}
		})
	}
}

func TestStreamProgramNeedsTerminalResponse(t *testing.T) {
	program := programJSON("prog_1", "text(1)", `,"call_id":"call_prog","fingerprint":"opaque"`)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, program), `{"type":"response.output_item.done","output_index":0,"item":`+program+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v; want unexpected EOF", err)
	}
	assertNoResponseEnd(t, events)
}

func TestStreamProgramBoundsRetainedData(t *testing.T) {
	for _, tc := range []struct{ name, first, second string }{
		{name: "code", first: programJSON("prog_1", strings.Repeat("x", 600_000), `,"call_id":"c1","fingerprint":"fp"`), second: programJSON("prog_2", strings.Repeat("y", 600_000), `,"call_id":"c2","fingerprint":"fp"`)},
		{name: "result", first: programOutputJSON("out_1", strings.Repeat("x", 600_000), `,"call_id":"c1","status":"completed"`), second: programOutputJSON("out_2", strings.Repeat("y", 600_000), `,"call_id":"c2","status":"completed"`)},
		{name: "fingerprint", first: programJSON("prog_1", "", `,"call_id":"c1","fingerprint":"`+strings.Repeat("x", 600_000)+`"`), second: programJSON("prog_2", "", `,"call_id":"c2","fingerprint":"`+strings.Repeat("y", 600_000)+`"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, tc.first), outputItemAdded(1, tc.second))

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamProgramDoesNotAccumulateRepeatedSnapshots(t *testing.T) {
	code := strings.Repeat("x", 600_000)
	program := programJSON("prog_1", code, `,"call_id":"call_prog","fingerprint":"opaque"`)
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, program), outputItemAdded(0, program),
		`{"type":"response.completed","response":`+responseWithItems(program)+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	got := eventAt[generation.ItemEnded](t, events, len(events)-2).Item.(generation.OpenAIProgram)
	if got.Code != code {
		t.Fatalf("code length = %d; want unchanged %d-byte code", len(got.Code), len(code))
	}
}

func programJSON(id, code, extra string) string {
	encoded, _ := json.Marshal(code)
	return `{"type":"program","id":"` + id + `","code":` + string(encoded) + extra + `}`
}

func programOutputJSON(id, result, extra string) string {
	encoded, _ := json.Marshal(result)
	return `{"type":"program_output","id":"` + id + `","result":` + string(encoded) + extra + `}`
}
