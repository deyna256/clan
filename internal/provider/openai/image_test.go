package openai_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/stretchr/testify/require"
)

func TestGenerateImagePreservesPayloadAndOptionalMetadata(t *testing.T) {
	var logs bytes.Buffer
	call := imageCallJSON("ig_1", "completed", `"aW1hZ2U="`, `,"action":"edit","background":null,"output_format":"png","quality":{"private":"data"},"size":"1536x864","revised_prompt":"Revised"`)
	client := testClient(t, staticJSON(imageResponse(call)), &logs)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	require.NoError(t, err)
	want := []generation.Item{generation.OpenAIImageGenerationCall{ID: "ig_1", Status: "completed", Result: generation.Some("aW1hZ2U="), Metadata: generation.ImageGenerationMetadata{Action: generation.Some("edit"), Background: generation.Null[string](), OutputFormat: generation.Some("png"), Size: generation.Some("1536x864"), RevisedPrompt: generation.Some("Revised")}}}
	require.Equal(t, want, result.Response.Output)
	require.Equal(t, "stop", result.Response.Finish.Reason)
	if !strings.Contains(logs.String(), "invalid_image_metadata") || strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), "aW1hZ2U=") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestGenerateRejectsMissingOrInvalidCompletedImage(t *testing.T) {
	for _, value := range []string{`null`, `""`, `"\r\n"`, `"private!"`} {
		t.Run(value, func(t *testing.T) {
			client := testClient(t, staticJSON(imageResponse(imageCallJSON("ig_1", "completed", value, ""))), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			assertProtocolError(t, err)
			if result.Usage.Output != count(8) {
				t.Fatalf("usage = %#v; want 8 output tokens", result.Usage)
			}
		})
	}
}

func TestStreamImagePreviewsInterleaveWithoutBecomingFinalResults(t *testing.T) {
	var logs bytes.Buffer
	client := streamClient(t, &logs, created(), imageAdded(0, "ig_1"), imageAdded(1, "ig_2"),
		imagePreviewJSON(1, "ig_2", 0, "cHJldmlldzI="),
		imagePreviewJSON(0, "ig_1", 0, "cHJldmlldzE="),
		`{"type":"response.image_generation_call.generating","output_index":0,"item_id":"ig_1"}`,
		`{"type":"response.image_generation_call.completed","output_index":0,"item_id":"ig_1"}`,
		`{"type":"response.completed","response":`+imageResponse(imageCallJSON("ig_1", "completed", `"ZmluYWwx"`, "")+","+imageCallJSON("ig_2", "completed", `"ZmluYWwy"`, ""))+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var previews []generation.ImagePreview
	var images []generation.Item
	for _, event := range events {
		switch value := event.(type) {
		case generation.ImagePreview:
			previews = append(previews, value)
		case generation.ItemStarted:
			if call := value.Item.(generation.OpenAIImageGenerationCall); !call.Result.IsZero() {
				t.Fatal("start exposed final image")
			}
		case generation.ItemEnded:
			images = append(images, value.Item)
		}
	}
	wantPreviews := []generation.ImagePreview{
		{ItemIndex: 1, Index: 0, Base64: "cHJldmlldzI=", Metadata: generation.ImageGenerationMetadata{OutputFormat: generation.Some("png")}},
		{ItemIndex: 0, Index: 0, Base64: "cHJldmlldzE=", Metadata: generation.ImageGenerationMetadata{OutputFormat: generation.Some("png")}},
	}
	wantImages := []generation.Item{
		generation.OpenAIImageGenerationCall{ID: "ig_1", Status: "completed", Result: generation.Some("ZmluYWwx")},
		generation.OpenAIImageGenerationCall{ID: "ig_2", Status: "completed", Result: generation.Some("ZmluYWwy")},
	}
	require.Equal(t, wantPreviews, previews)
	require.Equal(t, wantImages, images)
	require.Equal(t, 0, logs.Len())
}

func TestStreamImageRecoversEarlierResultAndLateMetadata(t *testing.T) {
	call := imageCallJSON("ig_1", "completed", `"aW1hZ2U="`, `,"output_format":"png","quality":"high"`)
	final := `{"id":"ig_1","type":"image_generation_call","status":"completed","quality":null,"revised_prompt":"Revised"}`
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.output_item.done","output_index":0,"item":`+call+`}`,
		`{"type":"response.completed","response":`+imageResponse(final)+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	end := eventAt[generation.ItemEnded](t, events, len(events)-2)
	want := generation.OpenAIImageGenerationCall{ID: "ig_1", Status: "completed", Result: generation.Some("aW1hZ2U="), Metadata: generation.ImageGenerationMetadata{OutputFormat: generation.Some("png"), Quality: generation.Null[string](), RevisedPrompt: generation.Some("Revised")}}
	require.Equal(t, want, end.Item)
}

func TestStreamImagePreviewDoesNotSatisfyCompletion(t *testing.T) {
	client := streamClient(t, io.Discard, created(), imageAdded(0, "ig_1"), imagePreviewJSON(0, "ig_1", 0, "cHJldmlldw=="),
		`{"type":"response.completed","response":`+imageResponse(imageCallJSON("ig_1", "completed", `null`, ""))+`}`,
	)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	last := eventAt[generation.UsageUpdated](t, events, len(events)-1)
	if last.Usage.Output != count(8) {
		t.Fatalf("last = %#v; want usage", events[len(events)-1])
	}
	assertNoResponseEnd(t, events)
}

func TestStreamImageConflictingFinalResult(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.output_item.done","output_index":0,"item":`+imageCallJSON("ig_1", "completed", `"b25l"`, "")+`}`,
		`{"type":"response.completed","response":`+imageResponse(imageCallJSON("ig_1", "completed", `"dHdv"`, ""))+`}`,
	)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamSkipsMalformedImagePreviews(t *testing.T) {
	var logs bytes.Buffer
	client := streamClient(t, &logs, created(), imageAdded(0, "ig_1"),
		imagePreviewJSON(0, "ig_1", -1, "eA=="), imagePreviewJSON(0, "other", 0, "eA=="), imagePreviewJSON(0, "ig_1", 0, "private!"),
		`{"type":"response.completed","response":`+imageResponse(imageCallJSON("ig_1", "completed", `"aW1hZ2U="`, ""))+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	for _, event := range events {
		if _, ok := event.(generation.ImagePreview); ok {
			t.Fatal("invalid preview emitted")
		}
	}
	if !strings.Contains(logs.String(), "invalid_image_preview") || strings.Contains(logs.String(), "private") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestStreamDoesNotAccumulateImagePreviews(t *testing.T) {
	large := strings.Repeat("eHh4", 150_000)
	client := streamClient(t, io.Discard, created(), imageAdded(0, "ig_1"),
		imagePreviewJSON(0, "ig_1", 0, large), imagePreviewJSON(0, "ig_1", 1, large), imagePreviewJSON(0, "ig_1", 2, large),
		`{"type":"response.completed","response":`+imageResponse(imageCallJSON("ig_1", "completed", `"ZmluYWw="`, ""))+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	previews := 0
	for _, event := range events {
		if _, ok := event.(generation.ImagePreview); ok {
			previews++
		}
	}
	if previews != 3 {
		t.Fatalf("previews = %d; want 3", previews)
	}
}

func TestStreamBoundsRetainedImagesAndMetadata(t *testing.T) {
	large := strings.Repeat("eHh4", 150_000)
	for _, tc := range []struct{ name, result, metadata string }{
		{name: "image", result: fmt.Sprintf("%q", large)},
		{name: "metadata", result: `null`, metadata: fmt.Sprintf(`,"revised_prompt":%q`, large)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(),
				`{"type":"response.output_item.added","output_index":0,"item":`+imageCallJSON("ig_1", "generating", tc.result, tc.metadata)+`}`,
				`{"type":"response.output_item.added","output_index":1,"item":`+imageCallJSON("ig_2", "generating", tc.result, tc.metadata)+`}`,
			)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func imageAdded(index int, id string) string {
	return fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":%s}`, index, imageCallJSON(id, "in_progress", `null`, ""))
}
func imagePreviewJSON(index int, id string, preview int, data string) string {
	return fmt.Sprintf(`{"type":"response.image_generation_call.partial_image","output_index":%d,"item_id":%q,"partial_image_index":%d,"partial_image_b64":%q,"output_format":"png"}`, index, id, preview, data)
}
func imageCallJSON(id, status, result, metadata string) string {
	return fmt.Sprintf(`{"id":%q,"type":"image_generation_call","status":%q,"result":%s%s}`, id, status, result, metadata)
}
func imageResponse(items string) string {
	return `{"id":"resp_1","model":"test-model","status":"completed","output":[` + items + `],"usage":{"output_tokens":8}}`
}
