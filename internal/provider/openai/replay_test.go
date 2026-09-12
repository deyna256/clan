package openai_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/stretchr/testify/require"
)

func TestCompactedMediaCanBeGenerated(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		switch r.URL.Path {
		case "/responses/compact":
			_, _ = io.WriteString(w, `{"id":"compact_1","object":"response.compaction","created_at":1,"output":[
				{"type":"message","id":"msg_1","role":"user","status":"completed","content":[
					{"type":"input_text","text":"Read this","prompt_cache_breakpoint":{"mode":"explicit"}},
					{"type":"input_image","detail":"original","file_id":null,"image_url":"https://example.com/image.png","prompt_cache_breakpoint":{"mode":"explicit"}},
					{"type":"input_file","file_id":"file_report","detail":"high"}
				]},
				{"type":"compaction","id":"cmp_1","encrypted_content":"opaque"}
			],"usage":{"input_tokens":20,"output_tokens":4,"total_tokens":24}}`)
		case "/responses":
			body, _ := io.ReadAll(r.Body)
			var got, want any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Error(err)
			}
			_ = json.Unmarshal([]byte(`{"model":"test-model","stream":false,"input":[
				{"type":"message","id":"msg_1","role":"user","status":"completed","content":[
					{"type":"input_text","text":"Read this","prompt_cache_breakpoint":{"mode":"explicit"}},
					{"type":"input_image","detail":"original","file_id":null,"image_url":"https://example.com/image.png","prompt_cache_breakpoint":{"mode":"explicit"}},
					{"type":"input_file","file_id":"file_report","detail":"high"}
				]},
				{"type":"compaction","id":"cmp_1","encrypted_content":"opaque"}
			]}`), &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("replayed request = %s", body)
			}
			_, _ = io.WriteString(w, textResponse("Read", `{"input_tokens":5,"output_tokens":1,"total_tokens":6}`))
		default:
			t.Errorf("path = %s", r.URL.Path)
		}
	}, io.Discard)
	compacted, err := client.Compact(t.Context(), testAttempt(), generation.CompactRequest{Model: generation.Some("test-model")})
	require.NoError(t, err)
	result, err := client.Generate(t.Context(), testAttempt(), generation.Request{Model: "test-model", Input: compacted.Output})
	if err != nil || len(result.Response.Output) != 1 || calls.Load() != 2 {
		t.Fatalf("generate = %#v, %v, calls=%d", result, err, calls.Load())
	}
}

func TestStoredMediaMissingReferencesNeverDispatch(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("unusable stored content dispatched") }, io.Discard)
	for _, part := range []generation.Part{
		generation.OpenAIStoredImage{Kind: "input_image", Detail: "auto", FileID: generation.Null[string](), ImageURL: generation.Null[string]()},
		generation.OpenAIStoredFile{FileID: generation.Null[string]()},
	} {
		request := generation.Request{Model: "test-model", Input: []generation.Item{generation.Message{Role: generation.User, Parts: []generation.Part{part}}}}
		_, err := client.Generate(t.Context(), testAttempt(), request)
		var input *openai.InputError
		if !errors.As(err, &input) {
			t.Fatalf("error = %v", err)
		}
	}
}
