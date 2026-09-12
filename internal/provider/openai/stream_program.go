package openai

import (
	"strings"

	"github.com/deyna256/clan/internal/generation"
)

func (s *streamState) mergeProgram(index int, item *streamItem, next generation.Item) ([]generation.Event, error) {
	switch value := next.(type) {
	case generation.OpenAIProgram:
		previous := item.item.(generation.OpenAIProgram)
		if value.CallID == "" {
			value.CallID = previous.CallID
		}
		var err error
		value.Fingerprint, err = mergeCallField(previous.Fingerprint, value.Fingerprint)
		if err != nil || (previous.CallID != "" && value.CallID != previous.CallID) || !strings.HasPrefix(value.Code, previous.Code) {
			return nil, protocolError()
		}
		next = value
	case generation.OpenAIProgramOutput:
		previous := item.item.(generation.OpenAIProgramOutput)
		if value.CallID == "" {
			value.CallID = previous.CallID
		}
		if value.Status == "" {
			value.Status = previous.Status
		}
		if (previous.CallID != "" && value.CallID != previous.CallID) || (previous.Status != "" && value.Status != previous.Status) || !strings.HasPrefix(value.Result, previous.Result) {
			return nil, protocolError()
		}
		next = value
	}
	if err := s.grow(programBytes(next) - programBytes(item.item)); err != nil {
		return nil, err
	}
	item.item = next
	if item.started {
		return nil, nil
	}
	item.started = true
	// Programs have no dedicated delta events. Publish their payload at ItemEnded.
	switch value := next.(type) {
	case generation.OpenAIProgram:
		value.Code = ""
		next = value
	case generation.OpenAIProgramOutput:
		value.Result = ""
		next = value
	}
	return []generation.Event{generation.ItemStarted{Index: index, Item: next}}, nil
}

func programBytes(item generation.Item) int {
	switch value := item.(type) {
	case generation.OpenAIProgram:
		return len(value.ID) + len(value.CallID) + len(value.Code) + optionalStringBytes(value.Fingerprint)
	case generation.OpenAIProgramOutput:
		return len(value.ID) + len(value.CallID) + len(value.Result)
	default:
		return 0
	}
}
