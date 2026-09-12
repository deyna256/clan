package openai

import (
	"slices"
	"strings"

	"github.com/deyna256/clan/internal/generation"
)

func (s *streamState) mergeMCP(index int, item *streamItem, next generation.Item) ([]generation.Event, error) {
	switch value := next.(type) {
	case generation.OpenAIMCPCall:
		previous := item.item.(generation.OpenAIMCPCall)
		if previous.Name != value.Name || previous.ServerLabel != value.ServerLabel {
			return nil, protocolError()
		}
		var err error
		if value.ApprovalRequestID, err = mergeCallField(previous.ApprovalRequestID, value.ApprovalRequestID); err != nil {
			return nil, err
		}
		if value.Status.IsZero() {
			value.Status = previous.Status
		}
		if value.Output.IsZero() {
			value.Output = previous.Output
		}
		if value.Error.IsZero() {
			value.Error = previous.Error
		}
		arguments := value.Arguments
		value.Arguments = ""
		return s.mergeMCPArguments(index, item, value, arguments)
	case generation.OpenAIMCPApprovalRequest:
		previous := item.item.(generation.OpenAIMCPApprovalRequest)
		if previous.Name != value.Name || previous.ServerLabel != value.ServerLabel {
			return nil, protocolError()
		}
		arguments := value.Arguments
		value.Arguments = ""
		return s.mergeMCPArguments(index, item, value, arguments)
	case generation.OpenAIMCPApprovalResponse:
		previous := item.item.(generation.OpenAIMCPApprovalResponse)
		if previous.ApprovalRequestID != value.ApprovalRequestID || previous.Approve != value.Approve {
			return nil, protocolError()
		}
		if value.Reason.IsZero() {
			value.Reason = previous.Reason
		}
		next = value
	case generation.OpenAIMCPListTools:
		previous := item.item.(generation.OpenAIMCPListTools)
		if previous.ServerLabel != value.ServerLabel {
			return nil, protocolError()
		}
		if value.Error.IsZero() {
			value.Error = previous.Error
		}
		value.Tools = retainMCPToolMetadata(previous.Tools, value.Tools)
		next = value
	}
	if err := s.grow(mcpBytes(next) - mcpBytes(item.item)); err != nil {
		return nil, err
	}
	item.item = next
	if item.started {
		return nil, nil
	}
	item.started = true
	return []generation.Event{generation.ItemStarted{Index: index, Item: copyMCPItem(next)}}, nil
}

func (s *streamState) mergeMCPArguments(index int, item *streamItem, next generation.Item, arguments string) ([]generation.Event, error) {
	if !strings.HasPrefix(arguments, item.arguments.String()) {
		return nil, protocolError()
	}
	suffix := arguments[item.arguments.Len():]
	if err := s.grow(len(suffix) + mcpBytes(next) - mcpBytes(item.item)); err != nil {
		return nil, err
	}
	item.arguments.WriteString(suffix)
	item.item = next
	var events []generation.Event
	if !item.started {
		item.started = true
		events = append(events, generation.ItemStarted{Index: index, Item: copyMCPItem(next)})
	}
	if suffix != "" {
		events = append(events, generation.ArgumentsDelta{ItemIndex: index, Fragment: suffix})
	}
	return events, nil
}

func (s *streamState) mcpArguments(e responseEvent) ([]generation.Event, error) {
	item, err := s.mcpTarget(e, true)
	if err != nil {
		return nil, err
	}
	call := item.item.(generation.OpenAIMCPCall)
	if e.Type == "response.mcp_call_arguments.done" {
		if e.Arguments == nil {
			return nil, protocolError()
		}
		return s.mergeMCPArguments(*e.OutputIndex, item, call, *e.Arguments)
	}
	if e.Delta == nil {
		return nil, protocolError()
	}
	if err := s.grow(len(*e.Delta)); err != nil {
		return nil, err
	}
	item.arguments.WriteString(*e.Delta)
	if *e.Delta == "" {
		return nil, nil
	}
	return []generation.Event{generation.ArgumentsDelta{ItemIndex: *e.OutputIndex, Fragment: *e.Delta}}, nil
}

