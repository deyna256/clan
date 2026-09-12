package wire

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type mcpListedTool struct {
	Name        string                               `json:"name"`
	InputSchema json.RawMessage                      `json:"input_schema"`
	Annotations generation.Optional[json.RawMessage] `json:"annotations,omitzero"`
	Description generation.Optional[string]          `json:"description,omitzero"`
}
type mcpListTools struct {
	Type        string                      `json:"type"`
	ID          string                      `json:"id"`
	ServerLabel string                      `json:"server_label"`
	Tools       []mcpListedTool             `json:"tools"`
	Error       generation.Optional[string] `json:"error,omitzero"`
}
type mcpCall struct {
	Type              string                      `json:"type"`
	ID                string                      `json:"id"`
	Name              string                      `json:"name"`
	ServerLabel       string                      `json:"server_label"`
	Arguments         *string                     `json:"arguments"`
	Status            generation.Optional[string] `json:"status,omitzero"`
	ApprovalRequestID generation.Optional[string] `json:"approval_request_id,omitzero"`
	Output            generation.Optional[string] `json:"output,omitzero"`
	Error             json.RawMessage             `json:"error,omitempty"`
}
type mcpApprovalRequest struct {
	Type        string  `json:"type"`
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	ServerLabel string  `json:"server_label"`
	Arguments   *string `json:"arguments"`
}
type mcpApprovalResponse struct {
	Type              string                      `json:"type"`
	ID                generation.Optional[string] `json:"id,omitzero"`
	ApprovalRequestID string                      `json:"approval_request_id"`
	Approve           *bool                       `json:"approve"`
	Reason            generation.Optional[string] `json:"reason,omitzero"`
}

func mcpIdentity(id, server string) error {
	if err := requiredString("id", id); err != nil {
		return err
	}
	return requiredString("server_label", server)
}

func encodeMCPListTools(list generation.OpenAIMCPListTools) (json.RawMessage, error) {
	if err := mcpIdentity(list.ID, list.ServerLabel); err != nil {
		return nil, err
	}
	if err := mcpString("error", list.Error, true); err != nil {
		return nil, err
	}
	tools := make([]mcpListedTool, len(list.Tools))
	for i, tool := range list.Tools {
		tools[i] = mcpListedTool{tool.Name, tool.InputSchema, tool.Annotations, tool.Description}
	}
	if err := validateMCPListedTools(tools); err != nil {
		return nil, at("tools", err)
	}
	return json.Marshal(mcpListTools{"mcp_list_tools", list.ID, list.ServerLabel, tools, list.Error})
}

func validateMCPListedTools(tools []mcpListedTool) error {
	seen := make(map[string]bool, len(tools))
	for i, tool := range tools {
		if err := requiredString("name", tool.Name); err != nil {
			return at(fmt.Sprintf("[%d]", i), err)
		}
		if seen[tool.Name] {
			return invalid("name", "duplicate MCP tool name")
		}
		seen[tool.Name] = true
		if err := mcpJSON("input_schema", tool.InputSchema); err != nil {
			return err
		}
		if annotations, ok := tool.Annotations.Value(); ok {
			if err := mcpJSON("annotations", annotations); err != nil {
				return err
			}
		}
		if err := mcpString("description", tool.Description, true); err != nil {
			return err
		}
	}
	return nil
}

func decodeMCPListTools(raw json.RawMessage) (generation.Item, error) {
	var value mcpListTools
	if json.Unmarshal(raw, &value) != nil || value.Tools == nil || mcpIdentity(value.ID, value.ServerLabel) != nil || validateMCPListedTools(value.Tools) != nil {
		return nil, failure(generation.ProtocolError)
	}
	tools := make([]generation.MCPListedTool, len(value.Tools))
	for i, tool := range value.Tools {
		tools[i] = generation.MCPListedTool{Name: tool.Name, InputSchema: tool.InputSchema, Annotations: tool.Annotations, Description: tool.Description}
	}
	return generation.OpenAIMCPListTools{ID: value.ID, ServerLabel: value.ServerLabel, Tools: tools, Error: value.Error}, nil
}

func validateMCPCall(call generation.OpenAIMCPCall) error {
	if err := mcpIdentity(call.ID, call.ServerLabel); err != nil {
		return err
	}
	if err := requiredString("name", call.Name); err != nil {
		return err
	}
	if !utf8.ValidString(call.Arguments) {
		return invalid("arguments", "must be valid UTF-8")
	}
	if call.Status.IsNull() {
		return invalid("status", "must not be null")
	}
	if status, ok := call.Status.Value(); ok {
		switch status {
		case "in_progress", "completed", "incomplete", "calling", "failed":
		default:
			return invalid("status", "unsupported MCP call status")
		}
	}
	if id, ok := call.ApprovalRequestID.Value(); ok {
		if err := requiredString("approval_request_id", id); err != nil {
			return err
		}
	}
	return mcpString("output", call.Output, true)
}

func encodeMCPCall(call generation.OpenAIMCPCall) (json.RawMessage, error) {
	if err := validateMCPCall(call); err != nil {
		return nil, err
	}
	callError, err := encodeMCPError(call.Error)
	if err != nil {
		return nil, at("error", err)
	}
	return json.Marshal(mcpCall{"mcp_call", call.ID, call.Name, call.ServerLabel, &call.Arguments, call.Status, call.ApprovalRequestID, call.Output, callError})
}

