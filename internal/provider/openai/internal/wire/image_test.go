package wire_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeImageTool(t *testing.T) {
	request := imageToolRequest()
	unchanged := imageToolRequest()

	body, err := wire.EncodeRequest(request, true)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":true,"tools":[{"type":"image_generation","model":"image-alias","action":"edit","background":"transparent","output_format":"webp","quality":"max","size":"1536x864","input_fidelity":"high","moderation":"low","output_compression":0,"partial_images":0,"input_image_mask":{"file_id":"file_mask"}}],"tool_choice":{"type":"image_generation"}}`)
	require.Equal(t, unchanged, request)
}

func TestEncodeImageMaskSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		mask generation.Optional[generation.ImageGenerationMask]
		want string
	}{
		{name: "omitted"},
		{
			name: "URL",
			mask: generation.Some(generation.ImageGenerationMask{ImageURL: generation.Some("https://example.com/mask.png")}),
			want: `,"input_image_mask":{"image_url":"https://example.com/mask.png"}`,
		},
		{
			name: "data URL",
			mask: generation.Some(generation.ImageGenerationMask{ImageURL: generation.Some("data:image/png;base64,bWFzaw==")}),
			want: `,"input_image_mask":{"image_url":"data:image/png;base64,bWFzaw=="}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{generation.OpenAIImageGenerationTool{Mask: tc.mask}}

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tools":[{"type":"image_generation"`+tc.want+`}]}`)
		})
	}
}

func TestRejectInvalidImageTools(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool generation.OpenAIImageGenerationTool
	}{
		{name: "quality", tool: generation.OpenAIImageGenerationTool{Quality: "unknown"}},
		{name: "action", tool: generation.OpenAIImageGenerationTool{Action: "unknown"}},
		{name: "size", tool: generation.OpenAIImageGenerationTool{Size: "0x1024"}},
		{name: "compression", tool: generation.OpenAIImageGenerationTool{OutputCompression: generation.Some(int64(101))}},
		{name: "negative partials", tool: generation.OpenAIImageGenerationTool{PartialImages: generation.Some(int64(-1))}},
		{name: "too many partials", tool: generation.OpenAIImageGenerationTool{PartialImages: generation.Some(int64(4))}},
		{name: "transparent JPEG", tool: generation.OpenAIImageGenerationTool{Background: "transparent", OutputFormat: "jpeg"}},
		{name: "null mask", tool: generation.OpenAIImageGenerationTool{Mask: generation.Null[generation.ImageGenerationMask]()}},
		{name: "null mask file", tool: generation.OpenAIImageGenerationTool{Mask: generation.Some(generation.ImageGenerationMask{FileID: generation.Null[string]()})}},
		{name: "mask base64", tool: generation.OpenAIImageGenerationTool{Mask: generation.Some(generation.ImageGenerationMask{ImageURL: generation.Some("data:image/png;base64,private!")})}},
		{name: "mask local file", tool: generation.OpenAIImageGenerationTool{Mask: generation.Some(generation.ImageGenerationMask{ImageURL: generation.Some("file:///private")})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{tc.tool}

			body, err := wire.EncodeRequest(request, false)

			var inputError *wire.InputError
			if body != nil || !errors.As(err, &inputError) {
				t.Fatalf("EncodeRequest = %s, %v", body, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked image contents")
			}
		})
	}
}

func TestEncodeImageHistory(t *testing.T) {
	request := requestWith(
		generation.OpenAIImageGenerationCall{
			ID:     "ig_1",
			Status: "completed",
			Result: generation.Some("aW1hZ2U="),
			Metadata: generation.ImageGenerationMetadata{
				Action:        generation.Some("edit"),
				Background:    generation.Null[string](),
				OutputFormat:  generation.Some("png"),
				Quality:       generation.Some("xhigh"),
				Size:          generation.Some("1536x864"),
				RevisedPrompt: generation.Some("Revised"),
			},
		},
		generation.OpenAIImageGenerationCall{ID: "ig_2", Status: "failed", Result: generation.Null[string]()},
	)

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","input":[{"type":"image_generation_call","id":"ig_1","status":"completed","result":"aW1hZ2U=","action":"edit","background":null,"output_format":"png","quality":"xhigh","size":"1536x864","revised_prompt":"Revised"},{"type":"image_generation_call","id":"ig_2","status":"failed","result":null}],"stream":false}`)
}

func TestRejectInvalidImageHistory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result generation.Optional[string]
	}{
		{name: "missing", result: generation.Optional[string]{}},
		{name: "null", result: generation.Null[string]()},
		{name: "empty", result: generation.Some("")},
		{name: "only line breaks", result: generation.Some("\r\n")},
		{name: "invalid base64", result: generation.Some("private!")},
		{name: "concatenated padded data", result: generation.Some("eA==eA==")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(generation.OpenAIImageGenerationCall{ID: "ig_1", Status: "completed", Result: tc.result})

			body, err := wire.EncodeRequest(request, false)

			var inputError *wire.InputError
			if body != nil || !errors.As(err, &inputError) {
				t.Fatalf("EncodeRequest = %s, %v", body, err)
			}
		})
	}
}

func imageToolRequest() generation.Request {
	request := requestWith()
	request.Tools = []generation.Tool{generation.OpenAIImageGenerationTool{
		Model:             generation.Some("image-alias"),
		Action:            "edit",
		Background:        "transparent",
		OutputFormat:      "webp",
		Quality:           "max",
		Size:              "1536x864",
		InputFidelity:     generation.Some("high"),
		Moderation:        "low",
		OutputCompression: generation.Some(int64(0)),
		PartialImages:     generation.Some(int64(0)),
		Mask:              generation.Some(generation.ImageGenerationMask{FileID: generation.Some("file_mask")}),
	}}
	request.ToolChoice = generation.OpenAIImageGenerationChoice{}
	return request
}
