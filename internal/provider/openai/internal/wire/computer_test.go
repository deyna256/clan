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

func TestComputerTools(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   generation.Tool
		choice generation.OpenAIComputerChoice
		want   string
	}{
		{name: "computer", tool: generation.OpenAIComputerTool{}, choice: "computer", want: `{"type":"computer"}`},
		{name: "preview", tool: generation.OpenAIComputerPreviewTool{DisplayWidth: 1920, DisplayHeight: 1080, Environment: "browser"}, choice: "computer_use_preview", want: `{"type":"computer_use_preview","display_width":1920,"display_height":1080,"environment":"browser"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}
			request.ToolChoice = tc.choice

			body, err := wire.EncodeRequest(request, false)

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[],"tools":[`+tc.want+`],"tool_choice":{"type":"`+string(tc.choice)+`"}}`)
		})
	}
}

func TestComputerActions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action generation.ComputerAction
		raw    string
	}{
		{name: "click", action: generation.ComputerClick{X: 0, Y: -5, Button: "left", Keys: generation.Null[[]string]()}, raw: `{"type":"click","x":0,"y":-5,"button":"left","keys":null}`},
		{name: "double click", action: generation.ComputerDoubleClick{X: 4, Y: 8, Keys: generation.Null[[]string]()}, raw: `{"type":"double_click","x":4,"y":8,"keys":null}`},
		{name: "drag", action: generation.ComputerDrag{Path: []generation.ComputerPoint{{X: 0, Y: 0}, {X: 40, Y: 50}}, Keys: generation.Some([]string{"SHIFT"})}, raw: `{"type":"drag","path":[{"x":0,"y":0},{"x":40,"y":50}],"keys":["SHIFT"]}`},
		{name: "keypress", action: generation.ComputerKeypress{Keys: []string{"CTRL", "C"}}, raw: `{"type":"keypress","keys":["CTRL","C"]}`},
		{name: "move", action: generation.ComputerMove{X: 1, Y: 2, Keys: generation.Some([]string{})}, raw: `{"type":"move","x":1,"y":2,"keys":[]}`},
		{name: "screenshot", action: generation.ComputerScreenshot{}, raw: `{"type":"screenshot"}`},
		{name: "scroll", action: generation.ComputerScroll{X: 0, Y: 0, ScrollX: -20, ScrollY: 0}, raw: `{"type":"scroll","x":0,"y":0,"scroll_x":-20,"scroll_y":0}`},
		{name: "type", action: generation.ComputerType{Text: "привет\n"}, raw: `{"type":"type","text":"привет\n"}`},
		{name: "wait", action: generation.ComputerWait{}, raw: `{"type":"wait"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := generation.OpenAIComputerCall{ID: "cu_1", CallID: "call_1", Status: "completed", Action: tc.action, PendingSafetyChecks: []generation.ComputerSafetyCheck{}}
			want := `{"type":"computer_call","id":"cu_1","call_id":"call_1","status":"completed","action":` + tc.raw + `,"pending_safety_checks":[]}`

			body, err := wire.EncodeRequest(requestWith(call), false)

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[`+want+`]}`)

			result, err := computerResponse("[" + want + "]")

			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Response.Output[0], call) {
				t.Fatalf("decoded call = %#v; want %#v", result.Response.Output[0], call)
			}
			if result.Response.Finish.Reason != "tool_calls" {
				t.Fatalf("finish = %s", result.Response.Finish.Reason)
			}
		})
	}
}

