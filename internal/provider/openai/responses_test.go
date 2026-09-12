package openai_test

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
)

func TestRetrieveResponsePreservesStoredState(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "in_progress", "queued", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/responses/resp_1" || r.URL.Query().Get("include_obfuscation") != "false" || !reflect.DeepEqual(r.URL.Query()["include[]"], []string{"reasoning.encrypted_content"}) {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, storedResponse(status, `[]`, `,"error":{"code":"server_error","message":"provider detail"},"incomplete_details":{}`))
			}, io.Discard)

			result, err := client.RetrieveResponse(t.Context(), testAttempt(), "resp_1", openai.ResponseRetrieveOptions{Include: []string{"reasoning.encrypted_content"}, IncludeObfuscation: generation.Some(false)})

			providerError, present := result.Error.Value()
			if err != nil || result.ID != "resp_1" || result.Model != "test-model" || result.Status != status || result.CreatedAt != 0 || len(result.Output) != 0 || !present || providerError.Code != "server_error" || !result.IncompleteReason.IsZero() || result.Usage.Input != count(12) || result.Usage.Output != count(3) {
				t.Fatalf("retrieved = %#v, %v", result, err)
			}
		})
	}
}

func TestStoredResponseValidatesTerminalContentAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		kind       generation.FailureKind
	}{
		{name: "wrong response", body: strings.ReplaceAll(storedResponse("completed", `[]`, ""), "resp_1", "resp_2"), kind: generation.ProtocolError},
		{name: "missing tool identity", body: storedResponse("completed", `[{"type":"function_call","id":"fc_1","arguments":"{}"}]`, ""), kind: generation.ProtocolError},
		{name: "invalid completed arguments", body: storedResponse("completed", `[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{"}]`, ""), kind: generation.ProtocolError},
		{name: "invalid error object", body: storedResponse("failed", `[]`, `,"error":{"code":"server_error"}`), kind: generation.ProtocolError},
		{name: "invalid incomplete reason", body: storedResponse("incomplete", `[]`, `,"incomplete_details":{"reason":null}`), kind: generation.ProtocolError},
		{name: "unsupported second item", body: storedResponse("completed", `[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{}"},{"type":"unknown"}]`, ""), kind: generation.Unsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, staticJSON(tc.body), io.Discard)

			result, err := client.RetrieveResponse(t.Context(), testAttempt(), "resp_1", openai.ResponseRetrieveOptions{})

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != tc.kind || result.Output != nil || result.Usage.Input != count(12) || result.Usage.Output != count(3) {
				t.Fatalf("result = %#v, %v; want usage without partial output", result, err)
			}
		})
	}
}

func TestStoredResponseRetainsUsageAfterMalformedMetadata(t *testing.T) {
	for _, tc := range []struct{ name, metadata string }{
		{name: "invalid creation time", metadata: `"created_at":"invalid"`},
		{name: "invalid incomplete details", metadata: `"created_at":0,"incomplete_details":42`},
		{name: "invalid error code", metadata: `"created_at":0,"error":{"code":true,"message":"private"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"resp_1","object":"response","model":"test-model","status":"completed","output":[],` + tc.metadata + `,"usage":{"input_tokens":12,"output_tokens":3}}`
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.RetrieveResponse(t.Context(), testAttempt(), "resp_1", openai.ResponseRetrieveOptions{})

			assertProtocolError(t, err)
			if result.Output != nil || result.Usage.Input != count(12) || result.Usage.Output != count(3) {
				t.Fatalf("result = %#v; want usage without output", result)
			}
		})
	}
}

func TestRetrieveResponseStreamIsBoundToRequestedResponse(t *testing.T) {
	for _, id := range []string{"resp_1", "resp_2"} {
		t.Run(id, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/responses/"+id || r.URL.Query().Get("stream") != "true" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, frame(created())+frame(`{"type":"response.completed","response":`+textResponse("Hello", `{"input_tokens":12,"output_tokens":3}`)+`}`))
			}, io.Discard)
			stream, err := client.RetrieveResponseStream(t.Context(), testAttempt(), id, openai.ResponseRetrieveOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()

			ended := false
			for err == nil {
				var event generation.Event
				event, err = stream.Next()
				if _, ok := event.(generation.ResponseEnded); ok {
					ended = true
				}
			}

			if id == "resp_1" {
				if !errors.Is(err, io.EOF) || !ended {
					t.Fatalf("end = %v, completed = %v", err, ended)
				}
			} else {
				assertProtocolError(t, err)
				if ended {
					t.Fatal("completed wrong response")
				}
			}
		})
	}
}