func (s *streamState) mcpProgress(e responseEvent) ([]generation.Event, error) {
	call := strings.HasPrefix(e.Type, "response.mcp_call.")
	if _, err := s.mcpTarget(e, call); err != nil {
		return nil, err
	}
	status := e.Type[strings.LastIndexByte(e.Type, '.')+1:]
	return []generation.Event{generation.MCPProgress{ItemIndex: *e.OutputIndex, Status: status}}, nil
}

func (s *streamState) mcpTarget(e responseEvent, call bool) (*streamItem, error) {
	if !validIndex(e.OutputIndex) || e.ItemID == "" {
		return nil, protocolError()
	}
	item := s.items[*e.OutputIndex]
	if item == nil || itemID(item.item) != e.ItemID {
		return nil, protocolError()
	}
	kind := "mcp_list"
	if call {
		kind = "mcp_call"
	}
	if itemKind(item.item) != kind {
		return nil, protocolError()
	}
	return item, nil
}

func retainMCPToolMetadata(previous, next []generation.MCPListedTool) []generation.MCPListedTool {
	if len(previous) == 0 {
		return next
	}
	byName := make(map[string]generation.MCPListedTool, len(previous))
	for _, tool := range previous {
		byName[tool.Name] = tool
	}
	next = slices.Clone(next)
	for index := range next {
		tool := &next[index]
		prior := byName[tool.Name]
		if tool.Description.IsZero() {
			tool.Description = prior.Description
		}
		if tool.Annotations.IsZero() {
			tool.Annotations = prior.Annotations
		}
	}
	return next
}

func copyMCPItem(item generation.Item) generation.Item {
	switch value := item.(type) {
	case generation.OpenAIMCPCall:
		if content, ok := value.Error.Value(); ok {
			if execution, ok := content.(generation.MCPExecutionError); ok {
				execution.Content = slices.Clone(execution.Content)
				value.Error = generation.Some[generation.MCPCallError](execution)
			}
		}
		return value
	case generation.OpenAIMCPListTools:
		value.Tools = slices.Clone(value.Tools)
		for index := range value.Tools {
			tool := &value.Tools[index]
			tool.InputSchema = slices.Clone(tool.InputSchema)
			if annotations, ok := tool.Annotations.Value(); ok {
				tool.Annotations = generation.Some(slices.Clone(annotations))
			}
		}
		return value
	default:
		return item
	}
}

func mcpBytes(item generation.Item) int {
	switch value := item.(type) {
	case generation.OpenAIMCPCall:
		approval, _ := value.ApprovalRequestID.Value()
		output, _ := value.Output.Value()
		err, _ := value.Error.Value()
		return len(value.ID) + len(value.Name) + len(value.ServerLabel) + len(approval) + len(output) + mcpErrorBytes(err)
	case generation.OpenAIMCPListTools:
		message, _ := value.Error.Value()
		size := len(value.ID) + len(value.ServerLabel) + len(message)
		for _, tool := range value.Tools {
			annotations, _ := tool.Annotations.Value()
			description, _ := tool.Description.Value()
			size += 64 + len(tool.Name) + len(tool.InputSchema) + len(annotations) + len(description)
		}
		return size
	case generation.OpenAIMCPApprovalRequest:
		return len(value.ID) + len(value.Name) + len(value.ServerLabel)
	case generation.OpenAIMCPApprovalResponse:
		id, _ := value.ID.Value()
		reason, _ := value.Reason.Value()
		return len(id) + len(value.ApprovalRequestID) + len(reason)
	default:
		return 0
	}
}

func mcpErrorBytes(err generation.MCPCallError) int {
	switch value := err.(type) {
	case generation.MCPProtocolError:
		return 32 + len(value.Message)
	case generation.MCPHTTPError:
		return 32 + len(value.Message)
	case generation.MCPExecutionError:
		return 32 + len(value.Content)
	default:
		return 0
	}
}
