package wire

import (
	"bytes"
	"encoding/json"

	"github.com/deyna256/clan/internal/generation"
)

func decodeLoadedMCP(raw json.RawMessage) (generation.Tool, error) {
	var value struct {
		mcpTool
		ServerURL   generation.Optional[string]             `json:"server_url"`
		ConnectorID generation.Optional[string]             `json:"connector_id"`
		TunnelID    generation.Optional[string]             `json:"tunnel_id"`
		Headers     generation.Optional[map[string]*string] `json:"headers"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	tool := generation.OpenAIMCPTool{ServerLabel: value.ServerLabel, Authorization: value.Authorization, ServerDescription: value.ServerDescription, AllowedCallers: value.AllowedCallers, DeferLoading: value.DeferLoading}
	endpoints := 0
	for _, endpoint := range []generation.Optional[string]{value.ServerURL, value.ConnectorID, value.TunnelID} {
		if endpoint.IsNull() {
			return nil, failure(generation.ProtocolError)
		}
		if !endpoint.IsZero() {
			endpoints++
		}
	}
	if endpoints != 1 {
		return nil, failure(generation.ProtocolError)
	}
	if url, ok := value.ServerURL.Value(); ok {
		tool.Endpoint = generation.MCPServerURL(url)
	}
	if connector, ok := value.ConnectorID.Value(); ok {
		tool.Endpoint = generation.MCPConnectorID(connector)
	}
	if tunnel, ok := value.TunnelID.Value(); ok {
		tool.Endpoint = generation.MCPTunnelID(tunnel)
	}
	if value.Headers.IsNull() {
		tool.Headers = generation.Null[map[string]string]()
	} else if headers, ok := value.Headers.Value(); ok {
		decoded := make(map[string]string, len(headers))
		for name, header := range headers {
			if header == nil {
				return nil, failure(generation.ProtocolError)
			}
			decoded[name] = *header
		}
		tool.Headers = generation.Some(decoded)
	}
	var err error
	tool.AllowedTools, err = decodeLoadedMCPAllowedTools(value.AllowedTools)
	if err != nil {
		return nil, err
	}
	tool.RequireApproval, err = decodeLoadedMCPApproval(value.RequireApproval)
	if err != nil {
		return nil, err
	}
	if _, err := encodeMCPTool(tool); err != nil {
		return nil, failure(generation.ProtocolError)
	}
	return tool, nil
}

func decodeLoadedMCPAllowedTools(raw json.RawMessage) (generation.Optional[generation.MCPAllowedTools], error) {
	if len(raw) == 0 {
		return generation.Optional[generation.MCPAllowedTools]{}, nil
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return generation.Null[generation.MCPAllowedTools](), nil
	}
	if raw[0] == '[' {
		var names strictStrings
		if json.Unmarshal(raw, &names) != nil {
			return generation.Optional[generation.MCPAllowedTools]{}, failure(generation.ProtocolError)
		}
		return generation.Some[generation.MCPAllowedTools](generation.MCPToolNames(names)), nil
	}
	filter, err := decodeLoadedMCPFilter(raw)
	if err != nil {
		return generation.Optional[generation.MCPAllowedTools]{}, err
	}
	return generation.Some[generation.MCPAllowedTools](filter), nil
}

func decodeLoadedMCPFilter(raw json.RawMessage) (generation.MCPToolFilter, error) {
	var value struct {
		ReadOnly  generation.Optional[bool] `json:"read_only"`
		ToolNames strictStrings             `json:"tool_names"`
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return generation.MCPToolFilter{}, failure(generation.ProtocolError)
	}
	return generation.MCPToolFilter{ReadOnly: value.ReadOnly, ToolNames: []string(value.ToolNames)}, nil
}

func decodeLoadedMCPApproval(raw json.RawMessage) (generation.Optional[generation.MCPApprovalPolicy], error) {
	if len(raw) == 0 {
		return generation.Optional[generation.MCPApprovalPolicy]{}, nil
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return generation.Null[generation.MCPApprovalPolicy](), nil
	}
	if raw[0] == '"' {
		var mode string
		if json.Unmarshal(raw, &mode) != nil {
			return generation.Optional[generation.MCPApprovalPolicy]{}, failure(generation.ProtocolError)
		}
		return generation.Some[generation.MCPApprovalPolicy](generation.MCPApprovalMode(mode)), nil
	}
	var value struct {
		Always json.RawMessage `json:"always"`
		Never  json.RawMessage `json:"never"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return generation.Optional[generation.MCPApprovalPolicy]{}, failure(generation.ProtocolError)
	}
	var policy generation.MCPApprovalFilter
	if len(value.Always) != 0 {
		filter, err := decodeLoadedMCPFilter(value.Always)
		if err != nil {
			return generation.Optional[generation.MCPApprovalPolicy]{}, err
		}
		policy.Always = generation.Some(filter)
	}
	if len(value.Never) != 0 {
		filter, err := decodeLoadedMCPFilter(value.Never)
		if err != nil {
			return generation.Optional[generation.MCPApprovalPolicy]{}, err
		}
		policy.Never = generation.Some(filter)
	}
	return generation.Some[generation.MCPApprovalPolicy](policy), nil
}
