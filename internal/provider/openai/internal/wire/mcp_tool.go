package wire

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type mcpTool struct {
	Type              string                                 `json:"type"`
	ServerLabel       string                                 `json:"server_label"`
	ServerURL         string                                 `json:"server_url,omitempty"`
	ConnectorID       string                                 `json:"connector_id,omitempty"`
	TunnelID          string                                 `json:"tunnel_id,omitempty"`
	Authorization     generation.Optional[string]            `json:"authorization,omitzero"`
	ServerDescription generation.Optional[string]            `json:"server_description,omitzero"`
	Headers           generation.Optional[map[string]string] `json:"headers,omitzero"`
	AllowedCallers    generation.Optional[[]string]          `json:"allowed_callers,omitzero"`
	AllowedTools      json.RawMessage                        `json:"allowed_tools,omitempty"`
	RequireApproval   json.RawMessage                        `json:"require_approval,omitempty"`
	DeferLoading      generation.Optional[bool]              `json:"defer_loading,omitzero"`
}

func encodeMCPTool(tool generation.OpenAIMCPTool) (json.RawMessage, error) {
	if err := requiredString("server_label", tool.ServerLabel); err != nil {
		return nil, err
	}
	value := mcpTool{Type: "mcp", ServerLabel: tool.ServerLabel, Authorization: tool.Authorization, ServerDescription: tool.ServerDescription, Headers: tool.Headers, AllowedCallers: tool.AllowedCallers, DeferLoading: tool.DeferLoading}
	switch endpoint := tool.Endpoint.(type) {
	case generation.MCPServerURL:
		if !httpURL(string(endpoint)) {
			return nil, invalid("server_url", "expected an HTTP(S) URL")
		}
		value.ServerURL = string(endpoint)
	case generation.MCPConnectorID:
		switch endpoint {
		case "connector_dropbox", "connector_gmail", "connector_googlecalendar", "connector_googledrive", "connector_microsoftteams", "connector_outlookcalendar", "connector_outlookemail", "connector_sharepoint":
		default:
			return nil, invalid("connector_id", "unsupported connector")
		}
		value.ConnectorID = string(endpoint)
	case generation.MCPTunnelID:
		if err := requiredString("tunnel_id", string(endpoint)); err != nil {
			return nil, err
		}
		value.TunnelID = string(endpoint)
	default:
		return nil, invalid("endpoint", "expected a server URL, connector ID, or tunnel ID")
	}
	if err := mcpString("authorization", tool.Authorization, false); err != nil {
		return nil, err
	}
	if err := mcpString("server_description", tool.ServerDescription, false); err != nil {
		return nil, err
	}
	if tool.DeferLoading.IsNull() {
		return nil, invalid("defer_loading", "must not be null")
	}
	if callers, ok := tool.AllowedCallers.Value(); ok {
		if err := allowedCallers(callers); err != nil {
			return nil, err
		}
		if callers == nil {
			value.AllowedCallers = generation.Some([]string{})
		}
	}
	if headers, ok := tool.Headers.Value(); ok {
		if headers == nil {
			value.Headers = generation.Some(map[string]string{})
		}
		for name, header := range headers {
			if !mcpHeader(name, header) {
				return nil, invalid("headers", "invalid HTTP header")
			}
		}
	}
	var err error
	value.AllowedTools, err = encodeMCPAllowedTools(tool.AllowedTools)
	if err != nil {
		return nil, at("allowed_tools", err)
	}
	value.RequireApproval, err = encodeMCPApprovalPolicy(tool.RequireApproval)
	if err != nil {
		return nil, at("require_approval", err)
	}
	return json.Marshal(value)
}

func mcpHeader(name, value string) bool {
	if name == "" || !utf8.ValidString(value) {
		return false
	}
	for _, c := range name {
		if c <= 32 || c >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", c) {
			return false
		}
	}
	for _, c := range value {
		if c == '\r' || c == '\n' || c == 127 || (c < 32 && c != '\t') {
			return false
		}
	}
	return true
}

func encodeMCPAllowedTools(optional generation.Optional[generation.MCPAllowedTools]) (json.RawMessage, error) {
	if optional.IsZero() {
		return nil, nil
	}
	if optional.IsNull() {
		return json.RawMessage(`null`), nil
	}
	value, _ := optional.Value()
	switch value := value.(type) {
	case generation.MCPToolNames:
		if err := mcpToolNames(value); err != nil {
			return nil, err
		}
		if value == nil {
			value = generation.MCPToolNames{}
		}
		return json.Marshal(value)
	case generation.MCPToolFilter:
		return encodeMCPToolFilter(value)
	default:
		return nil, invalid("", "expected tool names or a tool filter")
	}
}

func encodeMCPToolFilter(filter generation.MCPToolFilter) (json.RawMessage, error) {
	if filter.ReadOnly.IsNull() {
		return nil, invalid("read_only", "must not be null")
	}
	if err := mcpToolNames(filter.ToolNames); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ReadOnly  generation.Optional[bool] `json:"read_only,omitzero"`
		ToolNames []string                  `json:"tool_names,omitzero"`
	}{filter.ReadOnly, filter.ToolNames})
}

func mcpToolNames(names []string) error {
	for _, name := range names {
		if err := requiredString("tool_names", name); err != nil {
			return err
		}
	}
	return nil
}

func encodeMCPApprovalPolicy(optional generation.Optional[generation.MCPApprovalPolicy]) (json.RawMessage, error) {
	if optional.IsZero() {
		return nil, nil
	}
	if optional.IsNull() {
		return json.RawMessage(`null`), nil
	}
	value, _ := optional.Value()
	switch value := value.(type) {
	case generation.MCPApprovalMode:
		if value != "always" && value != "never" {
			return nil, invalid("", "expected always or never")
		}
		return json.Marshal(string(value))
	case generation.MCPApprovalFilter:
		var filter struct {
			Always json.RawMessage `json:"always,omitempty"`
			Never  json.RawMessage `json:"never,omitempty"`
		}
		if value.Always.IsNull() || value.Never.IsNull() {
			return nil, invalid("", "approval filters must not be null")
		}
		if always, ok := value.Always.Value(); ok {
			encoded, err := encodeMCPToolFilter(always)
			if err != nil {
				return nil, at("always", err)
			}
			filter.Always = encoded
		}
		if never, ok := value.Never.Value(); ok {
			encoded, err := encodeMCPToolFilter(never)
			if err != nil {
				return nil, at("never", err)
			}
			filter.Never = encoded
		}
		return json.Marshal(filter)
	default:
		return nil, invalid("", "expected an approval policy")
	}
}

func encodeMCPChoice(choice generation.OpenAIMCPChoice) (json.RawMessage, error) {
	if err := requiredString("server_label", choice.ServerLabel); err != nil {
		return nil, err
	}
	if name, ok := choice.Name.Value(); ok {
		if err := requiredString("name", name); err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		Type        string                      `json:"type"`
		ServerLabel string                      `json:"server_label"`
		Name        generation.Optional[string] `json:"name,omitzero"`
	}{"mcp", choice.ServerLabel, choice.Name})
}

func mcpString(field string, value generation.Optional[string], nullable bool) error {
	if !nullable && value.IsNull() {
		return invalid(field, "must not be null")
	}
	if text, _ := value.Value(); !utf8.ValidString(text) {
		return invalid(field, "must be valid UTF-8")
	}
	return nil
}
