package openai_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/stretchr/testify/require"
)

func TestGenerateComputerCallKeepsSingleAndBatchedActions(t *testing.T) {
	item := computerCallJSON(`,"action":{"type":"screenshot"},"actions":[{"type":"click","x":0,"y":20,"button":"left","keys":null},{"type":"type","text":"hello"}]`)
	client := testClient(t, staticJSON(responseWithItems(item)), io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	require.NoError(t, err)
	want := computerCall()
	want.Action = generation.ComputerScreenshot{}
	want.Actions = []generation.ComputerAction{generation.ComputerClick{X: 0, Y: 20, Button: "left", Keys: generation.Null[[]string]()}, generation.ComputerType{Text: "hello"}}
	require.Equal(t, []generation.Item{want}, result.Response.Output)
	require.Equal(t, "tool_calls", result.Response.Finish.Reason)
}

func TestStreamComputerInterleavesCallsAndKeepsLateChecks(t *testing.T) {
	var logs bytes.Buffer
	initial := strings.Replace(computerCallJSON(""), "completed", "in_progress", 1)
	second := strings.Replace(strings.Replace(computerCallJSON(`,"action":{"type":"wait"}`), "cu_1", "cu_2", 1), "call_1", "call_2", 1)
	partial := strings.Replace(computerCallJSON(`,"actions":[{"type":"screenshot"}]`), `"pending_safety_checks":[]`, `"pending_safety_checks":[{"id":"check_1","code":null}]`, 1)
	final := strings.Replace(computerCallJSON(`,"actions":[{"type":"screenshot"},{"type":"type","text":"hello"}]`), `"pending_safety_checks":[]`, `"pending_safety_checks":[{"id":"check_1","message":"confirm navigation"}]`, 1)
	client := streamClient(t, &logs, created(), outputItemAdded(0, initial), outputItemAdded(1, second),
		`{"type":"response.output_item.done","output_index":0,"item":`+partial+`}`,
		`{"type":"response.completed","response":`+responseWithItems(final+","+second)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	start := computerCall()
	start.Status = "in_progress"
	other := computerCall()
	other.ID, other.CallID, other.Action = "cu_2", "call_2", generation.ComputerWait{}
	end := computerCall()
	end.Actions = []generation.ComputerAction{generation.ComputerScreenshot{}, generation.ComputerType{Text: "hello"}}
	end.PendingSafetyChecks = []generation.ComputerSafetyCheck{{ID: "check_1", Code: generation.Null[string](), Message: generation.Some("confirm navigation")}}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: start},
		generation.ItemStarted{Index: 1, Item: other},
		generation.ItemEnded{Index: 0, Item: end},
		generation.ItemEnded{Index: 1, Item: other},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
	}
	require.Equal(t, want, events)
	require.Equal(t, 0, logs.Len())
}

func TestStreamComputerKeepsOmittedActions(t *testing.T) {
	initial := computerCallJSON(`,"action":{"type":"screenshot"},"actions":[{"type":"wait"}]`)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(computerCallJSON(""))+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := computerCall()
	want.Action = generation.ComputerScreenshot{}
	want.Actions = []generation.ComputerAction{generation.ComputerWait{}}
	require.Equal(t, want, eventAt[generation.ItemEnded](t, events, len(events)-2).Item)
}

