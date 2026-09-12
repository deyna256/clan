package openai_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
)

func TestGeneratePatchCalls(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		want            generation.PatchOperation
	}{
		{name: "create", operation: `{"type":"create_file","path":"new.go","diff":"+package main\n"}`, want: generation.PatchCreateFile{Path: "new.go", Diff: "+package main\n"}},
		{name: "update", operation: patchUpdateJSON("@@\n-old\n+новое\n"), want: generation.PatchUpdateFile{Path: "main.go", Diff: "@@\n-old\n+новое\n"}},
		{name: "delete", operation: `{"type":"delete_file","path":"old.go"}`, want: generation.PatchDeleteFile{Path: "old.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := responseWithItems(patchCallJSON("completed", tc.operation, `,"caller":{"type":"program","caller_id":"program_1"}`))
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			if err != nil {
				t.Fatal(err)
			}
			want := []generation.Item{generation.OpenAIApplyPatchCall{ID: "ap_1", CallID: "call_1", Status: "completed", Operation: tc.want, Caller: generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"})}}
			if !reflect.DeepEqual(result.Response.Output, want) || result.Response.Finish.Reason != "tool_calls" {
				t.Fatalf("response = %#v; want %#v and tool_calls", result.Response, want)
			}
		})
	}
}

func TestGeneratePatchResults(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		output      generation.Optional[string]
	}{
		{name: "omitted"},
		{name: "null", field: `,"output":null`, output: generation.Null[string]()},
		{name: "empty", field: `,"output":""`, output: generation.Some("")},
		{name: "failure", field: `,"output":"not found"`, output: generation.Some("not found")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := responseWithItems(`{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"failed","caller":null` + tc.field + `}`)
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			if err != nil {
				t.Fatal(err)
			}
			want := []generation.Item{generation.OpenAIApplyPatchResult{ID: "out_1", CallID: "call_1", Status: "failed", Output: tc.output, Caller: generation.Null[generation.OpenAIToolCaller]()}}
			if !reflect.DeepEqual(result.Response.Output, want) || result.Response.Finish.Reason != "stop" {
				t.Fatalf("response = %#v; want %#v and stop", result.Response, want)
			}
		})
	}
}

func TestStreamPatchInterleavesAndRecoversDiff(t *testing.T) {
	var logs bytes.Buffer
	first := patchCallJSON("in_progress", patchUpdateJSON(""), "")
	partial := patchCallJSON("completed", patchUpdateJSON("@@\n-old\n"), `,"caller":{"type":"direct"}`)
	final := patchCallJSON("completed", patchUpdateJSON("@@\n-old\n+new\n"), "")
	deletion := `{"type":"apply_patch_call","id":"ap_2","call_id":"call_2","status":"completed","operation":{"type":"delete_file","path":"old.go"}}`
	client := streamClient(t, &logs, created(), outputItemAdded(0, first), outputItemAdded(1, deletion),
		`{"type":"response.output_item.done","output_index":0,"item":`+partial+`}`,
		`{"type":"response.completed","response":`+responseWithItems(final+","+deletion)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.OpenAIApplyPatchCall{ID: "ap_1", CallID: "call_1", Status: "in_progress", Operation: generation.PatchUpdateFile{Path: "main.go"}}},
		generation.ItemStarted{Index: 1, Item: generation.OpenAIApplyPatchCall{ID: "ap_2", CallID: "call_2", Status: "completed", Operation: generation.PatchDeleteFile{Path: "old.go"}}},
		generation.PatchDiffDelta{ItemIndex: 0, Fragment: "@@\n-old\n"},
		generation.PatchDiffDelta{ItemIndex: 0, Fragment: "+new\n"},
		generation.ItemEnded{Index: 0, Item: generation.OpenAIApplyPatchCall{ID: "ap_1", CallID: "call_1", Status: "completed", Operation: generation.PatchUpdateFile{Path: "main.go", Diff: "@@\n-old\n+new\n"}, Caller: generation.Some(generation.OpenAIToolCaller{Type: "direct"})}},
		generation.ItemEnded{Index: 1, Item: generation.OpenAIApplyPatchCall{ID: "ap_2", CallID: "call_2", Status: "completed", Operation: generation.PatchDeleteFile{Path: "old.go"}}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
	}
	if !reflect.DeepEqual(events, want) || logs.Len() != 0 {
		t.Fatalf("events = %#v; want %#v; logs = %s", events, want, logs.String())
	}
}

