package codex_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codex"
)

func testAccount(t *testing.T, id string) account.Account {
	t.Helper()
	a, err := account.New(account.Identity{ID: account.ID(id), Name: id}, account.OAuthCredentials{
		ChatGPTAccountID: "chatgpt-" + id, AccessToken: "secret-" + id,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func testClient(t *testing.T, client *http.Client, baseURL string) *codex.Client {
	t.Helper()
	c, err := codex.NewClient(client, baseURL, "0.99.0")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func request(t *testing.T, body string) codex.Request {
	t.Helper()
	r, err := codex.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func assertJSON(t *testing.T, actual []byte, expected string) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatalf("invalid actual JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatalf("invalid expected JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("JSON mismatch\ngot: %s\nwant: %s", actual, expected)
	}
}

const completeSSE = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"

func TestRequestDispatchOwnsAndPreservesContent(t *testing.T) {
	input := []byte(`{
		"model":"gpt-test","instructions":"  Keep this exactly.\n","stream":false,"store":false,"background":false,
		"max_output_tokens":123,"prompt_cache_key":"session-1","parallel_tool_calls":false,
		"tools":[{"type":"function","name":"lookup","description":"find","strict":false,"parameters":{"type":"object","properties":{"temperature":{"type":"string"}},"custom":null}}],
		"tool_choice":{"type":"function","name":"lookup"},"reasoning":{"effort":"low","summary":"auto"},
		"include":["reasoning.encrypted_content"],"text":{"verbosity":"low","format":{"type":"json_schema","name":"Answer","description":"shape","strict":false,"schema":{"type":"object","properties":{"stream":{"type":"boolean"}}}}},
		"input":[
			{"role":"system","content":"system instructions"},{"role":"developer","content":"developer instructions"},
			{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="},{"type":"input_text","text":"what is this?"}]},
			{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"},
			{"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":"commentary","content":[{"type":"output_text","text":"Checking","annotations":[],"logprobs":[]}]},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{unfinished","status":"completed"},
			{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"found"},{"type":"input_image","image_url":"https://example.test/image.png","detail":"auto"}]}
		]}`)
	want := `{
		"model":"gpt-test","instructions":"  Keep this exactly.\n","stream":true,"store":false,"background":false,
		"prompt_cache_key":"session-1","parallel_tool_calls":false,
		"tools":[{"type":"function","name":"lookup","description":"find","strict":false,"parameters":{"type":"object","properties":{"temperature":{"type":"string"}},"custom":null}}],
		"tool_choice":{"type":"function","name":"lookup"},"reasoning":{"effort":"low","summary":"auto"},
		"include":["reasoning.encrypted_content"],"text":{"verbosity":"low","format":{"type":"json_schema","name":"Answer","description":"shape","strict":false,"schema":{"type":"object","properties":{"stream":{"type":"boolean"}}}}},
		"input":[
			{"role":"developer","content":"system instructions"},{"role":"developer","content":"developer instructions"},
			{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="},{"type":"input_text","text":"what is this?"}]},
			{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"},
			{"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":"commentary","content":[{"type":"output_text","text":"Checking","annotations":[],"logprobs":[]}]},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{unfinished","status":"completed"},
			{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"found"},{"type":"input_image","image_url":"https://example.test/image.png","detail":"auto"}]}
		]}`
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies <- data
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, completeSSE)
	}))
	defer server.Close()
	client := testClient(t, server.Client(), server.URL)
	r, err := codex.ParseRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	copy(input, bytes.Repeat([]byte("x"), len(input)))

	_, err = client.Generate(t.Context(), testAccount(t, "one"), r)

	if err != nil {
		t.Fatal(err)
	}
	if r.Model() != "gpt-test" || r.Streaming() || !r.OmittedMaxOutputTokens() {
		t.Errorf("incorrect request metadata")
	}
	assertJSON(t, <-bodies, want)
}

