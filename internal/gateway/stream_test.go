package gateway_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestStreamingConcurrencyRejectionReturnsJSON(t *testing.T) {
	f := newFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("rejected request reached upstream") })
	if err := f.executor.SetConcurrency(t.Context(), "key", 0); err != nil {
		t.Fatal(err)
	}

	resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("rejection = %d, %v, %s; want HTTP 429 JSON", resp.StatusCode, resp.Header, body)
	}
	var envelope struct{ Error struct{ Type, Code string } }
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("rejection body = %q: %v", body, err)
	}
	if envelope.Error.Type != "rate_limit_error" || envelope.Error.Code != "concurrency_limit" {
		t.Fatalf("rejection error = %+v", envelope.Error)
	}
}

func TestStreamingFlushesBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"hello\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			fmt.Fprintf(w, "data: %s\n\n", completed)
		case <-r.Context().Done():
		}
	})

	resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
	reader := bufio.NewReader(resp.Body)
	first := readEvent(t, reader)

	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || first["delta"] != "hello" {
		t.Fatalf("incremental response = %d, %v, %v", resp.StatusCode, resp.Header, first)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("stream does not disable proxy buffering")
	}
	unblock()
	last := readEvent(t, reader)
	if last["type"] != "response.completed" {
		t.Fatalf("terminal event = %v", last)
	}
	if rest, err := io.ReadAll(reader); err != nil || strings.TrimSpace(string(rest)) != "" {
		t.Fatalf("bytes after terminal = %q, %v", rest, err)
	}
}

func TestStreamingInterruptionEmitsOneSequencedError(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"sequence_number\":41,\"response\":{\"id\":\"r\",\"usage\":{\"input_tokens\":7}}}\n\n")
		w.(http.Flusher).Flush()
	})

	resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
	reader := bufio.NewReader(resp.Body)
	first, last := readEvent(t, reader), readEvent(t, reader)

	if resp.StatusCode != 200 || first["type"] != "response.created" || last["type"] != "error" || last["sequence_number"] != float64(42) {
		t.Fatalf("interrupted stream = %d, %v, %v", resp.StatusCode, first, last)
	}
	if last["code"] == nil || last["message"] == nil || last["error"] != nil {
		t.Fatalf("error must use native Responses shape: %v", last)
	}
	if rest, err := io.ReadAll(reader); err != nil || strings.TrimSpace(string(rest)) != "" {
		t.Fatalf("bytes after interruption error = %q, %v", rest, err)
	}
}

