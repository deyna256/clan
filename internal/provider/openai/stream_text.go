package openai

import (
	"cmp"
	"reflect"
	"slices"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func (s *streamState) annotation(e responseEvent) ([]generation.Event, error) {
	index, key, err := s.target(e)
	if err != nil || !validIndex(e.AnnotationIndex) {
		s.diagnostics.report("invalid_annotation")
		return nil, nil
	}
	if _, ok := s.items[index].item.(generation.Message); !ok {
		s.diagnostics.report("invalid_annotation")
		return nil, nil
	}
	annotation, err := wire.DecodeAnnotation(e.Annotation, *e.AnnotationIndex)
	if err != nil {
		s.diagnostics.report("invalid_annotation")
		return nil, nil
	}
	part := s.items[index].parts[key]
	var events []generation.Event
	if part == nil {
		events, err = s.mergePart(index, key, generation.Text{})
		if err != nil {
			return events, err
		}
		part = s.items[index].parts[key]
	}
	if _, ok := part.part.(generation.Text); !ok {
		s.diagnostics.report("invalid_annotation")
		return events, nil
	}
	more, err := s.mergeAnnotation(part, annotation)
	return append(events, more...), err
}

func (s *streamState) mergeTextMetadata(part *streamPart, data generation.OpenAITextData) ([]generation.Event, error) {
	var events []generation.Event
	for _, annotation := range data.Annotations {
		more, err := s.mergeAnnotation(part, annotation)
		events = append(events, more...)
		if err != nil {
			return events, err
		}
	}
	previous := part.part.(generation.Text).OpenAI.Logprobs
	shared := min(len(previous), len(data.Logprobs))
	if shared > 0 && !reflect.DeepEqual(previous[:shared], data.Logprobs[:shared]) {
		s.diagnostics.warn("logprobs_snapshot_conflict", "response", "kept_emitted_metadata")
		return events, nil
	}
	if len(data.Logprobs) <= len(previous) {
		return events, nil
	}
	more, err := s.appendLogprobs(part, data.Logprobs[len(previous):])
	return append(events, more...), err
}

func (s *streamState) mergeAnnotation(part *streamPart, annotation generation.Annotation) ([]generation.Event, error) {
	text := part.part.(generation.Text)
	index, found := slices.BinarySearchFunc(text.OpenAI.Annotations, annotation.Index, func(a generation.Annotation, index int) int { return cmp.Compare(a.Index, index) })
	if found {
		if text.OpenAI.Annotations[index].Citation != annotation.Citation {
			s.diagnostics.warn("annotation_snapshot_conflict", "response", "kept_emitted_metadata")
		}
		return nil, nil
	}
	if err := s.grow(annotationBytes(annotation)); err != nil {
		return nil, err
	}
	text.OpenAI.Annotations = slices.Insert(text.OpenAI.Annotations, index, annotation)
	part.part = text
	return []generation.Event{generation.AnnotationAdded{Address: part.address, Annotation: annotation}}, nil
}

func (s *streamState) appendLogprobs(part *streamPart, probabilities []generation.TokenLogprob) ([]generation.Event, error) {
	if len(probabilities) == 0 {
		return nil, nil
	}
	for _, token := range probabilities {
		if err := s.grow(64 + len(token.Token) + len(token.Bytes)); err != nil {
			return nil, err
		}
		for _, top := range token.Top {
			if err := s.grow(64 + len(top.Token) + len(top.Bytes)); err != nil {
				return nil, err
			}
		}
	}
	text := part.part.(generation.Text)
	text.OpenAI.Logprobs = append(text.OpenAI.Logprobs, probabilities...)
	part.part = text
	return []generation.Event{generation.LogprobsDelta{Address: part.address, Tokens: copyLogprobs(probabilities)}}, nil
}

func annotationBytes(annotation generation.Annotation) int {
	switch c := annotation.Citation.(type) {
	case generation.URLCitation:
		return 64 + len(c.URL) + len(c.Title)
	case generation.FileCitation:
		return 64 + len(c.FileID) + len(c.Filename)
	case generation.ContainerFileCitation:
		return 64 + len(c.ContainerID) + len(c.FileID) + len(c.Filename)
	case generation.FilePath:
		return 64 + len(c.FileID)
	default:
		return 64
	}
}

func emptyPart(part generation.Part) generation.Part {
	if _, ok := part.(generation.Text); ok {
		return generation.Text{}
	}
	return withText(part, "")
}

func copyPart(part generation.Part) generation.Part {
	if text, ok := part.(generation.Text); ok {
		text.OpenAI.Annotations = slices.Clone(text.OpenAI.Annotations)
		text.OpenAI.Logprobs = copyLogprobs(text.OpenAI.Logprobs)
		return text
	}
	return part
}

func copyLogprobs(probabilities []generation.TokenLogprob) []generation.TokenLogprob {
	cloned := slices.Clone(probabilities)
	for i := range cloned {
		cloned[i].Bytes = slices.Clone(cloned[i].Bytes)
		cloned[i].Top = slices.Clone(cloned[i].Top)
		for j := range cloned[i].Top {
			cloned[i].Top[j].Bytes = slices.Clone(cloned[i].Top[j].Bytes)
		}
	}
	return cloned
}
