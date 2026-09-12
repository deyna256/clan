package openai_test

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
)

func TestGeneratePreservesCitationsAndTokenBytes(t *testing.T) {
	client := testClient(t, staticJSON(metadataResponse()), io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	if err != nil {
		t.Fatal(err)
	}
	text := result.Response.Output[0].(generation.Message).Parts[0].(generation.Text)
	if !reflect.DeepEqual(text.OpenAI, expectedTextData()) {
		t.Fatalf("metadata = %#v; want %#v", text.OpenAI, expectedTextData())
	}
}

func TestGenerateSkipsMalformedOptionalTextData(t *testing.T) {
	var logs bytes.Buffer
	body := responseWithPart(`{"type":"output_text","text":"Usable","annotations":[{"type":"new_citation","payload":"secret"},{"type":"url_citation","start_index":-1},{"type":"file_path","file_id":"file_1","index":0}],"logprobs":[{"token":"private","bytes":[256],"logprob":-1}]}`)
	client := testClient(t, staticJSON(body), &logs)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	if err != nil {
		t.Fatal(err)
	}
	text := result.Response.Output[0].(generation.Message).Parts[0].(generation.Text)
	want := []generation.Annotation{{Index: 2, Citation: generation.FilePath{FileID: "file_1", Index: 0}}}
	if text.Text != "Usable" || !reflect.DeepEqual(text.OpenAI.Annotations, want) || len(text.OpenAI.Logprobs) != 0 {
		t.Fatalf("text = %#v", text)
	}
	if !strings.Contains(logs.String(), `"reason":"invalid_annotation"`) || !strings.Contains(logs.String(), `"reason":"invalid_logprobs"`) || strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "private") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestStreamMergesTextMetadataWithoutAliasing(t *testing.T) {
	client := streamClient(t, io.Discard, created(), messageAdded(),
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"é","logprobs":[{"token":"é","bytes":[195,169],"logprob":-0.5,"top_logprobs":[{"token":"e","bytes":[101],"logprob":-2}]}]}`,
		`{"type":"response.output_text.annotation.added","output_index":0,"content_index":0,"annotation_index":0,"annotation":{"type":"url_citation","url":"https://example.com","title":"Source","start_index":0,"end_index":1}}`,
		`{"type":"response.completed","response":`+metadataResponse()+`}`,
	)
	stream, err := client.GenerateStream(t.Context(), testAttempt(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var annotations, probabilities int
	var ended generation.Text

	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch e := event.(type) {
		case generation.AnnotationAdded:
			want := expectedTextData().Annotations
			if e.Address != (generation.PartAddress{Item: 0, Part: 0}) || e.Annotation.Index < 0 || e.Annotation.Index >= len(want) || e.Annotation != want[e.Annotation.Index] {
				t.Fatalf("annotation event = %#v", e)
			}
			annotations++
		case generation.LogprobsDelta:
			if e.Address != (generation.PartAddress{Item: 0, Part: 0}) || !reflect.DeepEqual(e.Tokens, expectedTextData().Logprobs) {
				t.Fatalf("probability event = %#v", e)
			}
			probabilities += len(e.Tokens)
			e.Tokens[0].Bytes[0] = 0
			e.Tokens[0].Top[0].Bytes[0] = 0
		case generation.PartEnded:
			text := e.Part.(generation.Text)
			if !reflect.DeepEqual(text.OpenAI, expectedTextData()) {
				t.Fatalf("part snapshot = %#v", text)
			}
			text.OpenAI.Annotations[0].Citation = generation.FilePath{FileID: "changed"}
			text.OpenAI.Logprobs[0].Bytes[0] = 0
		case generation.ItemEnded:
			ended = e.Item.(generation.Message).Parts[0].(generation.Text)
		}
	}

	if annotations != 4 || probabilities != 1 || !reflect.DeepEqual(ended.OpenAI, expectedTextData()) {
		t.Fatalf("annotations = %d; probabilities = %d; final = %#v", annotations, probabilities, ended.OpenAI)
	}
}

func TestStreamSkipsInvalidOptionalAnnotationEvents(t *testing.T) {
	for _, location := range []string{
		`"output_index":0,"content_index":0`,
		`"output_index":0,"content_index":0,"annotation_index":-1`,
		`"output_index":9,"content_index":0,"annotation_index":0`,
	} {
		t.Run(location, func(t *testing.T) {
			var logs bytes.Buffer
			client := streamClient(t, &logs, created(), messageAdded(),
				`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Usable"}`,
				`{"type":"response.output_text.annotation.added",`+location+`,"annotation":{"type":"file_path","file_id":"file_1","index":0}}`,
				`{"type":"response.completed","response":`+textResponse("Usable", "null")+`}`,
			)

			events, err := readStream(t, client)

			if !errors.Is(err, io.EOF) || !strings.Contains(logs.String(), `"reason":"invalid_annotation"`) {
				t.Fatalf("error = %v; logs = %s", err, logs.String())
			}
			for _, event := range events {
				if _, ok := event.(generation.AnnotationAdded); ok {
					t.Fatalf("invalid annotation emitted: %#v", event)
				}
			}
		})
	}
}

func responseWithPart(part string) string {
	return `{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[` + part + `]}]}`
}

func metadataResponse() string {
	return responseWithPart(`{"type":"output_text","text":"é","annotations":[
		{"type":"url_citation","url":"https://example.com","title":"Source","start_index":0,"end_index":1},
		{"type":"file_citation","file_id":"file_1","filename":"report.pdf","index":0},
		{"type":"container_file_citation","container_id":"container_1","file_id":"file_2","filename":"chart.png","start_index":0,"end_index":1},
		{"type":"file_path","file_id":"file_3","index":2}],
		"logprobs":[{"token":"é","bytes":[195,169],"logprob":-0.5,"top_logprobs":[{"token":"e","bytes":[101],"logprob":-2}]}]}`)
}

func expectedTextData() generation.OpenAITextData {
	return generation.OpenAITextData{
		Annotations: []generation.Annotation{
			{Index: 0, Citation: generation.URLCitation{URL: "https://example.com", Title: "Source", Start: 0, End: 1}},
			{Index: 1, Citation: generation.FileCitation{FileID: "file_1", Filename: "report.pdf", Index: 0}},
			{Index: 2, Citation: generation.ContainerFileCitation{ContainerID: "container_1", FileID: "file_2", Filename: "chart.png", Start: 0, End: 1}},
			{Index: 3, Citation: generation.FilePath{FileID: "file_3", Index: 2}},
		},
		Logprobs: []generation.TokenLogprob{{TokenProbability: generation.TokenProbability{Token: "é", Bytes: []byte{195, 169}, Logprob: -0.5}, Top: []generation.TokenProbability{{Token: "e", Bytes: []byte{101}, Logprob: -2}}}},
	}
}
