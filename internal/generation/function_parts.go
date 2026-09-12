package generation

// Function-result content has nullable fields that ordinary message content
// does not. These parts are accepted only within ToolResult.Output.
type OpenAIFunctionText struct {
	Text                  string
	PromptCacheBreakpoint Optional[OpenAIPromptCacheBreakpoint]
}

type OpenAIFunctionImage struct {
	Detail, FileID, ImageURL Optional[string]
	PromptCacheBreakpoint    Optional[OpenAIPromptCacheBreakpoint]
}

type OpenAIFunctionFile struct {
	Detail, FileID, FileData, FileURL, Filename Optional[string]
	PromptCacheBreakpoint                       Optional[OpenAIPromptCacheBreakpoint]
}

type OpenAIPromptCacheBreakpoint struct{ Mode string }

func (OpenAIFunctionText) isPart()  {}
func (OpenAIFunctionImage) isPart() {}
func (OpenAIFunctionFile) isPart()  {}
