package generation

// Stored content variants retain resource representations that cannot always be
// replayed as generation inputs, such as images whose URLs were not included.
type OpenAIStoredText struct{ Text string }

type OpenAIStoredImage struct {
	Kind, Detail          string
	FileID, ImageURL      Optional[string]
	PromptCacheBreakpoint bool
}

type OpenAIStoredFile struct {
	FileID, FileData, FileURL, Filename, Detail Optional[string]
	PromptCacheBreakpoint                       bool
}

func (OpenAIStoredText) isPart()  {}
func (OpenAIStoredImage) isPart() {}
func (OpenAIStoredFile) isPart()  {}
