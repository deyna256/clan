package openai

import (
	"strings"

	"github.com/deyna256/clan/internal/generation"
)

func (s *streamState) mergeFunctionCall(index int, item *streamItem, next generation.ToolCall) ([]generation.Event, error) {
	previous := item.item.(generation.ToolCall)
	if next.CallID == "" {
		next.CallID = previous.CallID
	}
	if next.Status == "" {
		next.Status = previous.Status
	}
	if next.Name == "" {
		next.Name = previous.Name
	}
	if (previous.CallID != "" && previous.CallID != next.CallID) || (previous.Name != "" && previous.Name != next.Name) || !strings.HasPrefix(next.Arguments, item.arguments.String()) {
		return nil, protocolError()
	}
	var err error
	if next.OpenAI, err = mergeToolCallData(previous.OpenAI, next.OpenAI); err != nil {
		return nil, err
	}
	suffix := next.Arguments[item.arguments.Len():]
	if err := s.grow(len(suffix) + metadataBytes(next) - metadataBytes(previous)); err != nil {
		return nil, err
	}
	item.arguments.WriteString(suffix)
	next.Arguments = ""
	item.item = next
	var events []generation.Event
	if !item.started && next.CallID != "" && strings.TrimSpace(next.Name) != "" {
		item.started = true
		events = append(events, generation.ItemStarted{Index: index, Item: next})
		suffix = item.arguments.String()
	}
	if item.started && suffix != "" {
		events = append(events, generation.ArgumentsDelta{ItemIndex: index, Fragment: suffix})
	}
	return events, nil
}

func (s *streamState) mergeFunctionResult(index int, item *streamItem, next generation.ToolResult) ([]generation.Event, error) {
	previous := item.item.(generation.ToolResult)
	if !extendsToolOutput(previous.Output, next.Output) {
		return nil, protocolError()
	}
	var err error
	if next.CallID, err = mergeCallField(previous.CallID, next.CallID); err != nil {
		return nil, err
	}
	if next.OpenAI.Name, err = mergeCallField(previous.OpenAI.Name, next.OpenAI.Name); err != nil {
		return nil, err
	}
	if next.OpenAI.Namespace, err = mergeCallField(previous.OpenAI.Namespace, next.OpenAI.Namespace); err != nil {
		return nil, err
	}
	if next.OpenAI.CreatedBy, err = mergeCallField(previous.OpenAI.CreatedBy, next.OpenAI.CreatedBy); err != nil {
		return nil, err
	}
	if next.Caller, err = mergeCallField(previous.Caller, next.Caller); err != nil {
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