func TestCompactKeepsUsageWhenOutputFails(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/responses/compact" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"id":"cmp_1","object":"response.compaction","created_at":0,"output":[{"type":"compaction","id":"cmp_item_1","encrypted_content":"opaque"},{"type":"unknown"}],"usage":{"input_tokens":12,"output_tokens":3},"error":{"code":"rate_limit_exceeded"}}`)
			}, io.Discard)

			result, err := client.Compact(t.Context(), testAttempt(), generation.CompactRequest{Model: generation.Some("test-model")})

			if err == nil || result.Output != nil || result.Usage.Input != count(12) || result.Usage.Output != count(3) {
				t.Fatalf("compact = %#v, %v", result, err)
			}
			if status == http.StatusTooManyRequests {
				var failure *generation.Failure
				if !errors.As(err, &failure) || failure.Kind != generation.RateLimited {
					t.Fatalf("error = %v", err)
				}
			}
		})
	}
}

func TestCountInputTokensValidatesCounts(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int64
		valid      bool
	}{
		{name: "zero", body: `{"object":"response.input_tokens","input_tokens":0}`, valid: true},
		{name: "exact large integer", body: `{"object":"response.input_tokens","input_tokens":9007199254740993}`, want: 9007199254740993, valid: true},
		{name: "negative", body: `{"object":"response.input_tokens","input_tokens":-1}`},
		{name: "null", body: `{"object":"response.input_tokens","input_tokens":null}`},
		{name: "missing", body: `{"object":"response.input_tokens"}`},
		{name: "wrong object", body: `{"object":"other","input_tokens":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.Path != "/responses/input_tokens" || string(body) != `{}` {
					t.Errorf("request = %s %s %s", r.Method, r.URL, body)
				}
				_, _ = io.WriteString(w, tc.body)
			}, io.Discard)

			count, err := client.CountInputTokens(t.Context(), testAttempt(), generation.InputTokenRequest{})

			if (err == nil) != tc.valid || count != tc.want {
				t.Fatalf("count = %d, %v", count, err)
			}
		})
	}
}

func TestDeleteResponseAcceptsDocumentedSuccessBodies(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{name: "empty response", valid: true}, {name: "deleted", body: `{"id":"resp_1","object":"response","deleted":true}`, valid: true},
		{name: "wrong identity", body: `{"id":"resp_2","object":"response","deleted":true}`}, {name: "not deleted", body: `{"id":"resp_1","object":"response","deleted":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" || r.URL.Path != "/responses/resp_1" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				if tc.body == "" {
					w.WriteHeader(http.StatusNoContent)
				} else {
					_, _ = io.WriteString(w, tc.body)
				}
			}, io.Discard)

			err := client.DeleteResponse(t.Context(), testAttempt(), "resp_1")

			if (err == nil) != tc.valid {
				t.Fatalf("delete = %v", err)
			}
		})
	}
}

func TestListResponseInputItemsReturnsOnePage(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/responses/resp_1/input_items" || query.Get("after") != "item_1" || query.Get("limit") != "1" || query.Get("order") != "asc" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"type":"message","id":"msg_1","role":"user","content":[{"type":"input_text","text":"Hello"}]}],"first_id":"msg_1","last_id":"msg_1","has_more":true}`)
	}, io.Discard)

	page, err := client.ListResponseInputItems(t.Context(), testAttempt(), "resp_1", openai.ItemListOptions{After: "item_1", Limit: generation.Some(int64(1)), Order: "asc"})

	want := openai.ItemPage{Data: []generation.Item{generation.Message{ID: "msg_1", Role: generation.User, Parts: []generation.Part{generation.Text{Text: "Hello"}}}}, FirstID: "msg_1", LastID: "msg_1", HasMore: true}
	if err != nil || !reflect.DeepEqual(page, want) {
		t.Fatalf("page = %#v, %v; want %#v", page, err, want)
	}
}

func TestResponseQueriesRejectInvalidInputBeforeDispatch(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid query dispatched") }, io.Discard)
	for _, options := range []openai.ResponseRetrieveOptions{
		{Include: []string{""}}, {IncludeObfuscation: generation.Null[bool]()},
	} {
		_, err := client.RetrieveResponse(t.Context(), testAttempt(), "resp_1", options)
		var input *openai.InputError
		if !errors.As(err, &input) {
			t.Fatalf("retrieve error = %v", err)
		}

		_, err = client.RetrieveResponseStream(t.Context(), testAttempt(), "resp_1", options)
		if !errors.As(err, &input) {
			t.Fatalf("stream error = %v", err)
		}
	}
}

func storedResponse(status, output, extra string) string {
	return `{"id":"resp_1","object":"response","model":"test-model","created_at":0,"status":"` + status + `","output":` + output + `,"usage":{"input_tokens":12,"output_tokens":3}` + extra + `}`
}