func TestStreamPatchFinalOnlyIncomplete(t *testing.T) {
	call := patchCallJSON("in_progress", patchUpdateJSON("@@\n-"), "")
	client := streamClient(t, io.Discard, `{"type":"response.incomplete","response":{"id":"resp_1","model":"test-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[`+call+`]}}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.OpenAIApplyPatchCall{ID: "ap_1", CallID: "call_1", Status: "in_progress", Operation: generation.PatchUpdateFile{Path: "main.go"}}},
		generation.PatchDiffDelta{ItemIndex: 0, Fragment: "@@\n-"},
		generation.ItemEnded{Index: 0, Item: generation.OpenAIApplyPatchCall{ID: "ap_1", CallID: "call_1", Status: "in_progress", Operation: generation.PatchUpdateFile{Path: "main.go", Diff: "@@\n-"}}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "incomplete", Reason: "max_output_tokens"}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v; want %#v", events, want)
	}
}

func TestStreamPatchResultRetainsOutputAndCaller(t *testing.T) {
	result := `{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"failed"}`
	initial := strings.TrimSuffix(result, "}") + `,"output":"not found","caller":{"type":"program","caller_id":"program_1"}}`
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.output_item.done","output_index":0,"item":`+result+`}`,
		`{"type":"response.completed","response":`+responseWithItems(result)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	caller := generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"})
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.OpenAIApplyPatchResult{ID: "out_1", CallID: "call_1", Status: "failed", Caller: caller}},
		generation.ItemEnded{Index: 0, Item: generation.OpenAIApplyPatchResult{ID: "out_1", CallID: "call_1", Status: "failed", Output: generation.Some("not found"), Caller: caller}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v; want %#v", events, want)
	}
}

func TestStreamPatchConflictsPreserveUsage(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{name: "item ID", before: `"id":"ap_1"`, after: `"id":"ap_other"`},
		{name: "call ID", before: `"call_id":"call_1"`, after: `"call_id":"other"`},
		{name: "path", before: `"path":"main.go"`, after: `"path":"other.go"`},
		{name: "operation", before: `"type":"update_file"`, after: `"type":"create_file"`},
		{name: "diff", before: `"diff":"private"`, after: `"diff":"changed"`},
		{name: "caller", before: `"caller_id":"program_1"`, after: `"caller_id":"other"`},
		{name: "status regression", before: `"status":"completed"`, after: `"status":"in_progress"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := patchCallJSON("completed", patchUpdateJSON("private"), `,"caller":{"type":"program","caller_id":"program_1"}`)
			conflict := strings.Replace(initial, tc.before, tc.after, 1)
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":[`+conflict+`],"usage":{"output_tokens":8}}}`)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
			last, ok := events[len(events)-1].(generation.UsageUpdated)
			if !ok || last.Usage.Output != count(8) {
				t.Fatalf("events = %#v; want preserved usage", events)
			}
		})
	}
}

func TestGenerateRejectsMalformedPatch(t *testing.T) {
	for _, tc := range []struct{ name, operation string }{
		{name: "missing operation", operation: "null"},
		{name: "missing diff", operation: `{"type":"update_file","path":"main.go"}`},
		{name: "null diff", operation: `{"type":"create_file","path":"main.go","diff":null}`},
		{name: "wrong diff type", operation: `{"type":"update_file","path":"main.go","diff":{}}`},
		{name: "missing path", operation: `{"type":"delete_file"}`},
		{name: "delete with diff", operation: `{"type":"delete_file","path":"main.go","diff":"private"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"resp_1","model":"test-model","status":"completed","output":[` + patchCallJSON("completed", tc.operation, "") + `],"usage":{"output_tokens":8}}`
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			assertProtocolError(t, err)
			if result.Usage.Output != count(8) {
				t.Fatalf("usage = %#v; want 8 output tokens", result.Usage)
			}
		})
	}
}

func TestStreamPatchBoundsRetainedDiff(t *testing.T) {
	large := strings.Repeat("x", 600_000)
	call := patchCallJSON("in_progress", patchUpdateJSON(large), "")
	second := strings.Replace(call, `"id":"ap_1"`, `"id":"ap_2"`, 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, call), outputItemAdded(1, second))

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamPatchResultUpdates(t *testing.T) {
	for _, tc := range []struct {
		name, status, output string
		valid                bool
	}{
		{name: "extended output", status: "failed", output: "not found: main.go", valid: true},
		{name: "changed output", status: "failed", output: "changed"},
		{name: "changed status", status: "completed", output: "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := `{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"failed","output":"not found"}`
			encoded, _ := json.Marshal(tc.output)
			final := fmt.Sprintf(`{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"%s","output":%s}`, tc.status, encoded)
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(final)+`}`)

			events, err := readStream(t, client)

			if !tc.valid {
				assertProtocolError(t, err)
				assertNoResponseEnd(t, events)
				return
			}
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			end := events[len(events)-2].(generation.ItemEnded).Item.(generation.OpenAIApplyPatchResult)
			if end.Output != generation.Some("not found: main.go") || end.Status != "failed" {
				t.Fatalf("result = %#v; want extended failure output", end)
			}
		})
	}
}

func TestStreamPatchNeedsTerminalResponse(t *testing.T) {
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, patchCallJSON("completed", patchUpdateJSON("+new\n"), "")))

	events, err := readStream(t, client)

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v; want unexpected EOF", err)
	}
	assertNoResponseEnd(t, events)
}

func patchCallJSON(status, operation, extra string) string {
	return fmt.Sprintf(`{"type":"apply_patch_call","id":"ap_1","call_id":"call_1","status":"%s","operation":%s%s}`, status, operation, extra)
}

func patchUpdateJSON(diff string) string {
	encoded, _ := json.Marshal(diff)
	return `{"type":"update_file","path":"main.go","diff":` + string(encoded) + `}`
}