func TestComputerActionPresenceAndSafetyChecks(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		action       generation.ComputerAction
		actions      []generation.ComputerAction
	}{
		{name: "omitted"},
		{name: "empty batch", fields: `,"actions":[]`, actions: []generation.ComputerAction{}},
		{name: "batch", fields: `,"actions":[{"type":"screenshot"},{"type":"type","text":""}]`, actions: []generation.ComputerAction{generation.ComputerScreenshot{}, generation.ComputerType{Text: ""}}},
		{name: "single and empty batch", fields: `,"action":{"type":"wait"},"actions":[]`, action: generation.ComputerWait{}, actions: []generation.ComputerAction{}},
		{name: "single and batch", fields: `,"action":{"type":"wait"},"actions":[{"type":"wait"}]`, action: generation.ComputerWait{}, actions: []generation.ComputerAction{generation.ComputerWait{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := `[{"type":"computer_call","id":"cu_1","call_id":"call_1","status":"in_progress","pending_safety_checks":[{"id":"check_1","code":null,"message":"Confirm"},{"id":"check_2","code":"sensitive","message":null},{"id":"check_3"}]` + tc.fields + `}]`

			result, err := computerResponse(output)

			if err != nil {
				t.Fatal(err)
			}
			call := result.Response.Output[0].(generation.OpenAIComputerCall)
			if !reflect.DeepEqual(call.Action, tc.action) || !reflect.DeepEqual(call.Actions, tc.actions) {
				t.Fatalf("actions = %#v / %#v; want %#v / %#v", call.Action, call.Actions, tc.action, tc.actions)
			}
			if !call.PendingSafetyChecks[0].Code.IsNull() || !call.PendingSafetyChecks[1].Message.IsNull() || !call.PendingSafetyChecks[2].Code.IsZero() {
				t.Fatal("lost safety check presence")
			}
			body, err := wire.EncodeRequest(requestWith(call), false)
			if err != nil {
				t.Fatal(err)
			}
			var replay struct {
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(body, &replay); err != nil {
				t.Fatal(err)
			}
			assertJSON(t, replay.Input, output)
			if strings.Contains(string(body), "acknowledged_safety_checks") {
				t.Fatal("pending checks were auto-acknowledged")
			}
		})
	}
}

func TestComputerScreenshotInputPresence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     generation.OpenAIComputerResult
		fields     string
		screenshot string
	}{
		{name: "omitted", result: generation.OpenAIComputerResult{CallID: "call_1"}, screenshot: `{"type":"computer_screenshot"}`},
		{name: "null metadata", result: generation.OpenAIComputerResult{CallID: "call_1", ID: generation.Null[string](), Status: generation.Null[string](), AcknowledgedSafetyChecks: generation.Null[[]generation.ComputerSafetyCheck]()}, fields: `,"id":null,"status":null,"acknowledged_safety_checks":null`, screenshot: `{"type":"computer_screenshot"}`},
		{name: "data image", result: generation.OpenAIComputerResult{CallID: "call_1", Output: generation.ComputerScreenshotOutput{ImageURL: generation.Some("data:image/png;base64,UEs="), Detail: generation.Some("original")}, AcknowledgedSafetyChecks: generation.Some([]generation.ComputerSafetyCheck{})}, fields: `,"acknowledged_safety_checks":[]`, screenshot: `{"type":"computer_screenshot","image_url":"data:image/png;base64,UEs=","detail":"original"}`},
		{name: "both references", result: generation.OpenAIComputerResult{CallID: "call_1", Output: generation.ComputerScreenshotOutput{FileID: generation.Some("file_1"), ImageURL: generation.Some("https://example.org/screenshot.png")}, AcknowledgedSafetyChecks: generation.Some([]generation.ComputerSafetyCheck{{ID: "check_1", Code: generation.Null[string](), Message: generation.Some("")}})}, fields: `,"acknowledged_safety_checks":[{"id":"check_1","code":null,"message":""}]`, screenshot: `{"type":"computer_screenshot","file_id":"file_1","image_url":"https://example.org/screenshot.png"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := wire.EncodeRequest(requestWith(tc.result), false)
			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"computer_call_output","call_id":"call_1","output":`+tc.screenshot+tc.fields+`}]}`)
		})
	}
}

