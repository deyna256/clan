package generation

type OpenAIPrompt struct {
	ID        string
	Version   Optional[string]
	Variables Optional[map[string]PromptVariable]
}

// PromptVariable is a string or an input text, image, or file part.
type PromptVariable interface{ isPromptVariable() }

type PromptString string

func (PromptString) isPromptVariable() {}
func (Text) isPromptVariable()         {}
func (ImageURL) isPromptVariable()     {}
func (ImageFile) isPromptVariable()    {}
func (FileID) isPromptVariable()       {}
func (FileURL) isPromptVariable()      {}
func (FileData) isPromptVariable()     {}

type OpenAIModerationOptions struct {
	Model  string
	Policy Optional[OpenAIModerationPolicy]
}

type OpenAIModerationPolicy struct {
	Input, Output Optional[OpenAIModerationRule]
}

type OpenAIModerationRule struct{ Mode string }

type OpenAIContextManagement struct {
	Type             string
	CompactThreshold Optional[int64]
}

type OpenAIPromptCacheOptions struct {
	ComparisonResponseID Optional[string]
	Mode, TTL            Optional[string]
}

type OpenAIStreamOptions struct {
	IncludeObfuscation Optional[bool]
}
