package wire_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestEncodeFileSources(t *testing.T) {
	request := requestWith(message(
		generation.Text{Text: "Read the files"},
		generation.FileID{ID: "file_1"},
		generation.FileURL{URL: "https://files.example/report.pdf?token=keep", Options: generation.FileOptions{Detail: "high"}},
		generation.FileData{Data: "data:application/pdf;base64,YQ==", Options: generation.FileOptions{Filename: generation.Some("report.pdf"), OpenAI: generation.OpenAIFileOptions{PromptCacheBreakpoint: true}}},
	))

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"Read the files"},
		{"type":"input_file","file_id":"file_1"},
		{"type":"input_file","file_url":"https://files.example/report.pdf?token=keep","detail":"high"},
		{"type":"input_file","file_data":"data:application/pdf;base64,YQ==","filename":"report.pdf","prompt_cache_breakpoint":{"mode":"explicit"}}
	]}]}`)
}

func TestEncodeOutputTextMetadata(t *testing.T) {
	request := annotatedRequest()
	unchanged := annotatedRequest()

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"é",
		"annotations":[{"type":"url_citation","url":"https://example.com","title":"Source","start_index":0,"end_index":1},{"type":"file_path","file_id":"file_1","index":0}],
		"logprobs":[{"token":"é","bytes":[195,169],"logprob":-0.5,"top_logprobs":[{"token":"e","bytes":[101],"logprob":-2}]}]
	}]}]}`)
	if !reflect.DeepEqual(request, unchanged) {
		t.Fatal("encoding changed nested metadata")
	}
}

func TestEncodeRejectsInvalidContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		part generation.Part
	}{
		{name: "missing file ID", part: generation.FileID{}},
		{name: "local file URL", part: generation.FileURL{URL: "file:///private"}},
		{name: "URL credentials", part: generation.FileURL{URL: "https://secret@example.com/file"}},
		{name: "invalid base64", part: generation.FileData{Data: "not base64"}},
		{name: "empty decoded file", part: generation.FileData{Data: "\r\n"}},
		{name: "wrong data URL", part: generation.FileData{Data: "data:application/pdf,abc"}},
		{name: "invalid filename", part: generation.FileData{Data: "YQ==", Options: generation.FileOptions{Filename: generation.Some("\xff")}}},
		{name: "wrong detail", part: generation.FileID{ID: "file_1", Options: generation.FileOptions{Detail: "original"}}},
		{name: "output metadata in user input", part: generation.Text{Text: "text", OpenAI: generation.OpenAITextData{Annotations: []generation.Annotation{{Citation: generation.FilePath{FileID: "file_1"}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(message(tc.part))

			body, err := wire.EncodeRequest(request, false)

			assertInputError(t, body, err)
		})
	}
}

func TestEncodeRejectsInvalidOutputProbability(t *testing.T) {
	request := annotatedRequest()
	text := request.Input[0].(generation.Message).Parts[0].(generation.Text)
	text.OpenAI.Logprobs[0].Logprob = math.NaN()

	body, err := wire.EncodeRequest(request, false)

	assertInputError(t, body, err)
}

func TestEncodeEmptyLogprobCandidatesAsArray(t *testing.T) {
	text := generation.Text{
		Text: "x",
		OpenAI: generation.OpenAITextData{
			Logprobs: []generation.TokenLogprob{{
				TokenProbability: generation.TokenProbability{Token: "x", Bytes: []byte{120}, Logprob: 0},
			}},
		},
	}
	request := requestWith(generation.Message{
		ID:     "msg_1",
		Role:   generation.Assistant,
		Status: generation.ItemCompleted,
		Parts:  []generation.Part{text},
	})

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"x","annotations":[],"logprobs":[{"token":"x","bytes":[120],"logprob":0,"top_logprobs":[]}]}]}]}`)
}

func annotatedRequest() generation.Request {
	return requestWith(generation.Message{ID: "msg_1", Role: generation.Assistant, Status: generation.ItemCompleted, Parts: []generation.Part{generation.Text{
		Text: "é", OpenAI: generation.OpenAITextData{
			Annotations: []generation.Annotation{{Index: 1, Citation: generation.FilePath{FileID: "file_1", Index: 0}}, {Index: 0, Citation: generation.URLCitation{URL: "https://example.com", Title: "Source", Start: 0, End: 1}}},
			Logprobs:    []generation.TokenLogprob{{TokenProbability: generation.TokenProbability{Token: "é", Bytes: []byte{195, 169}, Logprob: -0.5}, Top: []generation.TokenProbability{{Token: "e", Bytes: []byte{101}, Logprob: -2}}}},
		},
	}}})
}
