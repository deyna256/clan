package wire_test

import (
	"errors"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeInputCacheBreakpoints(t *testing.T) {
	request := requestWith(message(
		generation.Text{Text: "prefix", OpenAI: generation.OpenAITextData{PromptCacheBreakpoint: true}},
		generation.ImageURL{URL: "https://example.com/image.png", Detail: "original", OpenAI: generation.OpenAIImageOptions{PromptCacheBreakpoint: true}},
		generation.ImageFile{FileID: "file_image", OpenAI: generation.OpenAIImageOptions{PromptCacheBreakpoint: true}},
		generation.Text{Text: "uncached"},
	))
	body, err := wire.EncodeRequest(request, false)
	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"prefix","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"input_image","image_url":"https://example.com/image.png","detail":"original","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"input_image","file_id":"file_image","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"input_text","text":"uncached"}
	]}]}`)
}

func TestStoredMediaReplayPreservesPresence(t *testing.T) {
	raw := []byte(`{"type":"message","id":"msg_1","role":"user","status":"completed","content":[
		{"type":"input_image","detail":"original","file_id":null,"image_url":"https://example.com/image.png","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"input_image","detail":"low","file_id":"file_image","image_url":null},
		{"type":"computer_screenshot","detail":"auto","file_id":"file_screen","image_url":"https://example.com/screen.png"},
		{"type":"input_file","file_id":null,"file_url":"https://example.com/report.pdf","filename":"","detail":"high","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"input_file","file_id":"file_report"},
		{"type":"input_file","file_data":"data:application/pdf;base64,YQ==","filename":"report.pdf"}
	]}`)
	want := generation.Message{
		ID: "msg_1", Role: generation.User, Status: generation.ItemCompleted,
		Parts: []generation.Part{
			generation.OpenAIStoredImage{Kind: "input_image", Detail: "original", FileID: generation.Null[string](), ImageURL: generation.Some("https://example.com/image.png"), PromptCacheBreakpoint: true},
			generation.OpenAIStoredImage{Kind: "input_image", Detail: "low", FileID: generation.Some("file_image"), ImageURL: generation.Null[string]()},
			generation.OpenAIStoredImage{Kind: "computer_screenshot", Detail: "auto", FileID: generation.Some("file_screen"), ImageURL: generation.Some("https://example.com/screen.png")},
			generation.OpenAIStoredFile{FileID: generation.Null[string](), FileURL: generation.Some("https://example.com/report.pdf"), Filename: generation.Some(""), Detail: generation.Some("high"), PromptCacheBreakpoint: true},
			generation.OpenAIStoredFile{FileID: generation.Some("file_report")},
			generation.OpenAIStoredFile{FileData: generation.Some("data:application/pdf;base64,YQ=="), Filename: generation.Some("report.pdf")},
		},
	}

	item, err := wire.DecodeStoredItem(raw, nil)
	require.NoError(t, err)
	require.Equal(t, want, item)

	body, err := wire.EncodeRequest(requestWith(item), false)
	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"message","id":"msg_1","role":"user","status":"completed","content":[
		{"type":"input_image","detail":"original","file_id":null,"image_url":"https://example.com/image.png","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"input_image","detail":"low","file_id":"file_image","image_url":null},
		{"type":"input_image","detail":"auto","file_id":"file_screen","image_url":"https://example.com/screen.png"},
		{"type":"input_file","file_id":null,"file_url":"https://example.com/report.pdf","filename":"","detail":"high","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"input_file","file_id":"file_report"},
		{"type":"input_file","file_data":"data:application/pdf;base64,YQ==","filename":"report.pdf"}
	]}]}`)
}

func TestStoredMediaReplayRejectsUnusableSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		part generation.Part
	}{
		{name: "missing image", part: generation.OpenAIStoredImage{Kind: "input_image", Detail: "auto"}},
		{name: "null image references", part: generation.OpenAIStoredImage{Kind: "input_image", Detail: "auto", FileID: generation.Null[string](), ImageURL: generation.Null[string]()}},
		{name: "empty image ID", part: generation.OpenAIStoredImage{Kind: "input_image", Detail: "auto", FileID: generation.Some("")}},
		{name: "invalid image URL", part: generation.OpenAIStoredImage{Kind: "input_image", Detail: "auto", ImageURL: generation.Some("file:///private")}},
		{name: "image kind", part: generation.OpenAIStoredImage{Kind: "future", Detail: "auto", FileID: generation.Some("file_1")}},
		{name: "image detail", part: generation.OpenAIStoredImage{Kind: "input_image", Detail: "future", FileID: generation.Some("file_1")}},
		{name: "missing file", part: generation.OpenAIStoredFile{FileID: generation.Null[string]()}},
		{name: "file URL credentials", part: generation.OpenAIStoredFile{FileURL: generation.Some("https://private@example.com/file")}},
		{name: "file data", part: generation.OpenAIStoredFile{FileData: generation.Some("not base64")}},
		{name: "file null URL", part: generation.OpenAIStoredFile{FileID: generation.Some("file_1"), FileURL: generation.Null[string]()}},
		{name: "file null name", part: generation.OpenAIStoredFile{FileID: generation.Some("file_1"), Filename: generation.Null[string]()}},
		{name: "file invalid name", part: generation.OpenAIStoredFile{FileID: generation.Some("file_1"), Filename: generation.Some("\xff")}},
		{name: "file null detail", part: generation.OpenAIStoredFile{FileID: generation.Some("file_1"), Detail: generation.Null[string]()}},
		{name: "file invalid detail", part: generation.OpenAIStoredFile{FileID: generation.Some("file_1"), Detail: generation.Some("original")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := wire.EncodeRequest(requestWith(message(tc.part)), false)
			var input *wire.InputError
			if !errors.As(err, &input) || body != nil {
				t.Fatalf("encoding = %s, %v", body, err)
			}
		})
	}
}

func TestOutputPartsRejectInputCacheAndMedia(t *testing.T) {
	for _, part := range []generation.Part{
		generation.Text{Text: "output", OpenAI: generation.OpenAITextData{PromptCacheBreakpoint: true}},
		generation.OpenAIStoredImage{Kind: "input_image", Detail: "auto", FileID: generation.Some("file_1")},
		generation.OpenAIStoredFile{FileID: generation.Some("file_1")},
	} {
		request := requestWith(generation.Message{ID: "msg_1", Role: generation.Assistant, Status: generation.ItemCompleted, Parts: []generation.Part{part}})
		body, err := wire.EncodeRequest(request, false)
		if err == nil || body != nil {
			t.Fatalf("output encoding = %s, %v", body, err)
		}
	}
}
