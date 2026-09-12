package openai

import (
	"strings"

	"github.com/deyna256/clan/internal/generation"
)

func (s *streamState) mergePatchCall(index int, item *streamItem, next generation.OpenAIApplyPatchCall) ([]generation.Event, error) {
	previous := item.item.(generation.OpenAIApplyPatchCall)
	if previous.Status == "completed" && next.Status != "completed" {
		return nil, protocolError()
	}
	beforeKind, beforePath, beforeDiff := patchOperationData(previous.Operation)
	kind, path, diff := patchOperationData(next.Operation)
	if previous.CallID != next.CallID || beforeKind != kind || beforePath != path || !strings.HasPrefix(diff, beforeDiff) {
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
	var events []generation.Event
	suffix := diff[len(beforeDiff):]
	if !item.started {
		item.started = true
		start := next
		start.Operation = emptyPatchOperation(next.Operation)
		events = append(events, generation.ItemStarted{Index: index, Item: start})
		suffix = diff
	}
	if suffix != "" {
		events = append(events, generation.PatchDiffDelta{ItemIndex: index, Fragment: suffix})
	}
	return events, nil
}

func (s *streamState) mergePatchResult(index int, item *streamItem, next generation.OpenAIApplyPatchResult) ([]generation.Event, error) {
	previous := item.item.(generation.OpenAIApplyPatchResult)
	if previous.CallID != next.CallID || previous.Status != next.Status {
		return nil, protocolError()
	}
	var err error
	if next.Caller, err = mergeCallField(previous.Caller, next.Caller); err != nil {
		return nil, err
	}
	if next.CreatedBy, err = mergeCallField(previous.CreatedBy, next.CreatedBy); err != nil {
		return nil, err
	}
	if next.Output.IsZero() {
		next.Output = previous.Output
	}
	if before, known := previous.Output.Value(); known {
		after, present := next.Output.Value()
		if !present || !strings.HasPrefix(after, before) {
			return nil, protocolError()
		}
	}
	if err := s.grow(metadataBytes(next) - metadataBytes(previous)); err != nil {
		return nil, err
	}
	item.item = next
	if item.started {
		return nil, nil
	}
	item.started = true
	next.Output = generation.Optional[string]{}
	return []generation.Event{generation.ItemStarted{Index: index, Item: next}}, nil
}

func patchOperationData(operation generation.PatchOperation) (kind, path, diff string) {
	switch value := operation.(type) {
	case generation.PatchCreateFile:
		return "create_file", value.Path, value.Diff
	case generation.PatchUpdateFile:
		return "update_file", value.Path, value.Diff
	case generation.PatchDeleteFile:
		return "delete_file", value.Path, ""
	default:
		return "", "", ""
	}
}

func emptyPatchOperation(operation generation.PatchOperation) generation.PatchOperation {
	switch value := operation.(type) {
	case generation.PatchCreateFile:
		value.Diff = ""
		return value
	case generation.PatchUpdateFile:
		value.Diff = ""
		return value
	default:
		return operation
	}
}
