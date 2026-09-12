package generation

type OpenAIProgrammaticToolCallingTool struct{}
type OpenAIProgrammaticToolCallingChoice struct{}

// OpenAIProgram contains JavaScript executed by OpenAI's hosted runtime.
// Fingerprint is opaque replay state and must be returned unchanged.
type OpenAIProgram struct {
	ID, CallID, Code string
	Fingerprint      Optional[string]
}

type OpenAIProgramOutput struct {
	ID, CallID, Result string
	Status             ItemStatus
}

func (OpenAIProgrammaticToolCallingTool) isTool()         {}
func (OpenAIProgrammaticToolCallingChoice) isToolChoice() {}
func (OpenAIProgram) isItem()                             {}
func (OpenAIProgramOutput) isItem()                       {}
