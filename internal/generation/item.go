package generation

// Item variants describe ordered messages, reasoning and tool exchanges.
// Pass concrete values, not pointers. Nested slices remain caller-owned.
type Item interface{ isItem() }

type Role string

const (
	System    Role = "system"
	Developer Role = "developer"
	User      Role = "user"
	Assistant Role = "assistant"
)

type ItemStatus string

const (
	ItemInProgress ItemStatus = "in_progress"
	ItemCompleted  ItemStatus = "completed"
	ItemIncomplete ItemStatus = "incomplete"
)

type Message struct {
	ID     string
	Role   Role
	Parts  []Part
	Status ItemStatus
	OpenAI OpenAIMessageData
}

type OpenAIMessageData struct{ Phase Optional[string] }

type Reasoning struct {
	ID     string
	Parts  []Part
	Status ItemStatus
	OpenAI OpenAIReasoningData
}

// EncryptedContent is opaque and must only be replayed to a compatible origin.
type OpenAIReasoningData struct{ EncryptedContent Optional[string] }

type ToolCall struct {
	ID, CallID, Name string
	Arguments        string // May be incomplete when generation stops at a limit.
	Status           ItemStatus
	OpenAI           OpenAIToolCallData
}

type OpenAIToolCallData struct {
	CreatedBy Optional[string]
	Async     Optional[bool]
	Namespace Optional[string]
	Caller    Optional[OpenAIToolCaller]
}

// OpenAIToolCaller identifies a direct call or the program call that produced it.
type OpenAIToolCaller struct{ Type, CallerID string }

type ToolResult struct {
	ID, CallID Optional[string]
	Output     ToolOutput
	Status     Optional[ItemStatus]
	Caller     Optional[OpenAIToolCaller]
	OpenAI     OpenAIToolResultData
}

// CreatedBy is returned metadata and is omitted when replaying the result.
type OpenAIToolResultData struct {
	Name, Namespace, CreatedBy Optional[string]
}

func (Message) isItem()    {}
func (Reasoning) isItem()  {}
func (ToolCall) isItem()   {}
func (ToolResult) isItem() {}

// Part variants keep text, refusal and reasoning separate from image sources.
type Part interface{ isPart() }

type Text struct {
	Text   string
	OpenAI OpenAITextData
}
type Refusal struct{ Text string }
type ReasoningText struct{ Text string }
type ReasoningSummary struct{ Text string }

type ImageURL struct {
	URL    string // HTTP(S) or a data URL; the adapter does not fetch it.
	Detail string
	OpenAI OpenAIImageOptions
}

type ImageFile struct {
	FileID string
	Detail string
	OpenAI OpenAIImageOptions
}

type OpenAIImageOptions struct{ PromptCacheBreakpoint bool }

type FileID struct {
	ID      string
	Options FileOptions
}
type FileURL struct {
	URL     string
	Options FileOptions
}
type FileData struct {
	Data    string
	Options FileOptions
}
type FileOptions struct {
	Filename Optional[string]
	Detail   string
	OpenAI   OpenAIFileOptions
}
type OpenAIFileOptions struct{ PromptCacheBreakpoint bool }

func (Text) isPart()             {}
func (Refusal) isPart()          {}
func (ReasoningText) isPart()    {}
func (ReasoningSummary) isPart() {}
func (ImageURL) isPart()         {}
func (ImageFile) isPart()        {}
func (FileID) isPart()           {}
func (FileURL) isPart()          {}
func (FileData) isPart()         {}

type ToolOutput interface{ isToolOutput() }
type ToolTextOutput string
type ToolPartsOutput []Part

func (ToolTextOutput) isToolOutput()  {}
func (ToolPartsOutput) isToolOutput() {}
