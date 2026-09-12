package generation

import "encoding/json"

type OpenAIToolSearchTool struct {
	Description Optional[string]
	Parameters  json.RawMessage
	Execution   Optional[string]
}

// Search arguments are a JSON value, not a string of argument fragments.
type OpenAIToolSearchCall struct {
	ID, CallID Optional[string]
	Execution  Optional[string]
	Status     Optional[ItemStatus]
	Arguments  json.RawMessage
	CreatedBy  Optional[string]
}

type OpenAIToolSearchOutput struct {
	ID, CallID Optional[string]
	Execution  Optional[string]
	Status     Optional[ItemStatus]
	Tools      []Tool
	CreatedBy  Optional[string]
}

type OpenAIAdditionalTools struct {
	ID    Optional[string]
	Role  string
	Tools []Tool
}

func (OpenAIToolSearchTool) isTool()   {}
func (OpenAIToolSearchCall) isItem()   {}
func (OpenAIToolSearchOutput) isItem() {}
func (OpenAIAdditionalTools) isItem()  {}
