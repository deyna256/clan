package openai

import (
	"strings"

	"github.com/deyna256/clan/internal/generation"
)

func (s *streamState) mergeInterpreter(index int, item *streamItem, next generation.OpenAICodeInterpreterCall) ([]generation.Event, error) {
	previous := item.item.(generation.OpenAICodeInterpreterCall)
	if next.ContainerID == "" {
		next.ContainerID = previous.ContainerID
	}
	if previous.ContainerID != "" && previous.ContainerID != next.ContainerID {
		return nil, protocolError()
	}
	var suffix string
	if code, known := next.Code.Value(); known {
		if !strings.HasPrefix(code, item.code.String()) {
			return nil, protocolError()
		}
		suffix = code[item.code.Len():]
		next.Code = generation.Some("")
	} else if !previous.Code.IsZero() {
		next.Code = previous.Code
	}
	if next.Outputs.IsZero() {
		next.Outputs = previous.Outputs
	}
	if err := s.grow(len(suffix) + interpreterBytes(next) - interpreterBytes(previous)); err != nil {
		return nil, err
	}
	item.code.WriteString(suffix)
	item.item = next
	var events []generation.Event
	if !item.started {
		item.started = true
		events = append(events, generation.ItemStarted{Index: index, Item: generation.OpenAICodeInterpreterCall{ID: next.ID, ContainerID: next.ContainerID, Status: next.Status}})
	}
	if suffix != "" {
		events = append(events, generation.CodeDelta{ItemIndex: index, Fragment: suffix})
	}
	return events, nil
}

func (s *streamState) interpreterCode(e responseEvent) ([]generation.Event, error) {
	if !validIndex(e.OutputIndex) {
		return nil, protocolError()
	}
	item := s.items[*e.OutputIndex]
	if item == nil || (e.ItemID != "" && e.ItemID != itemID(item.item)) {
		return nil, protocolError()
	}
	call, ok := item.item.(generation.OpenAICodeInterpreterCall)
	if !ok {
		return nil, protocolError()
	}
	if e.Type == "response.code_interpreter_call_code.done" {
		if e.Code == nil {
			return nil, protocolError()
		}
		call.Code = generation.Some(*e.Code)
		return s.mergeInterpreter(*e.OutputIndex, item, call)
	}
	if e.Delta == nil {
		return nil, protocolError()
	}
	if err := s.grow(len(*e.Delta)); err != nil {
		return nil, err
	}
	item.code.WriteString(*e.Delta)
	call.Code = generation.Some("")
	item.item = call
	if *e.Delta == "" {
		return nil, nil
	}
	return []generation.Event{generation.CodeDelta{ItemIndex: *e.OutputIndex, Fragment: *e.Delta}}, nil
}

func interpreterBytes(call generation.OpenAICodeInterpreterCall) int {
	size := len(call.ID) + len(call.ContainerID)
	outputs, _ := call.Outputs.Value()
	for _, output := range outputs {
		size += 32
		switch value := output.(type) {
		case generation.InterpreterLogs:
			size += len(value.Logs)
		case generation.InterpreterImage:
			size += len(value.URL)
		}
	}
	return size
}