func TestRequestStringAndNullableControls(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{name: "string", input: `{"model":"gpt-test","input":"hi"}`, want: `{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true,"store":false}`},
		{name: "nullable", input: `{"model":"gpt-test","input":[],"max_output_tokens":null,"instructions":null,"parallel_tool_calls":null,"reasoning":{"effort":null,"summary":null},"text":{"verbosity":null,"format":{"type":"json_schema","name":"A","strict":null,"schema":{"custom":null}}},"tools":[{"type":"function","name":"f","description":null,"parameters":null,"strict":null}]}`, want: `{"model":"gpt-test","input":[],"stream":true,"store":false,"reasoning":{},"text":{"format":{"type":"json_schema","name":"A","schema":{"custom":null}}},"tools":[{"type":"function","name":"f"}]}`},
		{name: "nullable replay", input: `{"model":"gpt-test","input":[{"type":"reasoning","summary":[],"encrypted_content":null,"content":null,"status":null},{"role":"assistant","phase":null,"content":[{"type":"output_text","text":"done","annotations":[],"logprobs":null}]},{"type":"function_call_output","id":null,"status":null,"call_id":"c","output":"done"}],"include":[]}`, want: `{"model":"gpt-test","input":[{"type":"reasoning","summary":[],"encrypted_content":null,"content":null,"status":null},{"role":"assistant","phase":null,"content":[{"type":"output_text","text":"done","annotations":[],"logprobs":null}]},{"type":"function_call_output","id":null,"status":null,"call_id":"c","output":"done"}],"include":[],"stream":true,"store":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body []byte
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body, _ = io.ReadAll(r.Body)
				return httpResponse(200, "text/event-stream", completeSSE), nil
			})}, "https://example.test")
			r := request(t, tt.input)

			_, err := client.Generate(t.Context(), testAccount(t, "one"), r)

			if err != nil {
				t.Fatal(err)
			}
			if r.OmittedMaxOutputTokens() {
				t.Error("absent/null cap must not warn")
			}
			if err := r.ValidateModel(codex.Model{ID: "gpt-test"}); err != nil {
				t.Errorf("null controls activate capabilities: %v", err)
			}
			assertJSON(t, body, tt.want)
		})
	}
}

func TestRequestDoesNotHTMLEscapeContent(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{
			name:  "string input",
			input: `{"model":"m","input":"<tag>&value</tag>"}`,
			want:  `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<tag>&value</tag>"}]}],"stream":true,"store":false}`,
		},
		{
			name:  "nested content and schemas",
			input: `{"model":"m","input":[{"role":"user","content":"<&>"}],"instructions":"<&>","tools":[{"type":"function","name":"f","description":"<&>","parameters":{"description":"<&>"}}],"reasoning":{"effort":"<&>"},"text":{"format":{"type":"json_schema","name":"A","schema":{"description":"<&>"}}}}`,
			want:  `{"model":"m","input":[{"role":"user","content":"<&>"}],"instructions":"<&>","tools":[{"type":"function","name":"f","description":"<&>","parameters":{"description":"<&>"}}],"reasoning":{"effort":"<&>"},"text":{"format":{"type":"json_schema","name":"A","schema":{"description":"<&>"}}},"stream":true,"store":false}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var body []byte
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var err error
				body, err = io.ReadAll(r.Body)
				return httpResponse(200, "text/event-stream", completeSSE), err
			})}, "https://example.test")
			r := request(t, tt.input)

			_, err := client.Generate(t.Context(), testAccount(t, "one"), r)

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, body, tt.want)
			for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
				if strings.Contains(string(body), escaped) {
					t.Errorf("request contains HTML escape %s: %s", escaped, body)
				}
			}
		})
	}
}

func TestRequestRejectsUnsupportedAndMalformedFields(t *testing.T) {
	tests := []struct{ name, input, field string }{
		{name: "null request", input: `null`, field: "request"},
		{name: "missing model", input: `{}`, field: "model"},
		{name: "null model", input: `{"model":null,"input":[]}`, field: "model"},
		{name: "null input", input: `{"model":"m","input":null}`, field: "input"},
		{name: "stored response", input: `{"model":"m","input":"x","store":true}`, field: "store"},
		{name: "background", input: `{"model":"m","input":"x","background":true}`, field: "background"},
		{name: "stream type", input: `{"model":"m","input":"x","stream":"false"}`, field: "stream"},
		{name: "sampling", input: `{"model":"m","input":"x","temperature":1}`, field: "request.temperature"},
		{name: "zero cap", input: `{"model":"m","input":"x","max_output_tokens":0}`, field: "max_output_tokens"},
		{name: "fractional cap", input: `{"model":"m","input":"x","max_output_tokens":1.5}`, field: "max_output_tokens"},
		{name: "reasoning control", input: `{"model":"m","input":"x","reasoning":{"mode":"pro"}}`, field: "reasoning.mode"},
		{name: "schema type", input: `{"model":"m","input":"x","text":{"format":{"type":"json_schema","name":"A","schema":[]}}}`, field: "text.format.schema"},
		{name: "native tool", input: `{"model":"m","input":"x","tools":[{"type":"web_search"}]}`, field: "tools.type"},
		{name: "chat tool", input: `{"model":"m","input":"x","tools":[{"type":"function","function":{"name":"f"}}]}`, field: "tools.function"},
		{name: "unsupported include", input: `{"model":"m","input":"x","include":["usage"]}`, field: "include"},
		{name: "tool choice", input: `{"model":"m","input":"x","tool_choice":"any"}`, field: "tool_choice"},
		{name: "stored item", input: `{"model":"m","input":[{"type":"item_reference","id":"old"}]}`, field: "input[0].type"},
		{name: "tool role", input: `{"model":"m","input":[{"role":"tool","content":"x"}]}`, field: "input[0].role"},
		{name: "file id", input: `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","file_id":"f"}]}]}`, field: "input[0].content.file_id"},
		{name: "image data", input: `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,!!!"}]}]}`, field: "input[0].content.image_url"},
		{name: "argument type", input: `{"model":"m","input":[{"type":"function_call","name":"f","call_id":"c","arguments":{}}]}`, field: "input[0].arguments"},
		{name: "result type", input: `{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":{}}]}`, field: "input[0].output"},
		{name: "null tools", input: `{"model":"m","input":[],"tools":null}`, field: "tools"},
		{name: "null text", input: `{"model":"m","input":[],"text":null}`, field: "text"},
		{name: "null choice", input: `{"model":"m","input":[],"tool_choice":null}`, field: "tool_choice"},
		{name: "null format", input: `{"model":"m","input":[],"text":{"format":null}}`, field: "text.format"},
		{name: "null unsupported variant field", input: `{"model":"m","input":[],"text":{"format":{"type":"text","strict":null}}}`, field: "text.format.strict"},
		{name: "null schema description", input: `{"model":"m","input":[],"text":{"format":{"type":"json_schema","name":"A","schema":{},"description":null}}}`, field: "text.format.description"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codex.ParseRequest([]byte(tt.input))

			var invalid *codex.ValidationError
			if !errors.As(err, &invalid) || invalid.Field != tt.field {
				t.Fatalf("got %v, want ValidationError field %s", err, tt.field)
			}
		})
	}
}