func TestComputerResultMetadataAndReplay(t *testing.T) {
	output := `[{"type":"computer_call_output","id":"out_1","call_id":"call_1","status":"completed","output":{"type":"computer_screenshot","file_id":"file_1","detail":"original"},"acknowledged_safety_checks":[{"id":"check_1","code":null,"message":"Confirmed"}],"created_by":"actor"}]`
	result, err := computerResponse(output)
	if err != nil {
		t.Fatal(err)
	}
	got := result.Response.Output[0].(generation.OpenAIComputerResult)
	if actor, _ := got.CreatedBy.Value(); actor != "actor" {
		t.Fatalf("creator = %q", actor)
	}
	if !got.Output.ImageURL.IsZero() {
		t.Fatal("invented include-gated image URL")
	}
	checks, ok := got.AcknowledgedSafetyChecks.Value()
	if !ok || len(checks) != 1 || checks[0].ID != "check_1" || !checks[0].Code.IsNull() {
		t.Fatalf("checks = %#v", checks)
	}
	body, err := wire.EncodeRequest(requestWith(got), false)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"computer_call_output","id":"out_1","call_id":"call_1","status":"completed","output":{"type":"computer_screenshot","file_id":"file_1","detail":"original"},"acknowledged_safety_checks":[{"id":"check_1","code":null,"message":"Confirmed"}]}]}`)

	failed, err := computerResponse(strings.Replace(output, `"status":"completed"`, `"status":"failed"`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wire.EncodeRequest(requestWith(failed.Response.Output...), false); err == nil {
		t.Fatal("failed output status is not a supported input status")
	}
}

func TestRejectMalformedComputerResponses(t *testing.T) {
	call := `{"type":"computer_call","id":"cu_1","call_id":"call_1","status":"completed","pending_safety_checks":[],"action":{"type":"click","x":0,"y":0,"button":"left"}}`
	result := `{"type":"computer_call_output","id":"out_1","call_id":"call_1","status":"completed","output":{"type":"computer_screenshot"}}`
	for _, tc := range []struct{ name, raw string }{
		{name: "missing call id", raw: strings.Replace(call, `"call_id":"call_1",`, "", 1)},
		{name: "missing pending checks", raw: strings.Replace(call, `"pending_safety_checks":[],`, "", 1)},
		{name: "null pending checks", raw: strings.Replace(call, `"pending_safety_checks":[]`, `"pending_safety_checks":null`, 1)},
		{name: "null pending check", raw: strings.Replace(call, `"pending_safety_checks":[]`, `"pending_safety_checks":[null]`, 1)},
		{name: "check missing id", raw: strings.Replace(call, `"pending_safety_checks":[]`, `"pending_safety_checks":[{"code":"confirm"}]`, 1)},
		{name: "duplicate pending checks", raw: strings.Replace(call, `"pending_safety_checks":[]`, `"pending_safety_checks":[{"id":"check_1","code":"altered"},{"id":"check_1","code":"original"}]`, 1)},
		{name: "null coordinate", raw: strings.Replace(call, `"x":0`, `"x":null`, 1)},
		{name: "missing coordinate", raw: strings.Replace(call, `"x":0,`, "", 1)},
		{name: "fractional coordinate", raw: strings.Replace(call, `"x":0`, `"x":0.5`, 1)},
		{name: "invalid button", raw: strings.Replace(call, `"left"`, `"middle"`, 1)},
		{name: "null batch", raw: strings.Replace(call, `"pending_safety_checks":[]`, `"pending_safety_checks":[],"actions":null`, 1)},
		{name: "null batch element", raw: strings.Replace(call, `"pending_safety_checks":[]`, `"pending_safety_checks":[],"actions":[null]`, 1)},
		{name: "null keys entry", raw: strings.Replace(call, `"button":"left"`, `"button":"left","keys":[null]`, 1)},
		{name: "missing output", raw: strings.Replace(result, `,"output":{"type":"computer_screenshot"}`, "", 1)},
		{name: "null screenshot ref", raw: strings.Replace(result, `"computer_screenshot"`, `"computer_screenshot","file_id":null`, 1)},
		{name: "invalid screenshot URL", raw: strings.Replace(result, `"computer_screenshot"`, `"computer_screenshot","image_url":"file:///private"`, 1)},
		{name: "null acknowledged checks", raw: strings.Replace(result, `"status":"completed"`, `"status":"completed","acknowledged_safety_checks":null`, 1)},
		{name: "duplicate acknowledged checks", raw: strings.Replace(result, `"status":"completed"`, `"status":"completed","acknowledged_safety_checks":[{"id":"check_1","code":"altered"},{"id":"check_1","code":"original"}]`, 1)},
		{name: "missing result status", raw: strings.Replace(result, `"status":"completed",`, "", 1)},
		{name: "null result id", raw: strings.Replace(result, `"id":"out_1"`, `"id":null`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := computerResponse("[" + tc.raw + "]")
			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
		})
	}
}

func TestRejectMalformedComputerActions(t *testing.T) {
	for _, raw := range []string{
		`{"type":"double_click","x":0,"y":0}`,
		`{"type":"drag","path":[{"x":0}]}`,
		`{"type":"drag","path":null}`,
		`{"type":"keypress","keys":null}`,
		`{"type":"keypress","keys":[null]}`,
		`{"type":"scroll","x":0,"y":0,"scroll_x":0}`,
		`{"type":"type","text":null}`,
	} {
		output := `[{"type":"computer_call","id":"cu_1","call_id":"call_1","status":"completed","pending_safety_checks":[],"actions":[` + raw + `]}]`
		if _, err := computerResponse(output); err == nil {
			t.Fatalf("accepted malformed action %s", raw)
		}
	}
}

func TestRejectInvalidComputerInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		item   generation.Item
		tool   generation.Tool
		choice generation.ToolChoice
	}{
		{name: "missing id", item: generation.OpenAIComputerCall{CallID: "c", Status: "completed"}},
		{name: "call status", item: generation.OpenAIComputerCall{ID: "cu", CallID: "c", Status: "failed"}},
		{name: "batch nil element", item: generation.OpenAIComputerCall{ID: "cu", CallID: "c", Status: "completed", Actions: []generation.ComputerAction{nil}}},
		{name: "text UTF-8", item: generation.OpenAIComputerCall{ID: "cu", CallID: "c", Status: "completed", Action: generation.ComputerType{Text: "private\xff"}}},
		{name: "double click missing keys", item: generation.OpenAIComputerCall{ID: "cu", CallID: "c", Status: "completed", Action: generation.ComputerDoubleClick{}}},
		{name: "check id", item: generation.OpenAIComputerCall{ID: "cu", CallID: "c", Status: "completed", PendingSafetyChecks: []generation.ComputerSafetyCheck{{}}}},
		{name: "duplicate pending IDs", item: generation.OpenAIComputerCall{ID: "cu", CallID: "c", Status: "completed", PendingSafetyChecks: []generation.ComputerSafetyCheck{{ID: "check_1"}, {ID: "check_1", Code: generation.Some("altered")}}}},
		{name: "screenshot null", item: generation.OpenAIComputerResult{CallID: "c", Output: generation.ComputerScreenshotOutput{ImageURL: generation.Null[string]()}}},
		{name: "screenshot detail", item: generation.OpenAIComputerResult{CallID: "c", Output: generation.ComputerScreenshotOutput{Detail: generation.Some("huge")}}},
		{name: "acknowledgment id", item: generation.OpenAIComputerResult{CallID: "c", AcknowledgedSafetyChecks: generation.Some([]generation.ComputerSafetyCheck{{}})}},
		{name: "duplicate acknowledged IDs", item: generation.OpenAIComputerResult{CallID: "c", AcknowledgedSafetyChecks: generation.Some([]generation.ComputerSafetyCheck{{ID: "check_1"}, {ID: "check_1", Code: generation.Some("altered")}})}},
		{name: "preview dimensions", tool: generation.OpenAIComputerPreviewTool{DisplayWidth: 1920, Environment: "browser"}},
		{name: "preview environment", tool: generation.OpenAIComputerPreviewTool{DisplayWidth: 1920, DisplayHeight: 1080, Environment: "phone"}},
		{name: "choice", choice: generation.OpenAIComputerChoice("desktop")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			if tc.item != nil {
				request.Input = []generation.Item{tc.item}
			}
			if tc.tool != nil {
				request.Tools = []generation.Tool{tc.tool}
			}
			request.ToolChoice = tc.choice
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

func computerResponse(output string) (generation.Result, error) {
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":` + output + `}`))
	if err != nil {
		return generation.Result{}, err
	}
	return envelope.Result(func(string) {})
}
