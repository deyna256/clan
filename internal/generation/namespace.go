package generation

import "encoding/json"

// OpenAINamespaceTool groups function and custom declarations under a shared name.
// The provider adapter validates the permitted child tool types.
type OpenAINamespaceTool struct {
	Name, Description string
	Tools             []Tool
}

type OpenAIFunctionToolOptions struct {
	Async          Optional[bool]
	DeferLoading   Optional[bool]
	AllowedCallers Optional[[]string]
	OutputSchema   json.RawMessage
}

func (OpenAINamespaceTool) isTool() {}
