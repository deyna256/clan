package gateway_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

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
