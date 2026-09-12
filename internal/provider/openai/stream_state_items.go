package openai

import "github.com/deyna256/clan/internal/generation"

func validateStateItemCompletion(item generation.Item) error {
	if compaction, ok := item.(generation.OpenAICompaction); ok {
		if _, known := compaction.EncryptedContent.Value(); !known {
			return protocolError()
		}
	}
	return nil
}

func (s *streamState) mergeCompaction(index int, item *streamItem, next generation.OpenAICompaction) ([]generation.Event, error) {
	previous := item.item.(generation.OpenAICompaction)
	var err error
	next.EncryptedContent, err = mergeCallField(previous.EncryptedContent, next.EncryptedContent)
	if err != nil {
		return nil, err
	}
	next.CreatedBy, err = mergeCallField(previous.CreatedBy, next.CreatedBy)
	if err != nil {
		return nil, err
	}
	if err := s.grow(compactionBytes(next) - compactionBytes(previous)); err != nil {
		return nil, err
	}
	item.item = next
	if item.started {
		return nil, nil
	}
	item.started = true
	// Compaction has no payload delta events; publish its opaque content at ItemEnded.
	next.EncryptedContent = generation.Optional[string]{}
	return []generation.Event{generation.ItemStarted{Index: index, Item: next}}, nil
}

func compactionBytes(item generation.OpenAICompaction) int {
	return optionalStringBytes(item.ID, item.CreatedBy, item.EncryptedContent)
}
