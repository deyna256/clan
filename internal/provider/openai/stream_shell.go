package openai

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

type shellOutputState struct {
	stdout, stderr strings.Builder
	output         []generation.ShellOutput
}

func (s *streamState) mergeShellCall(index int, item *streamItem, next generation.OpenAIShellCall) ([]generation.Event, error) {
	previous := item.item.(generation.OpenAIShellCall)
	if previous.CallID != next.CallID || !itemStatusAdvances(previous.Status, next.Status) {
		return nil, protocolError()
	}
	var err error
	if next.Caller, err = mergeCallField(previous.Caller, next.Caller); err != nil {
		return nil, err
	}
	if next.CreatedBy, err = mergeCallField(previous.CreatedBy, next.CreatedBy); err != nil {
		return nil, err
	}
	if next.Action.TimeoutMs, err = mergeCallField(previous.Action.TimeoutMs, next.Action.TimeoutMs); err != nil {
		return nil, err
	}
	if next.Action.MaxOutputLength, err = mergeCallField(previous.Action.MaxOutputLength, next.Action.MaxOutputLength); err != nil {
		return nil, err
	}
	if _, known := previous.Environment.Value(); known && !next.Environment.IsZero() && !reflect.DeepEqual(previous.Environment, next.Environment) {
		return nil, protocolError()
	}
	if next.Environment.IsZero() {
		next.Environment = previous.Environment
	}
	for command := range item.commands {
		if command >= len(next.Action.Commands) {
			return nil, protocolError()
		}
	}
	if err := s.grow(shellBytes(next) - shellBytes(previous)); err != nil {
		return nil, err
	}
	commands := next.Action.Commands
	next.Action.Commands = nil
	item.item = next
	var events []generation.Event
	if !item.started {
		item.started = true
		events = append(events, generation.ItemStarted{Index: index, Item: next})
	}
	for command, text := range commands {
		more, err := s.shellCommandText(index, command, item, text, false)
		events = append(events, more...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

func (s *streamState) shellCommand(e responseEvent) ([]generation.Event, error) {
	if !validIndex(e.OutputIndex) || !validIndex(e.CommandIndex) {
		return nil, protocolError()
	}
	item := s.items[*e.OutputIndex]
	if item == nil {
		return nil, protocolError()
	}
	if _, ok := item.item.(generation.OpenAIShellCall); !ok {
		return nil, protocolError()
	}
	text := e.Command
	delta := e.Type == "response.shell_call_command.delta"
	if delta {
		text = e.Delta
	}
	if text == nil {
		return nil, protocolError()
	}
	return s.shellCommandText(*e.OutputIndex, *e.CommandIndex, item, *text, delta)
}

func (s *streamState) shellCommandText(index, command int, item *streamItem, text string, delta bool) ([]generation.Event, error) {
	if item.commands == nil {
		item.commands = make(map[int]*strings.Builder)
	}
	buffer := item.commands[command]
	if buffer == nil {
		if err := s.grow(64); err != nil {
			return nil, err
		}
		buffer = new(strings.Builder)
		item.commands[command] = buffer
	}
	if !delta {
		if !strings.HasPrefix(text, buffer.String()) {
			return nil, protocolError()
		}
		text = text[buffer.Len():]
	}
	if err := s.grow(len(text)); err != nil {
		return nil, err
	}
	buffer.WriteString(text)
	if text == "" {
		return nil, nil
	}
	return []generation.Event{generation.ShellCommandDelta{ItemIndex: index, CommandIndex: command, Fragment: text}}, nil
}

func (s *streamState) mergeShellResult(index int, item *streamItem, next generation.OpenAIShellResult) ([]generation.Event, error) {
	previous := item.item.(generation.OpenAIShellResult)
	if previous.CallID != next.CallID || !itemStatusAdvances(previous.Status, next.Status) {
		return nil, protocolError()
	}
	if err := mergeShellOutputs(previous.Output, next.Output); err != nil {
		return nil, err
	}
	var err error
	if next.Caller, err = mergeCallField(previous.Caller, next.Caller); err != nil {
		return nil, err
	}
	if next.CreatedBy, err = mergeCallField(previous.CreatedBy, next.CreatedBy); err != nil {
		return nil, err
	}
	if next.MaxOutputLength, err = mergeCallField(previous.MaxOutputLength, next.MaxOutputLength); err != nil {
		return nil, err
	}
	if err := s.grow(shellBytes(next) - shellBytes(previous)); err != nil {
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

func (s *streamState) shellOutput(e responseEvent) ([]generation.Event, error) {
	if !validIndex(e.OutputIndex) || !validIndex(e.CommandIndex) {
		return nil, protocolError()
	}
	item := s.items[*e.OutputIndex]
	if item == nil || e.ItemID == "" || e.ItemID != itemID(item.item) {
		return nil, protocolError()
	}
	if _, ok := item.item.(generation.OpenAIShellResult); !ok {
		return nil, protocolError()
	}
	if item.shellOutputs == nil {
		item.shellOutputs = make(map[int]*shellOutputState)
	}
	output := item.shellOutputs[*e.CommandIndex]
	if output == nil {
		if err := s.grow(128); err != nil {
			return nil, err
		}
		output = new(shellOutputState)
		item.shellOutputs[*e.CommandIndex] = output
	}
	if e.Type == "response.shell_call_output_content.done" {
		return s.shellOutputDone(e, output)
	}
	var delta *struct {
		Stdout generation.Optional[string] `json:"stdout"`
		Stderr generation.Optional[string] `json:"stderr"`
	}
	if json.Unmarshal(e.RawDelta, &delta) != nil || delta == nil || delta.Stdout.IsNull() || delta.Stderr.IsNull() {
		return nil, protocolError()
	}
	stdout, _ := delta.Stdout.Value()
	stderr, _ := delta.Stderr.Value()
	return s.appendShellOutput(e, output, stdout, stderr)
}

func (s *streamState) shellOutputDone(e responseEvent, state *shellOutputState) ([]generation.Event, error) {
	output, err := wire.DecodeShellOutputs(e.Output)
	if err != nil {
		return nil, err
	}
	if err := mergeShellOutputs(state.output, output); err != nil {
		return nil, err
	}
	var stdout, stderr strings.Builder
	for _, chunk := range output {
		stdout.WriteString(chunk.Stdout)
		stderr.WriteString(chunk.Stderr)
	}
	if !strings.HasPrefix(stdout.String(), state.stdout.String()) || !strings.HasPrefix(stderr.String(), state.stderr.String()) {
		return nil, protocolError()
	}
	if err := s.grow(shellOutputBytes(output) - shellOutputBytes(state.output)); err != nil {
		return nil, err
	}
	events, err := s.appendShellOutput(e, state, stdout.String()[state.stdout.Len():], stderr.String()[state.stderr.Len():])
	if err != nil {
		return events, err
	}
	if !reflect.DeepEqual(state.output, output) {
		state.output = output
		events = append(events, generation.ShellOutputEnded{ItemIndex: *e.OutputIndex, CommandIndex: *e.CommandIndex, Output: slices.Clone(output)})
	}
	return events, nil
}

func (s *streamState) appendShellOutput(e responseEvent, output *shellOutputState, stdout, stderr string) ([]generation.Event, error) {
	if err := s.grow(len(stdout) + len(stderr)); err != nil {
		return nil, err
	}
	output.stdout.WriteString(stdout)
	output.stderr.WriteString(stderr)
	if stdout == "" && stderr == "" {
		return nil, nil
	}
	return []generation.Event{generation.ShellOutputDelta{ItemIndex: *e.OutputIndex, CommandIndex: *e.CommandIndex, Stdout: stdout, Stderr: stderr}}, nil
}

func mergeShellOutputs(previous, next []generation.ShellOutput) error {
	if len(next) < len(previous) {
		return protocolError()
	}
	for index, before := range previous {
		after := &next[index]
		if !strings.HasPrefix(after.Stdout, before.Stdout) || !strings.HasPrefix(after.Stderr, before.Stderr) || before.Outcome != after.Outcome {
			return protocolError()
		}
		var err error
		if after.CreatedBy, err = mergeCallField(before.CreatedBy, after.CreatedBy); err != nil {
			return err
		}
	}
	return nil
}

func (s *streamState) mergeLocalShell(index int, item *streamItem, next generation.Item) ([]generation.Event, error) {
	switch value := next.(type) {
	case generation.OpenAILocalShellCall:
		previous := item.item.(generation.OpenAILocalShellCall)
		if previous.CallID != value.CallID || !itemStatusAdvances(generation.Some(previous.Status), generation.Some(value.Status)) {
			return nil, protocolError()
		}
		action, err := mergeLocalShellAction(previous.Action, value.Action)
		if err != nil {
			return nil, err
		}
		value.Action = action
		next = value
	case generation.OpenAILocalShellResult:
		previous := item.item.(generation.OpenAILocalShellResult)
		if value.Status.IsZero() {
			value.Status = previous.Status
		}
		next = value
		if !strings.HasPrefix(value.Output, previous.Output) || !itemStatusAdvances(previous.Status, value.Status) {
			return nil, protocolError()
		}
	}
	if err := s.grow(shellBytes(next) - shellBytes(item.item)); err != nil {
		return nil, err
	}
	item.item = next
	if item.started {
		return nil, nil
	}
	item.started = true
	return []generation.Event{generation.ItemStarted{Index: index, Item: copyLocalShellItem(next)}}, nil
}

func mergeLocalShellAction(previous, next generation.LocalShellAction) (generation.LocalShellAction, error) {
	if len(next.Command) < len(previous.Command) {
		return next, protocolError()
	}
	for index, argument := range previous.Command {
		if !strings.HasPrefix(next.Command[index], argument) {
			return next, protocolError()
		}
	}
	for key, value := range previous.Env {
		if after, ok := next.Env[key]; !ok || after != value {
			return next, protocolError()
		}
	}
	var err error
	if next.TimeoutMs, err = mergeCallField(previous.TimeoutMs, next.TimeoutMs); err != nil {
		return next, err
	}
	if next.User, err = mergeCallField(previous.User, next.User); err != nil {
		return next, err
	}
	next.WorkingDirectory, err = mergeCallField(previous.WorkingDirectory, next.WorkingDirectory)
	return next, err
}

func copyLocalShellItem(item generation.Item) generation.Item {
	value, ok := item.(generation.OpenAILocalShellCall)
	if !ok {
		return item
	}
	value.Action.Command = slices.Clone(value.Action.Command)
	value.Action.Env = maps.Clone(value.Action.Env)
	return value
}

func shellBytes(item generation.Item) int {
	switch value := item.(type) {
	case generation.OpenAIShellCall:
		id, _ := value.ID.Value()
		creator, _ := value.CreatedBy.Value()
		size := len(id) + len(value.CallID) + len(creator) + callerBytes(value.Caller)
		environment, _ := value.Environment.Value()
		if reference, ok := environment.(generation.ShellContainerReference); ok {
			size += len(reference.ContainerID)
		}
		return size
	case generation.OpenAIShellResult:
		id, _ := value.ID.Value()
		creator, _ := value.CreatedBy.Value()
		return len(id) + len(value.CallID) + len(creator) + callerBytes(value.Caller) + shellOutputBytes(value.Output)
	case generation.OpenAILocalShellCall:
		user, _ := value.Action.User.Value()
		directory, _ := value.Action.WorkingDirectory.Value()
		size := len(value.ID) + len(value.CallID) + len(user) + len(directory)
		for _, command := range value.Action.Command {
			size += 16 + len(command)
		}
		for key, entry := range value.Action.Env {
			size += 32 + len(key) + len(entry)
		}
		return size
	case generation.OpenAILocalShellResult:
		return len(value.ID) + len(value.Output)
	default:
		return 0
	}
}

func shellOutputBytes(outputs []generation.ShellOutput) int {
	size := 0
	for _, output := range outputs {
		creator, _ := output.CreatedBy.Value()
		size += 64 + len(output.Stdout) + len(output.Stderr) + len(creator)
	}
	return size
}
