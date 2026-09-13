package gateway_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNativeErrorsUseSafeClientCodesAndSequences(t *testing.T) {
	for _, tt := range []struct {
		name, code, sequence, wantCode string
		wantSequence                   float64
	}{
		{name: "missing sequence", code: "server_error", wantCode: "upstream_error", wantSequence: 42},
		{name: "invalid sequence", code: "server_error", sequence: `,"sequence_number":"bad"`, wantCode: "upstream_error", wantSequence: 42},
		{name: "invalid request", code: "invalid_prompt", sequence: `,"sequence_number":45`, wantCode: "invalid_request", wantSequence: 45},
		{name: "provider limit", code: "rate_limit_exceeded", wantCode: "rate_limit_exceeded", wantSequence: 42},
		{name: "account unavailable", code: "usage_not_included", wantCode: "service_unavailable", wantSequence: 42},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "data: {\"type\":\"response.created\",\"sequence_number\":41}\n\n")
				fmt.Fprintf(w, "data: {\"type\":\"error\",\"code\":%q,\"message\":\"private provider message\"%s}\n\n", tt.code, tt.sequence)
			})

			resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
			reader := bufio.NewReader(resp.Body)
			readEvent(t, reader)
			event := readEvent(t, reader)

			if resp.StatusCode != 200 || event["type"] != "error" || event["code"] != tt.wantCode || event["sequence_number"] != tt.wantSequence {
				t.Fatalf("native error = %d, %v", resp.StatusCode, event)
			}
			if event["message"] == "private provider message" || event["param"] != nil {
				t.Fatalf("unsafe error fields: %v", event)
			}
			if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
				t.Fatalf("data after error = %q, %v", rest, err)
			}
		})
	}
}

func TestFailedResponsesNormalizeErrorsAndPreserveResponseFields(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `data: {"type":"response.failed","sequence_number":-1,"response":{"id":"r","status":"failed","output":[{"type":"message","id":"item","content":[]}],"usage":{"input_tokens":7},"future_field":{"value":9007199254740993},"error":{"code":"rate_limit_exceeded","message":"private provider message"}}}`+"\n\n")
			})

			resp := f.request(t, "POST", "/v1/responses", fmt.Sprintf(`{"model":"model","input":"hello","stream":%t}`, stream))
			body := readBody(t, resp)
			if resp.StatusCode != 200 || strings.Contains(body, "private provider message") {
				t.Fatalf("failed response = %d, %s", resp.StatusCode, body)
			}
			var raw json.RawMessage = []byte(body)
			if stream {
				event := readEvent(t, bufio.NewReader(strings.NewReader(body)))
				if event["type"] != "response.failed" || event["sequence_number"] != float64(0) {
					t.Fatalf("failed event = %v", event)
				}
				_, data, _ := strings.Cut(body, "data: ")
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &fields); err != nil {
					t.Fatal(err)
				}
				raw = fields["response"]
			}
			var response struct {
				ID, Status string
				Output     []json.RawMessage
				Usage      struct {
					Input int `json:"input_tokens"`
				}
				Future struct{ Value int64 } `json:"future_field"`
				Error  struct{ Code string }
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != "r" || response.Status != "failed" || len(response.Output) != 1 || response.Usage.Input != 7 || response.Future.Value != 9007199254740993 || response.Error.Code != "rate_limit_exceeded" {
				t.Fatalf("response fields changed: %+v", response)
			}
		})
	}
}

func TestMalformedEventMetadataProducesOneSafeError(t *testing.T) {
	for _, tt := range []struct{ name, event string }{
		{name: "newline type", event: `{"type":"future\nevent"}`},
		{name: "string sequence", event: `{"type":"future.event","sequence_number":"secret"}`},
		{name: "negative sequence", event: `{"type":"future.event","sequence_number":-1}`},
		{name: "null sequence", event: `{"type":"future.event","sequence_number":null}`},
		{name: "overflow sequence", event: `{"type":"future.event","sequence_number":9223372036854775808}`},
		{name: "malformed completed sequence", event: `{"type":"response.completed","sequence_number":"bad","response":{"id":"r","status":"completed","output":[]}}`},
	} {
		for _, started := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/started=%t", tt.name, started), func(t *testing.T) {
				f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
					if started {
						fmt.Fprint(w, "data: {\"type\":\"future.event\",\"sequence_number\":41,\"future\":9007199254740993}\n\n")
					}
					fmt.Fprintf(w, "data: %s\n\n", tt.event)
				})

				resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
				body := readBody(t, resp)
				f.executor.Close()
				if logs := f.logs.String(); strings.Contains(logs, `"result":"completed"`) || !strings.Contains(logs, `"result":"delivery_failed"`) {
					t.Fatalf("metadata rejection was not recorded as a delivery failure: %s", logs)
				}

				if !started {
					if resp.StatusCode != 502 || !strings.Contains(body, `"code":"upstream_error"`) {
						t.Fatalf("precommit error = %d, %s", resp.StatusCode, body)
					}
					return
				}
				reader := bufio.NewReader(strings.NewReader(body))
				readEvent(t, reader)
				event := readEvent(t, reader)
				if resp.StatusCode != 200 || event["type"] != "error" || event["code"] != "upstream_error" || event["sequence_number"] != float64(42) || !strings.Contains(body, "9007199254740993") {
					t.Fatalf("postcommit error = %d, %s", resp.StatusCode, body)
				}
				if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
					t.Fatalf("data after error = %q, %v", rest, err)
				}
			})
		}
	}
}

func TestErrorSequenceDoesNotOverflow(t *testing.T) {
	for _, tt := range []struct {
		name, previous string
		wantError      bool
	}{
		{name: "last representable sequence", previous: "9223372036854775806", wantError: true},
		{name: "exhausted sequence", previous: "9223372036854775807", wantError: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, "data: {\"type\":\"future.event\",\"sequence_number\":%s}\n\n", tt.previous)
				fmt.Fprint(w, "data: {\"type\":\"error\",\"code\":\"server_error\"}\n\n")
			})

			resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
			body := readBody(t, resp)

			if resp.StatusCode != 200 || strings.Contains(body, `"sequence_number":-`) || strings.Contains(body, "9223372036854775808") {
				t.Fatalf("invalid sequence = %d, %s", resp.StatusCode, body)
			}
			if got := strings.Contains(body, `"type":"error"`); got != tt.wantError {
				t.Fatalf("error present = %t, want %t: %s", got, tt.wantError, body)
			}
			if tt.wantError && !strings.Contains(body, `"sequence_number":9223372036854775807`) {
				t.Fatalf("missing final sequence: %s", body)
			}
		})
	}
}
