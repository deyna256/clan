package generation

type OpenAIApplyPatchTool struct{ AllowedCallers Optional[[]string] }
type OpenAIApplyPatchChoice struct{}

type OpenAIApplyPatchCall struct {
	CreatedBy  Optional[string]
	ID, CallID string
	Status     string
	Operation  PatchOperation
	Caller     Optional[OpenAIToolCaller]
}

// PatchOperation describes work for the client; CLAN does not apply it.
type PatchOperation interface{ isPatchOperation() }
type PatchCreateFile struct{ Path, Diff string }
type PatchUpdateFile struct{ Path, Diff string }
type PatchDeleteFile struct{ Path string }

type OpenAIApplyPatchResult struct {
	CreatedBy  Optional[string]
	ID, CallID string
	Status     string
	Output     Optional[string]
	Caller     Optional[OpenAIToolCaller]
}

type PatchDiffDelta struct {
	ItemIndex int
	Fragment  string
}

func (OpenAIApplyPatchTool) isTool()         {}
func (OpenAIApplyPatchChoice) isToolChoice() {}
func (OpenAIApplyPatchCall) isItem()         {}
func (OpenAIApplyPatchResult) isItem()       {}
func (PatchCreateFile) isPatchOperation()    {}
func (PatchUpdateFile) isPatchOperation()    {}
func (PatchDeleteFile) isPatchOperation()    {}
func (PatchDiffDelta) isEvent()              {}
