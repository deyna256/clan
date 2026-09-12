package openai

import (
	"reflect"
	"slices"

	"github.com/deyna256/clan/internal/generation"
)

func (s *streamState) mergeComputer(index int, item *streamItem, next generation.Item) ([]generation.Event, error) {
	switch value := next.(type) {
	case generation.OpenAIComputerCall:
		previous := item.item.(generation.OpenAIComputerCall)
		if previous.CallID != value.CallID || !itemStatusAdvances(generation.Some(previous.Status), generation.Some(value.Status)) {
			return nil, protocolError()
		}
		var err error
		if value.Action, err = mergeComputerAction(previous.Action, value.Action); err != nil {
			return nil, err
		}
		if value.Actions == nil {
			value.Actions = previous.Actions
		}
		if len(value.Actions) < len(previous.Actions) {
			return nil, protocolError()
		}
		value.Actions = slices.Clone(value.Actions)
		for index, action := range previous.Actions {
			if value.Actions[index], err = mergeComputerAction(action, value.Actions[index]); err != nil {
				return nil, err
			}
		}
		if err := mergeComputerChecks(previous.PendingSafetyChecks, value.PendingSafetyChecks); err != nil {
			return nil, err
		}
		next = value
	case generation.OpenAIComputerResult:
		previous := item.item.(generation.OpenAIComputerResult)
		var err error
		value, err = mergeComputerResult(previous, value)
		if err != nil {
			return nil, err
		}
		next = value
	}
	if err := s.grow(computerBytes(next) - computerBytes(item.item)); err != nil {
		return nil, err
	}
	item.item = next
	if item.started {
		return nil, nil
	}
	item.started = true
	return []generation.Event{generation.ItemStarted{Index: index, Item: copyComputerItem(next)}}, nil
}

func mergeComputerAction(previous, next generation.ComputerAction) (generation.ComputerAction, error) {
	if previous == nil {
		return next, nil
	}
	if next == nil {
		return previous, nil
	}
	if reflect.TypeOf(previous) != reflect.TypeOf(next) {
		return nil, protocolError()
	}
	var err error
	switch value := next.(type) {
	case generation.ComputerClick:
		before := previous.(generation.ComputerClick)
		value.Keys, err = mergeComputerKeys(before.Keys, value.Keys)
		before.Keys = value.Keys
		previous, next = before, value
	case generation.ComputerDoubleClick:
		before := previous.(generation.ComputerDoubleClick)
		value.Keys, err = mergeComputerKeys(before.Keys, value.Keys)
		before.Keys = value.Keys
		previous, next = before, value
	case generation.ComputerDrag:
		before := previous.(generation.ComputerDrag)
		value.Keys, err = mergeComputerKeys(before.Keys, value.Keys)
		before.Keys = value.Keys
		previous, next = before, value
	case generation.ComputerMove:
		before := previous.(generation.ComputerMove)
		value.Keys, err = mergeComputerKeys(before.Keys, value.Keys)
		before.Keys = value.Keys
		previous, next = before, value
	case generation.ComputerScroll:
		before := previous.(generation.ComputerScroll)
		value.Keys, err = mergeComputerKeys(before.Keys, value.Keys)
		before.Keys = value.Keys
		previous, next = before, value
	}
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(previous, next) {
		return nil, protocolError()
	}
	return next, nil
}

func mergeComputerKeys(previous, next generation.Optional[[]string]) (generation.Optional[[]string], error) {
	if next.IsZero() {
		return previous, nil
	}
	if _, known := previous.Value(); known && !reflect.DeepEqual(previous, next) {
		return next, protocolError()
	}
	return next, nil
}

func mergeComputerResult(previous, next generation.OpenAIComputerResult) (generation.OpenAIComputerResult, error) {
	if next.Status.IsZero() {
		next.Status = previous.Status
	}
	if previous.CallID != next.CallID || !itemStatusAdvances(previous.Status, next.Status) {
		return next, protocolError()
	}
	var err error
	if next.Output.FileID, err = mergeCallField(previous.Output.FileID, next.Output.FileID); err != nil {
		return next, err
	}
	if next.Output.ImageURL, err = mergeCallField(previous.Output.ImageURL, next.Output.ImageURL); err != nil {
		return next, err
	}
	if next.Output.Detail, err = mergeCallField(previous.Output.Detail, next.Output.Detail); err != nil {
		return next, err
	}
	if next.CreatedBy, err = mergeCallField(previous.CreatedBy, next.CreatedBy); err != nil {
		return next, err
	}
	if next.AcknowledgedSafetyChecks.IsZero() {
		next.AcknowledgedSafetyChecks = previous.AcknowledgedSafetyChecks
	}
	before, _ := previous.AcknowledgedSafetyChecks.Value()
	after, _ := next.AcknowledgedSafetyChecks.Value()
	return next, mergeComputerChecks(before, after)
}

