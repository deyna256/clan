package openai

import (
	"encoding/json"
	"io"
	"reflect"
	"strings"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/deyna256/clan/internal/usage"
)

type streamState struct {
	expectedID  string
	identity    generation.Identity
	started     bool
	items       map[int]*streamItem
	order       []int
	usage       usage.State
	diagnostics *diagnostics
	limit, size int64
}

type streamItem struct {
	item         generation.Item
	started      bool
	parts        map[partKey]*streamPart
	order        []partKey
	arguments    strings.Builder
	code         strings.Builder
	commands     map[int]*strings.Builder
	shellOutputs map[int]*shellOutputState
}

type partKey struct {
	index   int
	summary bool
}

type streamPart struct {
	address generation.PartAddress
	part    generation.Part
	text    strings.Builder
}

func (s *streamState) consume(e responseEvent) ([]generation.Event, error) {
	switch e.Type {
	case "response.created", "response.in_progress":
		r, events, err := s.observeResponse(e.Response)
		if err != nil {
			return events, err
		}
		start, err := s.start(r.ID, r.Model)
		return append(start, events...), err
	case "response.completed", "response.incomplete", "response.failed":
		return s.complete(e)
	case "error":
		code := ""
		if e.Code != nil {
			code = *e.Code
		}
		return nil, (wire.ProviderError{Code: code}).Failure()
	case "response.output_item.added", "response.output_item.done":
		if !s.started || !validIndex(e.OutputIndex) {
			return nil, protocolError()
		}
		item, err := wire.DecodeItem(e.Item, false, s.diagnostics.report)
		if err != nil {
			return nil, err
		}
		if itemID(item) == "" {
			return nil, protocolError()
		}
		return s.mergeItem(*e.OutputIndex, item)
	case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part, err := wire.DecodePart(e.Part, s.diagnostics.report)
		if err != nil {
			return nil, err
		}
		return s.mergePartEvent(e, part)
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		return s.textDelta(e)
	case "response.output_text.annotation.added":
		return s.annotation(e)
	case "response.output_text.done", "response.refusal.done", "response.reasoning_text.done", "response.reasoning_summary_text.done":
		text := e.Text
		if e.Type == "response.refusal.done" {
			text = e.Refusal
		}
		if text == nil {
			return nil, protocolError()
		}
		part := textPartFor(e.Type, *text)
		if value, ok := part.(generation.Text); ok {
			probabilities, err := wire.DecodeLogprobs(e.Logprobs)
			if err != nil {
				s.diagnostics.report("invalid_logprobs")
			}
			value.OpenAI.Logprobs = probabilities
			part = value
		}
		return s.mergePartEvent(e, part)
	case "response.mcp_call_arguments.delta", "response.mcp_call_arguments.done":
		return s.mcpArguments(e)
	case "response.mcp_call.in_progress", "response.mcp_call.completed", "response.mcp_call.failed",
		"response.mcp_list_tools.in_progress", "response.mcp_list_tools.completed", "response.mcp_list_tools.failed":
		return s.mcpProgress(e)
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		return s.arguments(e)
	case "response.shell_call_command.added", "response.shell_call_command.delta", "response.shell_call_command.done":
		return s.shellCommand(e)
	case "response.shell_call_output_content.delta", "response.shell_call_output_content.done":
		return s.shellOutput(e)
	case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		return s.customInput(e)
	case "response.code_interpreter_call_code.delta", "response.code_interpreter_call_code.done":
		return s.interpreterCode(e)
	case "response.image_generation_call.partial_image":
		return s.imagePreview(e)
	case "response.image_generation_call.in_progress", "response.image_generation_call.generating", "response.image_generation_call.completed":
		return nil, nil
	case "response.ping", "ping", "response.queued":
		return nil, nil
	case "response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed",
		"response.file_search_call.in_progress", "response.file_search_call.searching", "response.file_search_call.completed":
		return nil, nil
	case "response.code_interpreter_call.in_progress", "response.code_interpreter_call.interpreting", "response.code_interpreter_call.completed":
		return nil, nil
	default:
		s.diagnostics.warn("unknown_event", e.Type, "skipped")
		return nil, nil
	}
}

