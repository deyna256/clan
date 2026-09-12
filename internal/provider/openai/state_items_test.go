package openai_test

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
)

func TestStreamCompactionSnapshots(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, `{"type":"compaction","id":"cmp_1"}`),
		outputItemAdded(0, `{"type":"compaction","id":"cmp_1","encrypted_content":"opaque","created_by":"creator_1"}`),
		`{"type":"response.completed","response":`+responseWithItems(`{"type":"compaction","id":"cmp_1"}`)+`}`,
	)
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.OpenAICompaction{ID: generation.Some("cmp_1")}},
		generation.ItemEnded{Index: 0, Item: generation.OpenAICompaction{ID: generation.Some("cmp_1"), EncryptedContent: generation.Some("opaque"), CreatedBy: generation.Some("creator_1")}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}},
	}

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) || !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, %v; want %#v", events, err, want)
	}
}

func TestStreamCompactionRejectsOpaqueChanges(t *testing.T) {
	for _, tc := range []struct{ name, first, next string }{
		{name: "ciphertext", first: `"opaque"`, next: `"opaque-extension"`},
		{name: "known empty", first: `""`, next: `"opaque"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, `{"type":"compaction","id":"cmp_1","encrypted_content":`+tc.first+`}`), outputItemAdded(0, `{"type":"compaction","id":"cmp_1","encrypted_content":`+tc.next+`}`))

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestCompactionRequiresTerminalCiphertext(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			body := `{"id":"resp_1","model":"test-model","status":"` + status + `","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"compaction","id":"cmp_1"}]}`
			ordinary := testClient(t, staticJSON(body), io.Discard)
			stream := streamClient(t, io.Discard, created(), `{"type":"response.`+status+`","response":`+body+`}`)

			_, ordinaryErr := ordinary.Generate(t.Context(), testAttempt(), textRequest())
			events, streamErr := readStream(t, stream)

			assertProtocolError(t, ordinaryErr)
			assertProtocolError(t, streamErr)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamCompactionPayloadBound(t *testing.T) {
	payload := strings.Repeat("x", 600_000)
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, `{"type":"compaction","id":"cmp_1","encrypted_content":"`+payload+`"}`),
		outputItemAdded(1, `{"type":"compaction","id":"cmp_2","encrypted_content":"`+payload+`"}`),
	)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestConfigurationUpdateIsNotGenerationOutput(t *testing.T) {
	client := testClient(t, staticJSON(responseWithItems(`{"type":"configuration_update","id":"cfg_1","reasoning":{"effort":"high"}}`)), io.Discard)

	_, err := client.Generate(t.Context(), testAttempt(), textRequest())

	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Kind != generation.Unsupported {
		t.Fatalf("Generate error = %v; want unsupported", err)
	}
}

func TestGenerateCompactionEmptyContent(t *testing.T) {
	client := testClient(t, staticJSON(responseWithItems(`{"type":"compaction","id":"cmp_1","encrypted_content":""}`)), io.Discard)
	want := []generation.Item{generation.OpenAICompaction{ID: generation.Some("cmp_1"), EncryptedContent: generation.Some("")}}

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	if err != nil || !reflect.DeepEqual(result.Response.Output, want) || result.Response.Finish.Reason != "stop" {
		t.Fatalf("Generate = %#v, %v; want %#v and stop", result.Response, err, want)
	}
}