func mergeComputerChecks(previous, next []generation.ComputerSafetyCheck) error {
	if len(previous) == 0 {
		return nil
	}
	indices := make(map[string]int, len(next))
	for index, check := range next {
		indices[check.ID] = index
	}
	for _, before := range previous {
		index, present := indices[before.ID]
		if !present {
			return protocolError()
		}
		after := &next[index]
		var err error
		if after.Code, err = mergeCallField(before.Code, after.Code); err != nil {
			return err
		}
		if after.Message, err = mergeCallField(before.Message, after.Message); err != nil {
			return err
		}
	}
	return nil
}

func copyComputerItem(item generation.Item) generation.Item {
	switch value := item.(type) {
	case generation.OpenAIComputerCall:
		value.Action = copyComputerAction(value.Action)
		value.Actions = slices.Clone(value.Actions)
		for index, action := range value.Actions {
			value.Actions[index] = copyComputerAction(action)
		}
		value.PendingSafetyChecks = slices.Clone(value.PendingSafetyChecks)
		return value
	case generation.OpenAIComputerResult:
		value.AcknowledgedSafetyChecks = copyOptionalSlice(value.AcknowledgedSafetyChecks)
		return value
	default:
		return item
	}
}

func copyOptionalSlice[T any](value generation.Optional[[]T]) generation.Optional[[]T] {
	if values, ok := value.Value(); ok {
		return generation.Some(slices.Clone(values))
	}
	return value
}

func copyComputerAction(action generation.ComputerAction) generation.ComputerAction {
	switch value := action.(type) {
	case generation.ComputerClick:
		value.Keys = copyOptionalSlice(value.Keys)
		return value
	case generation.ComputerDoubleClick:
		value.Keys = copyOptionalSlice(value.Keys)
		return value
	case generation.ComputerDrag:
		value.Keys = copyOptionalSlice(value.Keys)
		value.Path = slices.Clone(value.Path)
		return value
	case generation.ComputerKeypress:
		value.Keys = slices.Clone(value.Keys)
		return value
	case generation.ComputerMove:
		value.Keys = copyOptionalSlice(value.Keys)
		return value
	case generation.ComputerScroll:
		value.Keys = copyOptionalSlice(value.Keys)
		return value
	default:
		return action
	}
}

func computerBytes(item generation.Item) int {
	switch value := item.(type) {
	case generation.OpenAIComputerCall:
		size := len(value.ID) + len(value.CallID) + computerActionBytes(value.Action) + computerCheckBytes(value.PendingSafetyChecks)
		for _, action := range value.Actions {
			size += computerActionBytes(action)
		}
		return size
	case generation.OpenAIComputerResult:
		id, _ := value.ID.Value()
		fileID, _ := value.Output.FileID.Value()
		imageURL, _ := value.Output.ImageURL.Value()
		detail, _ := value.Output.Detail.Value()
		creator, _ := value.CreatedBy.Value()
		checks, _ := value.AcknowledgedSafetyChecks.Value()
		return len(id) + len(value.CallID) + len(fileID) + len(imageURL) + len(detail) + len(creator) + computerCheckBytes(checks)
	default:
		return 0
	}
}

func computerActionBytes(action generation.ComputerAction) int {
	if action == nil {
		return 0
	}
	size := 64
	var keys []string
	switch value := action.(type) {
	case generation.ComputerClick:
		keys, _ = value.Keys.Value()
		size += len(value.Button)
	case generation.ComputerDoubleClick:
		keys, _ = value.Keys.Value()
	case generation.ComputerDrag:
		keys, _ = value.Keys.Value()
		size += 16 * len(value.Path)
	case generation.ComputerKeypress:
		keys = value.Keys
	case generation.ComputerMove:
		keys, _ = value.Keys.Value()
	case generation.ComputerScroll:
		keys, _ = value.Keys.Value()
	case generation.ComputerType:
		size += len(value.Text)
	}
	for _, key := range keys {
		size += 16 + len(key)
	}
	return size
}

func computerCheckBytes(checks []generation.ComputerSafetyCheck) int {
	size := 0
	for _, check := range checks {
		code, _ := check.Code.Value()
		message, _ := check.Message.Value()
		size += 64 + len(check.ID) + len(code) + len(message)
	}
	return size
}
