package openai_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
)

func TestFunctionContentMissingSourcesNeverDispatch(t *testing.T) {
	for _, content := range []string{
		`{"type":"input_image","file_id":null,"image_url":null,"detail":null}`,
		`{"type":"input_file","file_id":null,"file_url":null,"file_data":null,"filename":null}`,
	} {
		t.Run(content, func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, responseWithItems(functionResultJSON("["+content+"]", "")))
			}, io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())
			if err != nil || len(result.Response.Output) != 1 {
				t.Fatalf("observed content = %#v, %v", result.Response.Output, err)
			}

			_, err = client.Generate(t.Context(), testAttempt(), generation.Request{Model: "test-model", Input: result.Response.Output})

			var input *openai.InputError
			if !errors.As(err, &input) || calls.Load() != 1 {
				t.Fatalf("replay error = %v, dispatches = %d", err, calls.Load())
			}
		})
	}
}

func TestStreamBoundsFunctionContentAcrossItems(t *testing.T) {
	large := strings.Repeat("x", 600_000)
	for _, content := range []struct{ name, value string }{
		{name: "text", value: `{"type":"input_text","text":"` + large + `"}`},
		{name: "image", value: `{"type":"input_image","file_id":"` + large + `","detail":null}`},
		{name: "file", value: `{"type":"input_file","file_id":"file_1","filename":"` + large + `","file_data":null}`},
	} {
		t.Run(content.name, func(t *testing.T) {
			first := functionResultJSON("["+content.value+"]", "")
			second := strings.Replace(first, `"id":"out_1"`, `"id":"out_2"`, 1)
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, first), outputItemAdded(1, second))

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}