func TestRequestValidatesAccountModelCapabilities(t *testing.T) {
	tests := []struct {
		name, input string
		field       string
		model       codex.Model
		valid       bool
	}{
		{name: "wrong model", field: "model", input: `{"model":"m","input":"hi"}`, model: codex.Model{ID: "other"}},
		{name: "effort", field: "reasoning.effort", input: `{"model":"m","input":"hi","reasoning":{"effort":"high"}}`, model: codex.Model{ID: "m", ReasoningEfforts: []string{"low"}}},
		{name: "summary", field: "reasoning.summary", input: `{"model":"m","input":"hi","reasoning":{"summary":"auto"}}`, model: codex.Model{ID: "m"}},
		{name: "verbosity without format", input: `{"model":"m","input":"hi","text":{"verbosity":"low"}}`, model: codex.Model{ID: "m", SupportsVerbosity: true}, valid: true},
		{name: "image in result", field: "input.image", input: `{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","image_url":"https://example.test/i"}]}]}`, model: codex.Model{ID: "m", InputModalities: []string{"text"}}},
		{name: "original image", field: "input.image.detail", input: `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/i","detail":"original"}]}]}`, model: codex.Model{ID: "m", InputModalities: []string{"text", "image"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := request(t, tt.input)

			err := r.ValidateModel(tt.model)

			if (err == nil) != tt.valid {
				t.Fatalf("ValidateModel = %v, want valid %v", err, tt.valid)
			}
			if !tt.valid {
				var invalid *codex.ValidationError
				if !errors.As(err, &invalid) || invalid.Field != tt.field {
					t.Errorf("wrong capability error: %v, want %s", err, tt.field)
				}
			}
		})
	}
}

func TestRequestPreservesSupportedChoicesAndFormats(t *testing.T) {
	for _, tt := range []struct{ choice, format string }{
		{choice: "auto", format: "text"}, {choice: "none", format: "json_object"}, {choice: "required", format: "text"},
	} {
		t.Run(tt.choice, func(t *testing.T) {
			input := `{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":"plain result"}],"stream":true,"store":false,"tool_choice":"` + tt.choice + `","text":{"format":{"type":"` + tt.format + `"}}}`
			var body []byte
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body, _ = io.ReadAll(r.Body)
				return httpResponse(200, "text/event-stream", completeSSE), nil
			})}, "https://example.test")
			r := request(t, input)

			_, err := client.Generate(t.Context(), testAccount(t, "one"), r)

			if err != nil {
				t.Fatal(err)
			}
			if !r.Streaming() {
				t.Error("client stream preference lost")
			}
			assertJSON(t, body, input)
		})
	}
}
