package generation

import "encoding/json"

// Request contains generation input, without credentials or transport mode.
// Adapters read it without mutation; callers must not change it during a call.
type Request struct {
	Model             string
	Input             []Item
	Instructions      Optional[string]
	MaxOutputTokens   Optional[int64]
	Temperature       Optional[float64]
	TopP              Optional[float64]
	ParallelToolCalls Optional[bool]
	Tools             []Tool
	ToolChoice        ToolChoice
	OutputFormat      OutputFormat
	OpenAI            OpenAIOptions
}

// OpenAIOptions describes supported Responses settings without SDK types.
type OpenAIOptions struct {
	Background           Optional[bool]
	Store                Optional[bool]
	PreviousResponseID   Optional[string]
	Include              Optional[[]string]
	Reasoning            Optional[ReasoningOptions]
	Verbosity            Optional[string]
	MaxToolCalls         Optional[int64]
	TopLogprobs          Optional[int64]
	ServiceTier          Optional[string]
	Truncation           Optional[string]
	PromptCacheKey       Optional[string]
	PromptCacheRetention Optional[string]
	SafetyIdentifier     Optional[string]
	Metadata             Optional[map[string]string]
	Conversation         Optional[string]
	Prompt               Optional[OpenAIPrompt]
	Moderation           Optional[OpenAIModerationOptions]
	ContextManagement    Optional[[]OpenAIContextManagement]
	PromptCacheOptions   Optional[OpenAIPromptCacheOptions]
	StreamOptions        Optional[OpenAIStreamOptions]
	User                 Optional[string]
}

type ReasoningOptions struct {
	Effort          Optional[string]
	Summary         Optional[string]
	Context         Optional[string]
	GenerateSummary Optional[string]
	Mode            Optional[string]
}

// Tool variants are passed by value. The gateway does not execute these tools.
type Tool interface{ isTool() }

type FunctionTool struct {
	Name        string
	Description Optional[string]
	Parameters  json.RawMessage
	Strict      Optional[bool]
	OpenAI      OpenAIFunctionToolOptions
}

func (FunctionTool) isTool() {}

// ToolChoice selects a mode or a specific tool; nil leaves it unspecified.
type ToolChoice interface{ isToolChoice() }

type ToolMode string

const (
	ToolAuto     ToolMode = "auto"
	ToolNone     ToolMode = "none"
	ToolRequired ToolMode = "required"
)

func (ToolMode) isToolChoice() {}

type NamedTool struct{ Name string }

func (NamedTool) isToolChoice() {}

// OutputFormat is a concrete format value; nil leaves the format unspecified.
type OutputFormat interface{ isOutputFormat() }

type TextFormat struct{}
type JSONObjectFormat struct{}
type JSONSchemaFormat struct {
	Name        string
	Description Optional[string]
	Schema      json.RawMessage
	Strict      Optional[bool]
}

func (TextFormat) isOutputFormat()       {}
func (JSONObjectFormat) isOutputFormat() {}
func (JSONSchemaFormat) isOutputFormat() {}
