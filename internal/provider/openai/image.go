package openai

import (
	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func validateImageCompletion(item generation.Item) error {
	if call, ok := item.(generation.OpenAIImageGenerationCall); ok && call.Status == "completed" {
		if result, known := call.Result.Value(); !known || result == "" {
			return protocolError()
		}
	}
	return nil
}

func (s *streamState) imagePreview(e responseEvent) ([]generation.Event, error) {
	if !validIndex(e.OutputIndex) || !validIndex(e.PartialImageIndex) || e.PartialImage == nil {
		s.diagnostics.report("invalid_image_preview")
		return nil, nil
	}
	item := s.items[*e.OutputIndex]
	if item == nil || e.ItemID != itemID(item.item) {
		s.diagnostics.report("invalid_image_preview")
		return nil, nil
	}
	if _, ok := item.item.(generation.OpenAIImageGenerationCall); !ok || !wire.ValidBase64Data(*e.PartialImage) {
		s.diagnostics.report("invalid_image_preview")
		return nil, nil
	}
	metadata := wire.DecodeImageMetadata([]byte(e.raw), s.diagnostics.report)
	if int64(len(*e.PartialImage)+imageMetadataBytes(metadata)) > s.limit-s.size {
		return nil, protocolError()
	}
	return []generation.Event{generation.ImagePreview{ItemIndex: *e.OutputIndex, Index: *e.PartialImageIndex, Base64: *e.PartialImage, Metadata: metadata}}, nil
}

func retainImageMetadata(previous, next generation.ImageGenerationMetadata) generation.ImageGenerationMetadata {
	for _, field := range []struct {
		prior generation.Optional[string]
		next  *generation.Optional[string]
	}{
		{previous.Action, &next.Action}, {previous.Background, &next.Background},
		{previous.OutputFormat, &next.OutputFormat}, {previous.Quality, &next.Quality},
		{previous.Size, &next.Size}, {previous.RevisedPrompt, &next.RevisedPrompt},
	} {
		if field.next.IsZero() {
			*field.next = field.prior
		}
	}
	return next
}

func imageMetadataBytes(metadata generation.ImageGenerationMetadata) int {
	size := 0
	for _, field := range []generation.Optional[string]{metadata.Action, metadata.Background, metadata.OutputFormat, metadata.Quality, metadata.Size, metadata.RevisedPrompt} {
		value, _ := field.Value()
		size += len(value)
	}
	return size
}
