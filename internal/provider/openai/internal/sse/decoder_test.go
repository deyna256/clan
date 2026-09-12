package sse_test

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/deyna256/clan/internal/provider/openai/internal/sse"
)

func TestDecodeFrames(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []sse.Event
	}{
		{
			name: "multiline and fragmented unicode",
			input: "\ufeff: heartbeat\nevent: response.output_text.delta\n" +
				"data: {\ndata: \"delta\":\"Привет 👋\"}\n\n",
			want: []sse.Event{{Type: "response.output_text.delta", Data: "{\n\"delta\":\"Привет 👋\"}"}},
		},
		{
			name:  "mixed line endings",
			input: "data: first\r\rdata: second\r\n\r\ndata: third\n\n",
			want: []sse.Event{
				{Type: "message", Data: "first"},
				{Type: "message", Data: "second"},
				{Type: "message", Data: "third"},
			},
		},
		{
			name: "ignore control fields and reset event names",
			input: "event: unused\nid: 7\nretry: 100\n: ping\n\n" +
				"event: original\nevent: replacement\ndata: a:b\n\ndata: next\n\n",
			want: []sse.Event{{Type: "replacement", Data: "a:b"}, {Type: "message", Data: "next"}},
		},
		{
			name:  "empty data and single optional space",
			input: "data\n\nevent:\ndata:  x\ndata:\n\n",
			want:  []sse.Event{{Type: "message"}, {Type: "message", Data: " x\n"}},
		},
		{
			name:  "discard unterminated frame",
			input: "data: complete\n\ndata: unfinished\n",
			want:  []sse.Event{{Type: "message", Data: "complete"}},
		},
		{
			name:  "discard unterminated line",
			input: "data: unfinished",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoder := newDecoder(t, iotest.OneByteReader(strings.NewReader(tt.input)), 1024)

			got, err := readEvents(decoder)

			if !errors.Is(err, io.EOF) || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("events = %#v, %v; want %#v, EOF", got, err, tt.want)
			}
		})
	}
}

func TestFrameLimit(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		limit  int
		want   error
		events []sse.Event
	}{
		{name: "exact limit", input: "data: x\n\n", limit: 9, want: io.EOF, events: []sse.Event{{Type: "message", Data: "x"}}},
		{name: "oversized line", input: "data: xx\n\n", limit: 9, want: sse.ErrTooLarge},
		{name: "accumulated data", input: "data: a\ndata: b\n\n", limit: 10, want: sse.ErrTooLarge},
		{name: "ignored field", input: ": long comment\n\ndata: x\n\n", limit: 9, want: sse.ErrTooLarge},
		{name: "limit resets per frame", input: "data: x\n\ndata: y\n\n", limit: 9, want: io.EOF, events: []sse.Event{{Type: "message", Data: "x"}, {Type: "message", Data: "y"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoder := newDecoder(t, strings.NewReader(tt.input), tt.limit)

			events, err := readEvents(decoder)

			if !errors.Is(err, tt.want) || !reflect.DeepEqual(events, tt.events) {
				t.Fatalf("events = %#v, %v; want %#v, %v", events, err, tt.events, tt.want)
			}
		})
	}
}

func TestReadFailureIsTerminal(t *testing.T) {
	want := errors.New("broken connection")
	reader := &failingReader{err: want}
	decoder := newDecoder(t, io.MultiReader(strings.NewReader("data: partial\n"), reader), 1024)

	event, err := decoder.Next()
	again, nextErr := decoder.Next()

	if event != (sse.Event{}) || again != (sse.Event{}) || !errors.Is(err, want) || !errors.Is(nextErr, want) {
		t.Fatalf("Next results = %#v, %v then %#v, %v; want zero events and %v", event, err, again, nextErr, want)
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d; want 1", reader.calls)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		reader io.Reader
		limit  int
	}{
		{name: "missing reader", limit: 1024},
		{name: "zero limit", reader: strings.NewReader("")},
		{name: "negative limit", reader: strings.NewReader(""), limit: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoder, err := sse.New(tt.reader, tt.limit)

			if decoder != nil || err == nil {
				t.Fatalf("New = %v, %v; want nil decoder and error", decoder, err)
			}
		})
	}
}

func TestCarriageReturnDispatchesWithoutReadingAhead(t *testing.T) {
	reader := &failingReader{err: errors.New("read beyond complete frame")}
	decoder := newDecoder(t, io.MultiReader(strings.NewReader("data: ready\r\r"), reader), 1024)

	event, err := decoder.Next()

	if err != nil || event != (sse.Event{Type: "message", Data: "ready"}) || reader.calls != 0 {
		t.Fatalf("Next = %#v, %v; subsequent reads = %d", event, err, reader.calls)
	}
}

func FuzzFragmentation(f *testing.F) {
	f.Add("data: hello\n\n")
	f.Add("\ufeffdata: 👋\r\r")
	f.Add("data: incomplete")
	f.Fuzz(func(t *testing.T, input string) {
		whole := newDecoder(t, strings.NewReader(input), 4096)
		fragmented := newDecoder(t, iotest.OneByteReader(strings.NewReader(input)), 4096)

		want, wantErr := readEvents(whole)
		got, err := readEvents(fragmented)

		if !reflect.DeepEqual(got, want) || !errors.Is(err, wantErr) {
			t.Fatalf("fragmentation changed events or error: %#v, %v versus %#v, %v", got, err, want, wantErr)
		}
	})
}

func newDecoder(t *testing.T, reader io.Reader, limit int) *sse.Decoder {
	t.Helper()
	decoder, err := sse.New(reader, limit)
	if err != nil {
		t.Fatal(err)
	}
	return decoder
}

func readEvents(decoder *sse.Decoder) ([]sse.Event, error) {
	var events []sse.Event
	for {
		event, err := decoder.Next()
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

type failingReader struct {
	err   error
	calls int
}

func (r *failingReader) Read([]byte) (int, error) {
	r.calls++
	return 0, r.err
}