func (s *streamState) start(id, model string) ([]generation.Event, error) {
	identity := generation.Identity{ID: id, Model: model}
	if id == "" || model == "" {
		return nil, protocolError()
	}
	if s.started {
		return nil, nil
	}
	if err := s.grow(len(id) + len(model)); err != nil {
		return nil, err
	}
	s.started, s.identity = true, identity
	return []generation.Event{generation.ResponseStarted{Identity: identity}}, nil
}

func (s *streamState) observeResponse(raw json.RawMessage) (wire.ResponseEnvelope, []generation.Event, error) {
	response, err := wire.DecodeEnvelope(raw)
	expectedID := s.expectedID
	if s.started {
		expectedID = s.identity.ID
	}
	if response.ID != "" && expectedID != "" && response.ID != expectedID {
		return response, nil, protocolError()
	}
	if s.started && response.Model != "" && response.Model != s.identity.Model {
		return response, nil, protocolError()
	}
	return response, s.observe(response.Usage), err
}

func (s *streamState) observe(raw json.RawMessage) []generation.Event {
	previous := s.usage
	next, invalid := wire.NormalizeUsage(raw, previous)
	s.usage = next
	if invalid {
		s.diagnostics.warn("invalid_usage", "response", "kept_valid_counters")
	}
	if previous.Usage == next.Usage {
		return nil
	}
	return []generation.Event{generation.UsageUpdated{Usage: next.Usage}}
}

func (s *streamState) complete(e responseEvent) ([]generation.Event, error) {
	r, events, err := s.observeResponse(e.Response)
	if err != nil {
		return events, err
	}
	if e.Type != "response."+r.Status {
		return events, protocolError()
	}
	result, err := r.Result(s.diagnostics.report)
	if err != nil {
		return events, err
	}
	start, err := s.start(r.ID, r.Model)
	if err != nil {
		return events, err
	}
	events = append(start, events...)
	for index := range s.items {
		if index >= len(result.Response.Output) {
			return events, protocolError()
		}
	}
	for index, item := range result.Response.Output {
		previousID := ""
		if previous := s.items[index]; previous != nil {
			previousID = itemID(previous.item)
		}
		switch call := item.(type) {
		case generation.CustomToolCall:
			if call.ID == "" {
				call.ID = previousID
			}
			item = call
		case generation.ToolCall:
			if call.ID == "" {
				call.ID = previousID
			}
			item = call
		}
		more, err := s.mergeItem(index, item)
		events = append(events, more...)
		if err != nil {
			return events, err
		}
	}
	// End snapshots wait for response completion so late metadata is not lost.
	for _, index := range s.order {
		item := s.items[index]
		if err := validateImageCompletion(item.item); err != nil {
			return events, err
		}
		if err := validateStateItemCompletion(item.item); err != nil {
			return events, err
		}
		for _, key := range item.order {
			part := item.parts[key]
			events = append(events, generation.PartEnded{Address: part.address, Part: copyPart(withText(part.part, part.text.String()))})
		}
		events = append(events, generation.ItemEnded{Index: index, Item: item.snapshot()})
	}
	events = append(events, generation.ResponseEnded{Finish: result.Response.Finish})
	return events, io.EOF
}