func decodeMCPCall(raw json.RawMessage) (generation.Item, error) {
	var value mcpCall
	if json.Unmarshal(raw, &value) != nil || value.Arguments == nil {
		return nil, failure(generation.ProtocolError)
	}
	call := generation.OpenAIMCPCall{ID: value.ID, Name: value.Name, ServerLabel: value.ServerLabel, Arguments: *value.Arguments, Status: value.Status, ApprovalRequestID: value.ApprovalRequestID, Output: value.Output}
	if validateMCPCall(call) != nil {
		return nil, failure(generation.ProtocolError)
	}
	var err error
	call.Error, err = decodeMCPError(value.Error)
	if err != nil {
		return nil, err
	}
	return call, nil
}

func encodeMCPApprovalRequest(request generation.OpenAIMCPApprovalRequest) (json.RawMessage, error) {
	if err := mcpIdentity(request.ID, request.ServerLabel); err != nil {
		return nil, err
	}
	if err := requiredString("name", request.Name); err != nil {
		return nil, err
	}
	if !utf8.ValidString(request.Arguments) {
		return nil, invalid("arguments", "must be valid UTF-8")
	}
	return json.Marshal(mcpApprovalRequest{"mcp_approval_request", request.ID, request.Name, request.ServerLabel, &request.Arguments})
}

func decodeMCPApprovalRequest(raw json.RawMessage) (generation.Item, error) {
	var value mcpApprovalRequest
	if json.Unmarshal(raw, &value) != nil || value.Arguments == nil || mcpIdentity(value.ID, value.ServerLabel) != nil || requiredString("name", value.Name) != nil {
		return nil, failure(generation.ProtocolError)
	}
	return generation.OpenAIMCPApprovalRequest{ID: value.ID, Name: value.Name, ServerLabel: value.ServerLabel, Arguments: *value.Arguments}, nil
}

func encodeMCPApprovalResponse(response generation.OpenAIMCPApprovalResponse) (json.RawMessage, error) {
	if err := requiredString("approval_request_id", response.ApprovalRequestID); err != nil {
		return nil, err
	}
	if id, ok := response.ID.Value(); ok {
		if err := requiredString("id", id); err != nil {
			return nil, err
		}
	}
	if err := mcpString("reason", response.Reason, true); err != nil {
		return nil, err
	}
	return json.Marshal(mcpApprovalResponse{"mcp_approval_response", response.ID, response.ApprovalRequestID, &response.Approve, response.Reason})
}

func decodeMCPApprovalResponse(raw json.RawMessage) (generation.Item, error) {
	var value mcpApprovalResponse
	if json.Unmarshal(raw, &value) != nil || value.Approve == nil || requiredString("approval_request_id", value.ApprovalRequestID) != nil {
		return nil, failure(generation.ProtocolError)
	}
	id, ok := value.ID.Value()
	if !ok || requiredString("id", id) != nil {
		return nil, failure(generation.ProtocolError)
	}
	return generation.OpenAIMCPApprovalResponse{ID: value.ID, ApprovalRequestID: value.ApprovalRequestID, Approve: *value.Approve, Reason: value.Reason}, nil
}

func mcpJSON(field string, raw json.RawMessage) error {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return invalid(field, "expected a JSON value")
	}
	return nil
}

func encodeMCPError(optional generation.Optional[generation.MCPCallError]) (json.RawMessage, error) {
	if optional.IsZero() {
		return nil, nil
	}
	if optional.IsNull() {
		return json.RawMessage(`null`), nil
	}
	value, _ := optional.Value()
	var kind, message string
	var code int64
	switch value := value.(type) {
	case generation.MCPProtocolError:
		kind, code, message = "mcp_protocol_error", value.Code, value.Message
	case generation.MCPHTTPError:
		kind, code, message = "http_error", value.Code, value.Message
	case generation.MCPExecutionError:
		if err := mcpJSON("content", value.Content); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}{"mcp_tool_execution_error", value.Content})
	default:
		return nil, invalid("", "expected a typed MCP error")
	}
	if !utf8.ValidString(message) {
		return nil, invalid("message", "must be valid UTF-8")
	}
	return json.Marshal(struct {
		Type    string `json:"type"`
		Code    int64  `json:"code"`
		Message string `json:"message"`
	}{kind, code, message})
}

func decodeMCPError(raw json.RawMessage) (generation.Optional[generation.MCPCallError], error) {
	var result generation.Optional[generation.MCPCallError]
	if len(raw) == 0 {
		return result, nil
	}
	if absent(raw) {
		return generation.Null[generation.MCPCallError](), nil
	}
	var value struct {
		Type    string          `json:"type"`
		Code    *int64          `json:"code"`
		Message *string         `json:"message"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Type == "" {
		return result, failure(generation.ProtocolError)
	}
	switch value.Type {
	case "mcp_protocol_error", "http_error":
		if value.Code == nil || value.Message == nil {
			return result, failure(generation.ProtocolError)
		}
		if value.Type == "mcp_protocol_error" {
			return generation.Some[generation.MCPCallError](generation.MCPProtocolError{Code: *value.Code, Message: *value.Message}), nil
		}
		return generation.Some[generation.MCPCallError](generation.MCPHTTPError{Code: *value.Code, Message: *value.Message}), nil
	case "mcp_tool_execution_error":
		if len(value.Content) == 0 {
			return result, failure(generation.ProtocolError)
		}
		return generation.Some[generation.MCPCallError](generation.MCPExecutionError{Content: value.Content}), nil
	default:
		return result, failure(generation.Unsupported)
	}
}