func TestStreamComputerRejectsConflictsAndPreservesUsage(t *testing.T) {
	initial := strings.Replace(computerCallJSON(`,"action":{"type":"click","x":0,"y":2,"button":"left","keys":["SHIFT"]},"actions":[{"type":"type","text":"private"}]`), `"pending_safety_checks":[]`, `"pending_safety_checks":[{"id":"check_1","code":"confirm"}]`, 1)
	for _, tc := range []struct{ name, before, after string }{
		{name: "call identity", before: `"call_1"`, after: `"other"`},
		{name: "changed modifiers", before: `["SHIFT"]`, after: `["ALT"]`},
		{name: "coordinates", before: `"x":0`, after: `"x":1`},
		{name: "action kind", before: `"type":"click"`, after: `"type":"move"`},
		{name: "batch content", before: `"private"`, after: `"changed"`},
		{name: "removed batch", before: `"actions":[{"type":"type","text":"private"}]`, after: `"actions":[]`},
		{name: "removed safety check", before: `[{"id":"check_1","code":"confirm"}]`, after: `[]`},
		{name: "changed check code", before: `"code":"confirm"`, after: `"code":"different"`},
		{name: "status regression", before: `"status":"completed"`, after: `"status":"in_progress"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := strings.TrimSuffix(responseWithItems(strings.Replace(initial, tc.before, tc.after, 1)), "}") + `,"usage":{"output_tokens":8}}`
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+final+`}`)

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

func TestStreamComputerSnapshotsOwnNestedData(t *testing.T) {
	call := computerCallJSON(`,"action":{"type":"drag","path":[{"x":0,"y":10}],"keys":["SHIFT"]},"actions":[{"type":"keypress","keys":["ENTER"]}]`)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, call), `{"type":"response.completed","response":`+responseWithItems(call)+`}`)
	stream, err := client.GenerateStream(t.Context(), testAttempt(), textRequest())
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	var end generation.OpenAIComputerCall
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
			call := value.Item.(generation.OpenAIComputerCall)
			drag := call.Action.(generation.ComputerDrag)
			drag.Path[0].X = 99
			keys, _ := drag.Keys.Value()
			keys[0] = "ALT"
			call.Actions[0].(generation.ComputerKeypress).Keys[0] = "changed"
		case generation.ItemEnded:
			end = value.Item.(generation.OpenAIComputerCall)
		}
	}

	want := computerCall()
	want.Action = generation.ComputerDrag{Path: []generation.ComputerPoint{{X: 0, Y: 10}}, Keys: generation.Some([]string{"SHIFT"})}
	want.Actions = []generation.ComputerAction{generation.ComputerKeypress{Keys: []string{"ENTER"}}}
	require.Equal(t, 1, starts)
	require.Equal(t, want, end)
}

func TestStreamComputerResultRetainsScreenshotAndAcknowledgements(t *testing.T) {
	initial := computerResultJSON(`{"type":"computer_screenshot","file_id":"file_1","detail":"original"}`, `,"acknowledged_safety_checks":[{"id":"check_1","code":null}],"created_by":"program_1"`)
	final := computerResultJSON(`{"type":"computer_screenshot","image_url":"https://example.com/screenshot.png"}`, "")
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(final)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := generation.OpenAIComputerResult{
		ID: generation.Some("out_1"), CallID: "call_1", Status: generation.Some("completed"),
		Output:                   generation.ComputerScreenshotOutput{FileID: generation.Some("file_1"), ImageURL: generation.Some("https://example.com/screenshot.png"), Detail: generation.Some("original")},
		AcknowledgedSafetyChecks: generation.Some([]generation.ComputerSafetyCheck{{ID: "check_1", Code: generation.Null[string]()}}), CreatedBy: generation.Some("program_1"),
	}
	require.Equal(t, want, eventAt[generation.ItemEnded](t, events, len(events)-2).Item)
}

func TestGenerateComputerResultAllowsOmittedScreenshotReferences(t *testing.T) {
	client := testClient(t, staticJSON(responseWithItems(computerResultJSON(`{"type":"computer_screenshot"}`, ""))), io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	require.NoError(t, err)
	want := generation.OpenAIComputerResult{ID: generation.Some("out_1"), CallID: "call_1", Status: generation.Some("completed")}
	require.Equal(t, []generation.Item{want}, result.Response.Output)
	require.Equal(t, "stop", result.Response.Finish.Reason)
}

func TestStreamComputerBoundsRetainedActions(t *testing.T) {
	text, _ := json.Marshal(strings.Repeat("x", 600_000))
	call := computerCallJSON(`,"actions":[{"type":"type","text":` + string(text) + `}]`)
	second := strings.Replace(call, "cu_1", "cu_2", 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, call), outputItemAdded(1, second))

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamComputerNeedsTerminalResponse(t *testing.T) {
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, computerCallJSON(`,"actions":[{"type":"wait"}]`)))

	events, err := readStream(t, client)

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v; want unexpected EOF", err)
	}
	assertNoResponseEnd(t, events)
}

func TestStreamComputerRetainsModifiersInBothActionForms(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		want         generation.ComputerAction
	}{
		{name: "click", action: `{"type":"click","x":0,"y":1,"button":"left"}`, want: generation.ComputerClick{X: 0, Y: 1, Button: "left", Keys: generation.Some([]string{"SHIFT"})}},
		{name: "drag", action: `{"type":"drag","path":[{"x":0,"y":1}]}`, want: generation.ComputerDrag{Path: []generation.ComputerPoint{{X: 0, Y: 1}}, Keys: generation.Some([]string{"SHIFT"})}},
		{name: "move", action: `{"type":"move","x":0,"y":1}`, want: generation.ComputerMove{X: 0, Y: 1, Keys: generation.Some([]string{"SHIFT"})}},
		{name: "scroll", action: `{"type":"scroll","x":0,"y":1,"scroll_x":0,"scroll_y":-10}`, want: generation.ComputerScroll{X: 0, Y: 1, ScrollY: -10, Keys: generation.Some([]string{"SHIFT"})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initialAction := strings.TrimSuffix(tc.action, "}") + `,"keys":["SHIFT"]}`
			initial := computerCallJSON(`,"action":` + initialAction + `,"actions":[` + initialAction + `]`)
			final := computerCallJSON(`,"action":` + tc.action + `,"actions":[` + tc.action + `]`)
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(final)+`}`)

			events, err := readStream(t, client)

			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			end := eventAt[generation.ItemEnded](t, events, len(events)-2).Item.(generation.OpenAIComputerCall)
			require.Equal(t, tc.want, end.Action)
			require.Equal(t, []generation.ComputerAction{tc.want}, end.Actions)
		})
	}
}

func TestStreamComputerAcceptsLateModifiers(t *testing.T) {
	initial := computerCallJSON(`,"action":{"type":"move","x":0,"y":1,"keys":null},"actions":[{"type":"move","x":0,"y":1}]`)
	final := computerCallJSON(`,"action":{"type":"move","x":0,"y":1,"keys":["SHIFT"]},"actions":[{"type":"move","x":0,"y":1,"keys":["SHIFT"]}]`)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(final)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := generation.ComputerMove{X: 0, Y: 1, Keys: generation.Some([]string{"SHIFT"})}
	end := eventAt[generation.ItemEnded](t, events, len(events)-2).Item.(generation.OpenAIComputerCall)
	require.Equal(t, want, end.Action)
	require.Equal(t, []generation.ComputerAction{want}, end.Actions)
}

func TestStreamComputerRejectsChangedScreenshotAndAcknowledgements(t *testing.T) {
	initial := computerResultJSON(`{"type":"computer_screenshot","file_id":"file_1","detail":"original"}`, `,"acknowledged_safety_checks":[{"id":"check_1","message":"private"}]`)
	for _, tc := range []struct{ name, before, after string }{
		{name: "screenshot identity", before: `"file_1"`, after: `"other_file"`},
		{name: "detail", before: `"original"`, after: `"low"`},
		{name: "removed acknowledgement", before: `[{"id":"check_1","message":"private"}]`, after: `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := strings.Replace(initial, tc.before, tc.after, 1)
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(final)+`}`)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamComputerFinalOnlyIncomplete(t *testing.T) {
	call := strings.Replace(computerCallJSON(`,"actions":[{"type":"type","text":"hel"}]`), "completed", "incomplete", 1)
	final := `{"id":"resp_1","model":"test-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + call + `]}`
	client := streamClient(t, io.Discard, `{"type":"response.incomplete","response":`+final+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := computerCall()
	want.Status = "incomplete"
	want.Actions = []generation.ComputerAction{generation.ComputerType{Text: "hel"}}
	require.Equal(t, want, eventAt[generation.ItemEnded](t, events, len(events)-2).Item)
	if finish := eventAt[generation.ResponseEnded](t, events, len(events)-1).Finish; finish.Reason != "max_output_tokens" || finish.Status != "incomplete" {
		t.Fatalf("finish = %#v; want incomplete due to max_output_tokens", finish)
	}
}

func computerCall() generation.OpenAIComputerCall {
	return generation.OpenAIComputerCall{ID: "cu_1", CallID: "call_1", Status: "completed", PendingSafetyChecks: []generation.ComputerSafetyCheck{}}
}

func computerCallJSON(extra string) string {
	return `{"type":"computer_call","id":"cu_1","call_id":"call_1","status":"completed","pending_safety_checks":[]` + extra + `}`
}

func computerResultJSON(output, extra string) string {
	return `{"type":"computer_call_output","id":"out_1","call_id":"call_1","status":"completed","output":` + output + extra + `}`
}
