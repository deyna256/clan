package generation

// CustomTool accepts freeform text, optionally constrained by a grammar.
type CustomTool struct {
	Name        string
	Description Optional[string]
	Format      CustomInputFormat
	OpenAI      OpenAICustomToolOptions
}

type OpenAICustomToolOptions struct {
	Async          Optional[bool]
	DeferLoading   Optional[bool]
	AllowedCallers Optional[[]string]
}

// A nil format leaves the provider's default text format unchanged.
type CustomInputFormat interface{ isCustomInputFormat() }
type CustomTextFormat struct{}
type CustomGrammarFormat struct{ Syntax, Definition string }

type NamedCustomTool struct{ Name string }

type CustomToolCall struct {
	ID, CallID, Name string
	Input            string
	Status           ItemStatus
	OpenAI           OpenAIToolCallData
}

type CustomToolResult struct {
	CreatedBy  Optional[string]
	ID, CallID string
	Output     ToolOutput
	Status     ItemStatus
	Caller     Optional[OpenAIToolCaller]
}

type ToolInputDelta struct {
	ItemIndex int
	Fragment  string
}

func (CustomTool) isTool()                       {}
func (CustomTextFormat) isCustomInputFormat()    {}
func (CustomGrammarFormat) isCustomInputFormat() {}
func (NamedCustomTool) isToolChoice()            {}
func (CustomToolCall) isItem()                   {}
func (CustomToolResult) isItem()                 {}
func (ToolInputDelta) isEvent()                  {}
