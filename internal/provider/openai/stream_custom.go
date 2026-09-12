package openai

import (
	"reflect"
	"strings"

	"github.com/deyna256/clan/internal/generation"
)

func (s *streamState) mergeCustomCall(index int, item *streamItem, next generation.CustomToolCall) ([]generation.Event, error) {
	previous := item.item.(generation.CustomToolCall)
	if next.CallID == "" {
		next.CallID = previous.CallID
	}
	if next.Status == "" {
		next.Status = previous.Status
	}
	if next.Name == "" {
		next.Name = previous.Name
	}
	if (previous.CallID != "" && previous.CallID != next.CallID) || (previous.Name != "" && previous.Name != next.Name) || !strings.HasPrefix(next.Input, item.arguments.String()) {
		return nil, protocolError()
	}
	var err error
	if next.OpenAI, err = mergeToolCallData(previous.OpenAI, next.OpenAI); err != nil {
		return nil, err
	}
	suffix := next.Input[item.arguments.Len():]
	if err := s.grow(len(suffix) + metadataBytes(next) - metadataBytes(previous)); err != nil {
		return nil, err
	}
	item.arguments.WriteString(suffix)
	next.Input = ""
	item.item = next
	var events []generation.Event
	if !item.started && strings.TrimSpace(next.CallID) != "" && strings.TrimSpace(next.Name) != "" {
		item.started = true
		events = append(events, generation.ItemStarted{Index: index, Item: next})
		suffix = item.arguments.String()
	}
	if item.started && suffix != "" {
		events = append(events, generation.ToolInputDelta{ItemIndex: index, Fragment: suffix})
	}
	return events, nil
}

func mergeToolCallData(previous, next generation.OpenAIToolCallData) (generation.OpenAIToolCallData, error) {
	var err error
	if next.Async, err = mergeCallField(previous.Async, next.Async); err != nil {
		return next, err
	}
	if next.Namespace, err = mergeCallField(previous.Namespace, next.Namespace); err != nil {
		return next, err
	}
	next.Caller, err = mergeCallField(previous.Caller, next.Caller)
	return next, err
}

func mergeCallField[T comparable](previous, next generation.Optional[T]) (generation.Optional[T], error) {
	if next.IsZero() {
		return previous, nil
	}
	if _, known := previous.Value(); known && previous != next {
		return next, protocolError()
	}
	return next, nil
}

func (s *streamState) customInput(e responseEvent) ([]generation.Event, error) {
	if !validIndex(e.OutputIndex) {
		return nil, protocolError()
	}
	item := s.items[*e.OutputIndex]
	if item == nil || e.ItemID == "" || e.ItemID != itemID(item.item) {
		return nil, protocolError()
	}
	call, ok := item.item.(generation.CustomToolCall)
	if !ok {
		return nil, protocolError()
	}
	if e.Type == "response.custom_tool_call_input.done" {
		if e.Input == nil {
			return nil, protocolError()
		}
		call.Input = *e.Input
		return s.mergeCustomCall(*e.OutputIndex, item, call)
	}
	if e.Delta == nil {
		return nil, protocolError()
	}
	if err := s.grow(len(*e.Delta)); err != nil {
		return nil, err
	}
	item.arguments.WriteString(*e.Delta)
	if !item.started || *e.Delta == "" {
		return nil, nil
	}
	return []generation.Event{generation.ToolInputDelta{ItemIndex: *e.OutputIndex, Fragment: *e.Delta}}, nil
}

func (s *streamState) mergeCustomResult(index int, item *streamItem, next generation.CustomToolResult) ([]generation.Event, error) {
	previous := item.item.(generation.CustomToolResult)
	if previous.CallID != next.CallID || !extendsToolOutput(previous.Output, next.Output) {
		return nil, protocolError()
	}
	var err error
	if next.Caller, err = mergeCallField(previous.Caller, next.Caller); err != nil {
		return nil, err
	}
	if next.CreatedBy, err = mergeCallField(previous.CreatedBy, next.CreatedBy); err != nil {
		return nil, err
	}
	if err := s.grow(metadataBytes(next) - metadataBytes(previous)); err != nil {
		return nil, err
	}
	item.item = next
	if item.started {
		return nil, nil
	}
	item.started = true
	next.Output = nil
	return []generation.Event{generation.ItemStarted{Index: index, Item: next}}, nil
}

func extendsToolOutput(previous, next generation.ToolOutput) bool {
	switch value := next.(type) {
	case generation.ToolTextOutput:
		prior, ok := previous.(generation.ToolTextOutput)
		return ok && strings.HasPrefix(string(value), string(prior))
	case generation.ToolPartsOutput:
		prior, ok := previous.(generation.ToolPartsOutput)
		return ok && len(value) >= len(prior) && reflect.DeepEqual(prior, value[:len(prior)])
	default:
		return false
	}
}

func callerBytes(caller generation.Optional[generation.OpenAIToolCaller]) int {
	value, _ := caller.Value()
	return len(value.Type) + len(value.CallerID)
}

func toolOutputBytes(output generation.ToolOutput) int {
	if text, ok := output.(generation.ToolTextOutput); ok {
		return len(text)
	}
	parts, _ := output.(generation.ToolPartsOutput)
	size := 0
	for _, part := range parts {
		size += 64
		var options generation.FileOptions
		switch value := part.(type) {
		case generation.OpenAIFunctionText:
			size += len(value.Text)
		case generation.OpenAIFunctionImage:
			size += optionalStringBytes(value.Detail, value.FileID, value.ImageURL)
		case generation.OpenAIFunctionFile:
			size += optionalStringBytes(value.Detail, value.FileID, value.FileData, value.FileURL, value.Filename)
		case generation.Text:
			size += len(value.Text)
		case generation.ImageURL:
			size += len(value.URL) + len(value.Detail)
		case generation.ImageFile:
			size += len(value.FileID) + len(value.Detail)
		case generation.FileID:
			size += len(value.ID)
			options = value.Options
		case generation.FileURL:
			size += len(value.URL)
			options = value.Options
		case generation.FileData:
			size += len(value.Data)
			options = value.Options
		}
		filename, _ := options.Filename.Value()
		size += len(filename) + len(options.Detail)
	}
	return size
}
