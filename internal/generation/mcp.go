package generation

import "encoding/json"

// OpenAIMCPTool configures provider-hosted MCP access; CLAN does not contact the server.
type OpenAIMCPTool struct {
	ServerLabel                      string
	Endpoint                         MCPEndpoint
	Authorization, ServerDescription Optional[string]
	Headers                          Optional[map[string]string]
	AllowedCallers                   Optional[[]string]
	AllowedTools                     Optional[MCPAllowedTools]
	RequireApproval                  Optional[MCPApprovalPolicy]
	DeferLoading                     Optional[bool]
}
type MCPEndpoint interface{ isMCPEndpoint() }
type MCPServerURL string
type MCPConnectorID string
type MCPTunnelID string

type MCPAllowedTools interface{ isMCPAllowedTools() }
type MCPToolNames []string
type MCPToolFilter struct {
	ReadOnly  Optional[bool]
	ToolNames []string
}
type MCPApprovalPolicy interface{ isMCPApprovalPolicy() }
type MCPApprovalMode string
type MCPApprovalFilter struct{ Always, Never Optional[MCPToolFilter] }
type OpenAIMCPChoice struct {
	ServerLabel string
	Name        Optional[string]
}

type OpenAIMCPListTools struct {
	ID, ServerLabel string
	Tools           []MCPListedTool
	Error           Optional[string]
}
type MCPListedTool struct {
	Name        string
	InputSchema json.RawMessage
	Annotations Optional[json.RawMessage]
	Description Optional[string]
}
type OpenAIMCPCall struct {
	ID, Name, ServerLabel, Arguments string
	Status                           Optional[string]
	ApprovalRequestID, Output        Optional[string]
	Error                            Optional[MCPCallError]
}
type MCPCallError interface{ isMCPCallError() }
type MCPProtocolError struct {
	Code    int64
	Message string
}
type MCPExecutionError struct{ Content json.RawMessage }
type MCPHTTPError struct {
	Code    int64
	Message string
}
type OpenAIMCPApprovalRequest struct{ ID, Name, ServerLabel, Arguments string }
type OpenAIMCPApprovalResponse struct {
	ID                Optional[string]
	ApprovalRequestID string
	Approve           bool
	Reason            Optional[string]
}

func (OpenAIMCPTool) isTool()                  {}
func (OpenAIMCPChoice) isToolChoice()          {}
func (MCPServerURL) isMCPEndpoint()            {}
func (MCPConnectorID) isMCPEndpoint()          {}
func (MCPTunnelID) isMCPEndpoint()             {}
func (MCPToolNames) isMCPAllowedTools()        {}
func (MCPToolFilter) isMCPAllowedTools()       {}
func (MCPApprovalMode) isMCPApprovalPolicy()   {}
func (MCPApprovalFilter) isMCPApprovalPolicy() {}
func (MCPProtocolError) isMCPCallError()       {}
func (MCPExecutionError) isMCPCallError()      {}
func (MCPHTTPError) isMCPCallError()           {}
func (OpenAIMCPListTools) isItem()             {}
func (OpenAIMCPCall) isItem()                  {}
func (OpenAIMCPApprovalRequest) isItem()       {}
func (OpenAIMCPApprovalResponse) isItem()      {}