func (s *streamState) mergeItem(index int, value generation.Item) ([]generation.Event, error) {
	item := s.items[index]
	var events []generation.Event
	if item == nil {
		if err := s.grow(64 + metadataBytes(value)); err != nil {
			return nil, err
		}
		item = &streamItem{item: value, parts: make(map[partKey]*streamPart)}
		s.items[index] = item
		s.order = append(s.order, index)
	} else if itemID(item.item) != itemID(value) || reflect.TypeOf(item.item) != reflect.TypeOf(value) {
		return nil, protocolError()
	}
	switch call := value.(type) {
	case generation.OpenAICompaction:
		return s.mergeCompaction(index, item, call)
	case generation.OpenAIProgram, generation.OpenAIProgramOutput:
		return s.mergeProgram(index, item, call)
	case generation.OpenAIToolSearchCall, generation.OpenAIToolSearchOutput, generation.OpenAIAdditionalTools:
		return s.mergeToolSearch(index, item, call)
	case generation.OpenAIMCPCall, generation.OpenAIMCPListTools, generation.OpenAIMCPApprovalRequest, generation.OpenAIMCPApprovalResponse:
		return s.mergeMCP(index, item, call)
	case generation.OpenAIComputerCall, generation.OpenAIComputerResult:
		return s.mergeComputer(index, item, call)
	case generation.OpenAIShellCall:
		return s.mergeShellCall(index, item, call)
	case generation.OpenAIShellResult:
		return s.mergeShellResult(index, item, call)
	case generation.OpenAILocalShellCall, generation.OpenAILocalShellResult:
		return s.mergeLocalShell(index, item, call)
	}
	if call, ok := value.(generation.OpenAICodeInterpreterCall); ok {
		return s.mergeInterpreter(index, item, call)
	}
	if call, ok := value.(generation.CustomToolCall); ok {
		return s.mergeCustomCall(index, item, call)
	}
	if call, ok := value.(generation.OpenAIApplyPatchCall); ok {
		return s.mergePatchCall(index, item, call)
	}
	if result, ok := value.(generation.OpenAIApplyPatchResult); ok {
		return s.mergePatchResult(index, item, result)
	}
	if result, ok := value.(generation.CustomToolResult); ok {
		return s.mergeCustomResult(index, item, result)
	}
	if call, ok := value.(generation.OpenAIImageGenerationCall); ok {
		previous := item.item.(generation.OpenAIImageGenerationCall)
		prior, known := previous.Result.Value()
		if result, ok := call.Result.Value(); ok {
			if known && prior != result {
				return nil, protocolError()
			}
		} else if known {
			call.Result = previous.Result
		}
		call.Metadata = retainImageMetadata(previous.Metadata, call.Metadata)
		value = call
	}
	if call, ok := value.(generation.ToolCall); ok {
		return s.mergeFunctionCall(index, item, call)
	}
	if result, ok := value.(generation.ToolResult); ok {
		return s.mergeFunctionResult(index, item, result)
	}
	value = retainMetadata(item.item, value)
	if err := s.grow(metadataBytes(value) - metadataBytes(item.item)); err != nil {
		return events, err
	}
	item.item = emptyItem(value)
	if !item.started {
		item.started = true
		events = append(events, generation.ItemStarted{Index: index, Item: startItem(value)})
	}
	textIndex, summaryIndex := 0, 0
	for _, part := range itemParts(value) {
		_, summary := part.(generation.ReasoningSummary)
		key := partKey{index: textIndex}
		if summary {
			key = partKey{index: summaryIndex, summary: true}
			summaryIndex++
		} else {
			textIndex++
		}
		more, err := s.mergePart(index, key, part)
		events = append(events, more...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

func (s *streamState) mergePartEvent(e responseEvent, part generation.Part) ([]generation.Event, error) {
	item, key, err := s.target(e)
	if err != nil {
		return nil, err
	}
	return s.mergePart(item, key, part)
}

func (s *streamState) mergePart(index int, key partKey, value generation.Part) ([]generation.Event, error) {
	item := s.items[index]
	if !acceptsPart(item.item, key, value) {
		return nil, protocolError()
	}
	part := item.parts[key]
	var events []generation.Event
	if part == nil {
		if err := s.grow(64); err != nil {
			return nil, err
		}
		part = &streamPart{address: generation.PartAddress{Item: index, Part: len(item.order)}, part: emptyPart(value)}
		item.parts[key] = part
		item.order = append(item.order, key)
		events = append(events, generation.PartStarted{Address: part.address, Part: emptyPart(value)})
	} else if partKind(part.part) != partKind(value) {
		return nil, protocolError()
	}
	text := partText(value)
	if !strings.HasPrefix(text, part.text.String()) {
		s.diagnostics.warn("text_snapshot_conflict", "response", "kept_emitted_text")
		return events, nil
	}
	suffix := text[part.text.Len():]
	if err := s.grow(len(suffix)); err != nil {
		return events, err
	}
	part.text.WriteString(suffix)
	if suffix != "" {
		events = append(events, generation.TextDelta{Address: part.address, Text: suffix})
	}
	if text, ok := value.(generation.Text); ok {
		more, err := s.mergeTextMetadata(part, text.OpenAI)
		return append(events, more...), err
	}
	part.part = withText(value, "")
	return events, nil
}

func (s *streamState) textDelta(e responseEvent) ([]generation.Event, error) {
	index, key, err := s.target(e)
	if err != nil || e.Delta == nil {
		return nil, protocolError()
	}
	item := s.items[index]
	part := item.parts[key]
	var events []generation.Event
	if part == nil {
		events, err = s.mergePart(index, key, textPartFor(e.Type, ""))
		if err != nil {
			return events, err
		}
		part = item.parts[key]
	}
	if partKind(part.part) != partKind(textPartFor(e.Type, "")) {
		return events, protocolError()
	}
	if err := s.grow(len(*e.Delta)); err != nil {
		return events, err
	}
	part.text.WriteString(*e.Delta)
	if *e.Delta != "" {
		events = append(events, generation.TextDelta{Address: part.address, Text: *e.Delta})
	}
	if e.Type == "response.output_text.delta" {
		probabilities, err := wire.DecodeLogprobs(e.Logprobs)
		if err != nil {
			s.diagnostics.report("invalid_logprobs")
		}
		more, err := s.appendLogprobs(part, probabilities)
		return append(events, more...), err
	}
	return events, nil
}

func (s *streamState) arguments(e responseEvent) ([]generation.Event, error) {
	if !validIndex(e.OutputIndex) {
		return nil, protocolError()
	}
	item := s.items[*e.OutputIndex]
	if item == nil || (e.ItemID != "" && itemID(item.item) != e.ItemID) {
		return nil, protocolError()
	}
	call, ok := item.item.(generation.ToolCall)
	if !ok {
		return nil, protocolError()
	}
	if e.Type == "response.function_call_arguments.delta" {
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
		return []generation.Event{generation.ArgumentsDelta{ItemIndex: *e.OutputIndex, Fragment: *e.Delta}}, nil
	}
	if e.Arguments == nil {
		return nil, protocolError()
	}
	call.Arguments = *e.Arguments
	return s.mergeItem(*e.OutputIndex, call)
}

func (s *streamState) target(e responseEvent) (int, partKey, error) {
	index := e.ContentIndex
	summary := strings.Contains(e.Type, "reasoning_summary")
	if summary {
		index = e.SummaryIndex
	}
	if !validIndex(e.OutputIndex) || !validIndex(index) {
		return 0, partKey{}, protocolError()
	}
	item := s.items[*e.OutputIndex]
	if item == nil || (e.ItemID != "" && e.ItemID != itemID(item.item)) {
		return 0, partKey{}, protocolError()
	}
	return *e.OutputIndex, partKey{index: *index, summary: summary}, nil
}

func (s *streamState) grow(bytes int) error {
	if int64(bytes) > s.limit-s.size {
		return protocolError()
	}
	s.size += int64(bytes)
	return nil
}

func validIndex(index *int) bool { return index != nil && *index >= 0 }

func itemID(item generation.Item) string {
	switch value := item.(type) {
	case generation.OpenAICompaction:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAIProgram:
		return value.ID
	case generation.OpenAIProgramOutput:
		return value.ID
	case generation.OpenAIToolSearchCall:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAIToolSearchOutput:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAIAdditionalTools:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAIMCPCall:
		return value.ID
	case generation.OpenAIMCPListTools:
		return value.ID
	case generation.OpenAIMCPApprovalRequest:
		return value.ID
	case generation.OpenAIMCPApprovalResponse:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAIComputerCall:
		return value.ID
	case generation.OpenAIComputerResult:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAIShellCall:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAIShellResult:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAILocalShellCall:
		return value.ID
	case generation.OpenAILocalShellResult:
		return value.ID
	case generation.OpenAIApplyPatchCall:
		return value.ID
	case generation.OpenAIApplyPatchResult:
		return value.ID
	case generation.CustomToolCall:
		return value.ID
	case generation.CustomToolResult:
		return value.ID
	case generation.OpenAIImageGenerationCall:
		return value.ID
	case generation.OpenAICodeInterpreterCall:
		return value.ID
	case generation.Message:
		return value.ID
	case generation.Reasoning:
		return value.ID
	case generation.ToolCall:
		return value.ID
	case generation.ToolResult:
		id, _ := value.ID.Value()
		return id
	case generation.OpenAIWebSearchCall:
		return value.ID
	case generation.OpenAIFileSearchCall:
		return value.ID
	default:
		return ""
	}
}

func itemParts(item generation.Item) []generation.Part {
	switch value := item.(type) {
	case generation.Message:
		return value.Parts
	case generation.Reasoning:
		return value.Parts
	default:
		return nil
	}
}

func emptyItem(item generation.Item) generation.Item {
	switch value := item.(type) {
	case generation.Message:
		value.Parts = nil
		return value
	case generation.Reasoning:
		value.Parts = nil
		return value
	case generation.ToolCall:
		value.Arguments = ""
		return value
	default:
		return item
	}
}

func (item *streamItem) snapshot() generation.Item {
	parts := make([]generation.Part, 0, len(item.order))
	for _, key := range item.order {
		part := item.parts[key]
		parts = append(parts, copyPart(withText(part.part, part.text.String())))
	}
	switch value := item.item.(type) {
	case generation.OpenAIMCPCall:
		value.Arguments = item.arguments.String()
		return copyMCPItem(value)
	case generation.OpenAIMCPApprovalRequest:
		value.Arguments = item.arguments.String()
		return value
	case generation.OpenAIMCPListTools, generation.OpenAIMCPApprovalResponse:
		return copyMCPItem(value)
	case generation.OpenAIComputerCall, generation.OpenAIComputerResult:
		return copyComputerItem(value)
	case generation.OpenAIShellCall:
		value.Action.Commands = make([]string, len(item.commands))
		for index, command := range item.commands {
			value.Action.Commands[index] = command.String()
		}
		return value
	case generation.OpenAIShellResult, generation.OpenAILocalShellCall, generation.OpenAILocalShellResult:
		return copyShellItem(value)
	case generation.ToolCall:
		value.Arguments = item.arguments.String()
		return value
	case generation.CustomToolCall:
		value.Input = item.arguments.String()
		return value
	case generation.OpenAICodeInterpreterCall:
		if _, known := value.Code.Value(); known {
			value.Code = generation.Some(item.code.String())
		}
		return value
	case generation.Message:
		value.Parts = parts
		return value
	case generation.Reasoning:
		value.Parts = parts
		return value
	default:
		return item.item
	}
}

func textPartFor(eventType, text string) generation.Part {
	switch {
	case strings.Contains(eventType, "reasoning_summary"):
		return generation.ReasoningSummary{Text: text}
	case strings.Contains(eventType, "reasoning_text"):
		return generation.ReasoningText{Text: text}
	case strings.Contains(eventType, "refusal"):
		return generation.Refusal{Text: text}
	default:
		return generation.Text{Text: text}
	}
}

func partKind(part generation.Part) string {
	switch part.(type) {
	case generation.Text:
		return "text"
	case generation.Refusal:
		return "refusal"
	case generation.ReasoningText:
		return "reasoning_text"
	case generation.ReasoningSummary:
		return "reasoning_summary"
	default:
		return ""
	}
}

func partText(part generation.Part) string {
	switch value := part.(type) {
	case generation.Text:
		return value.Text
	case generation.Refusal:
		return value.Text
	case generation.ReasoningText:
		return value.Text
	case generation.ReasoningSummary:
		return value.Text
	default:
		return ""
	}
}

func withText(part generation.Part, text string) generation.Part {
	switch value := part.(type) {
	case generation.Text:
		value.Text = text
		return value
	case generation.Refusal:
		value.Text = text
		return value
	case generation.ReasoningText:
		value.Text = text
		return value
	case generation.ReasoningSummary:
		value.Text = text
		return value
	default:
		return part
	}
}

func acceptsPart(item generation.Item, key partKey, part generation.Part) bool {
	switch item.(type) {
	case generation.Message:
		return !key.summary && (partKind(part) == "text" || partKind(part) == "refusal")
	case generation.Reasoning:
		if key.summary {
			return partKind(part) == "reasoning_summary"
		}
		return partKind(part) == "reasoning_text"
	default:
		return false
	}
}

func retainMetadata(previous, next generation.Item) generation.Item {
	switch value := next.(type) {
	case generation.Message:
		if value.OpenAI.Phase.IsZero() {
			value.OpenAI = previous.(generation.Message).OpenAI
		}
		return value
	case generation.Reasoning:
		if value.OpenAI.EncryptedContent.IsZero() {
			value.OpenAI = previous.(generation.Reasoning).OpenAI
		}
		return value
	case generation.OpenAIWebSearchCall:
		value.Action = retainSearchAction(previous.(generation.OpenAIWebSearchCall).Action, value.Action)
		return value
	case generation.OpenAIFileSearchCall:
		prior := previous.(generation.OpenAIFileSearchCall)
		if value.Queries == nil {
			value.Queries = prior.Queries
		}
		if value.Results.IsZero() {
			value.Results = prior.Results
		}
		return value
	default:
		return next
	}
}

func metadataBytes(item generation.Item) int {
	switch value := item.(type) {
	case generation.OpenAICompaction:
		return compactionBytes(value)
	case generation.OpenAIProgram, generation.OpenAIProgramOutput:
		return programBytes(value)
	case generation.OpenAIToolSearchCall, generation.OpenAIToolSearchOutput, generation.OpenAIAdditionalTools:
		return toolSearchBytes(value)
	case generation.OpenAIMCPCall, generation.OpenAIMCPListTools, generation.OpenAIMCPApprovalRequest, generation.OpenAIMCPApprovalResponse:
		return mcpBytes(value)
	case generation.OpenAIComputerCall, generation.OpenAIComputerResult:
		return computerBytes(value)
	case generation.OpenAIShellCall, generation.OpenAIShellResult, generation.OpenAILocalShellCall, generation.OpenAILocalShellResult:
		return shellBytes(value)
	case generation.OpenAIApplyPatchCall:
		_, path, diff := patchOperationData(value.Operation)
		creator, _ := value.CreatedBy.Value()
		return len(creator) + len(value.ID) + len(value.CallID) + len(path) + len(diff) + callerBytes(value.Caller)
	case generation.OpenAIApplyPatchResult:
		output, _ := value.Output.Value()
		creator, _ := value.CreatedBy.Value()
		return len(creator) + len(value.ID) + len(value.CallID) + len(output) + callerBytes(value.Caller)
	case generation.CustomToolCall:
		namespace, _ := value.OpenAI.Namespace.Value()
		return len(value.ID) + len(value.CallID) + len(value.Name) + len(namespace) + callerBytes(value.OpenAI.Caller)
	case generation.CustomToolResult:
		creator, _ := value.CreatedBy.Value()
		return len(creator) + len(value.ID) + len(value.CallID) + callerBytes(value.Caller) + toolOutputBytes(value.Output)
	case generation.OpenAIImageGenerationCall:
		result, _ := value.Result.Value()
		return len(value.ID) + len(result) + imageMetadataBytes(value.Metadata)
	case generation.OpenAICodeInterpreterCall:
		return interpreterBytes(value)
	case generation.Message:
		phase, _ := value.OpenAI.Phase.Value()
		return len(value.ID) + len(phase)
	case generation.Reasoning:
		encrypted, _ := value.OpenAI.EncryptedContent.Value()
		return len(value.ID) + len(encrypted)
	case generation.ToolCall:
		namespace, _ := value.OpenAI.Namespace.Value()
		return len(value.ID) + len(value.CallID) + len(value.Name) + len(namespace) + callerBytes(value.OpenAI.Caller)
	case generation.ToolResult:
		id, _ := value.ID.Value()
		callID, _ := value.CallID.Value()
		name, _ := value.OpenAI.Name.Value()
		namespace, _ := value.OpenAI.Namespace.Value()
		creator, _ := value.OpenAI.CreatedBy.Value()
		return len(id) + len(callID) + len(name) + len(namespace) + len(creator) + callerBytes(value.Caller) + toolOutputBytes(value.Output)
	case generation.OpenAIWebSearchCall:
		return webSearchBytes(value)
	case generation.OpenAIFileSearchCall:
		return fileSearchBytes(value)
	default:
		return 0
	}
}

func itemStatusAdvances(previous, next generation.Optional[string]) bool {
	before, known := previous.Value()
	after, _ := next.Value()
	return !known || before == "in_progress" || before == after
}