func TestStreamingTerminalFailureIsNotDuplicated(t *testing.T) {
	for _, tt := range []struct{ name, event string }{
		{name: "response.failed", event: `{"type":"response.failed","sequence_number":7,"response":{"id":"r","status":"failed","output":[],"error":{"code":"server_error","message":"secret-provider-detail"}}}`},
		{name: "error", event: `{"type":"error","sequence_number":7,"code":"server_error","message":"secret-provider-detail"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "data: %s\n\n", tt.event) })

			resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
			reader := bufio.NewReader(resp.Body)
			event := readEvent(t, reader)

			if resp.StatusCode != 200 || event["type"] != tt.name || event["sequence_number"] != float64(7) {
				t.Fatalf("terminal failure = %d, %v", resp.StatusCode, event)
			}
			encoded, err := json.Marshal(event)
			if err != nil || strings.Contains(string(encoded), "secret-provider-detail") {
				t.Fatalf("unsafe terminal event = %s, %v", encoded, err)
			}
			if rest, err := io.ReadAll(reader); err != nil || strings.TrimSpace(string(rest)) != "" {
				t.Fatalf("duplicate terminal data = %q, %v", rest, err)
			}
		})
	}
}

func TestStreamingIncompleteResponsePreservesReason(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `data: {"type":"response.incomplete","sequence_number":7,`+
			`"response":{"id":"r","status":"incomplete","output":[],`+
			`"incomplete_details":{"reason":"max_output_tokens"}}}`+"\n\n")
	})

	resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
	reader := bufio.NewReader(resp.Body)
	event := readEvent(t, reader)

	if resp.StatusCode != http.StatusOK || event["type"] != "response.incomplete" || event["sequence_number"] != float64(7) {
		t.Fatalf("incomplete event = %d, %v", resp.StatusCode, event)
	}
	response, ok := event["response"].(map[string]any)
	if !ok || response["status"] != "incomplete" {
		t.Fatalf("incomplete response = %v", event["response"])
	}
	details, ok := response["incomplete_details"].(map[string]any)
	if !ok || details["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete details = %v", response["incomplete_details"])
	}
	if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
		t.Fatalf("bytes after incomplete event = %q, %v", rest, err)
	}
}

func TestStreamingCleanupFailureReplacesCompletion(t *testing.T) {
	for _, tt := range []struct {
		name    string
		started bool
		want    int
	}{
		{name: "before first event", want: 502},
		{name: "after first event", started: true, want: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixtureWithTransport(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.started {
					fmt.Fprint(w, `data: {"type":"response.created","sequence_number":1}`+"\n\n")
				}
				fmt.Fprintf(w, "data: %s\n\n", completed)
			}, failResponseCleanup)

			resp := f.request(t, "POST", "/v1/responses", `{"model":"model","input":"hello","stream":true}`)
			body := readBody(t, resp)
			f.executor.Close()

			if resp.StatusCode != tt.want {
				t.Fatalf("cleanup failure response = %d, %s; want %d", resp.StatusCode, body, tt.want)
			}
			logs := f.logs.String()
			if strings.Contains(body+logs, "private-close-detail") || strings.Contains(body, "response.completed") {
				t.Fatalf("cleanup failure leaked or reported completion: body=%s, logs=%s", body, logs)
			}
			for _, field := range []string{`"result":"transport_failure"`, `"input_tokens":7`, `"output_tokens":3`, `"total_tokens":10`} {
				if !strings.Contains(logs, field) {
					t.Errorf("cleanup failure logs lack %s: %s", field, logs)
				}
			}
			if !tt.started {
				var envelope struct{ Error struct{ Code string } }
				if err := json.Unmarshal([]byte(body), &envelope); err != nil || envelope.Error.Code != "upstream_error" {
					t.Fatalf("precommit cleanup error = %s, %v", body, err)
				}
				if resp.Header.Get("X-Should-Retry") != "false" {
					t.Fatal("precommit cleanup error lacks retry suppression")
				}
				return
			}
			reader := bufio.NewReader(strings.NewReader(body))
			first, last := readEvent(t, reader), readEvent(t, reader)
			if first["type"] != "response.created" || last["type"] != "error" || last["code"] != "upstream_error" || last["sequence_number"] != float64(2) {
				t.Fatalf("postcommit cleanup failure events = %v, %v", first, last)
			}
			if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
				t.Fatalf("bytes after cleanup error = %q, %v", rest, err)
			}
		})
	}
}

func TestStreamingWriteFailureStopsDelivery(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"sequence_number\":1}\n\n")
		fmt.Fprintf(w, "data: %s\n\n", completed)
	})
	w := &failingEventWriter{accountingWriter: &accountingWriter{ResponseRecorder: httptest.NewRecorder()}}
	body := strings.NewReader(`{"model":"model","input":"hello","stream":true}`)
	r := httptest.NewRequest("POST", "/v1/responses", body).WithContext(t.Context())
	r.Header.Set("Authorization", "Bearer "+f.key)
	r.Header.Set("Content-Type", "application/json")

	f.handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response = %d, %v", w.Code, w.Header())
	}
	if w.writes != 1 {
		t.Fatalf("write attempts = %d, want 1 with no further events after failure", w.writes)
	}
	row, err := f.store.GetRequest(t.Context(), w.Header().Get("X-Request-ID"))
	if err != nil || row.Result != "delivery_failed" || !row.ResponseStarted {
		t.Fatalf("write failure record = %+v, error = %v", row, err)
	}
}

func TestStreamingFlushFailureStopsDelivery(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"sequence_number\":1}\n\n")
		fmt.Fprintf(w, "data: %s\n\n", completed)
	})
	w := &failingFlushWriter{accountingWriter: &accountingWriter{ResponseRecorder: httptest.NewRecorder()}}
	body := strings.NewReader(`{"model":"model","input":"hello","stream":true}`)
	r := httptest.NewRequest("POST", "/v1/responses", body).WithContext(t.Context())
	r.Header.Set("Authorization", "Bearer "+f.key)
	r.Header.Set("Content-Type", "application/json")

	f.handler.ServeHTTP(w, r)

	reader := bufio.NewReader(w.Body)
	if event := readEvent(t, reader); event["type"] != "response.created" {
		t.Fatalf("event written before flush = %v", event)
	}
	if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
		t.Fatalf("bytes after failed flush = %q, %v", rest, err)
	}
	row, err := f.store.GetRequest(t.Context(), w.Header().Get("X-Request-ID"))
	if err != nil || row.Result != "delivery_failed" || !row.ResponseStarted {
		t.Fatalf("flush failure record = %+v, error = %v", row, err)
	}
}

type failingEventWriter struct {
	*accountingWriter
	writes int
}

func (w *failingEventWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, io.ErrClosedPipe
}

type failingFlushWriter struct{ *accountingWriter }

func (*failingFlushWriter) FlushError() error { return io.ErrClosedPipe }

func readEvent(t *testing.T, reader *bufio.Reader) map[string]any {
	t.Helper()
	var kind, data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE event: %v", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" && data != "" {
			break
		}
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			kind = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data += strings.TrimSpace(value)
		}
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatalf("SSE data = %q: %v", data, err)
	}
	if kind != event["type"] {
		t.Fatalf("SSE event name %q differs from payload %v", kind, event["type"])
	}
	return event
}
