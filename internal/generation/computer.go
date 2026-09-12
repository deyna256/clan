package generation

type OpenAIComputerTool struct{}
type OpenAIComputerPreviewTool struct {
	DisplayWidth, DisplayHeight int64
	Environment                 string
}
type OpenAIComputerChoice string

// ComputerAction describes a client operation; CLAN does not execute it.
type ComputerAction interface{ isComputerAction() }
type ComputerPoint struct{ X, Y int64 }
type ComputerClick struct {
	X, Y   int64
	Button string
	Keys   Optional[[]string]
}
type ComputerDoubleClick struct {
	X, Y int64
	Keys Optional[[]string]
}
type ComputerDrag struct {
	Path []ComputerPoint
	Keys Optional[[]string]
}
type ComputerKeypress struct{ Keys []string }
type ComputerMove struct {
	X, Y int64
	Keys Optional[[]string]
}
type ComputerScreenshot struct{}
type ComputerScroll struct {
	X, Y, ScrollX, ScrollY int64
	Keys                   Optional[[]string]
}
type ComputerType struct{ Text string }
type ComputerWait struct{}

type OpenAIComputerCall struct {
	ID, CallID, Status  string
	Action              ComputerAction
	Actions             []ComputerAction
	PendingSafetyChecks []ComputerSafetyCheck
}
type ComputerSafetyCheck struct {
	ID            string
	Code, Message Optional[string]
}
type ComputerScreenshotOutput struct {
	FileID, ImageURL, Detail Optional[string]
}
type OpenAIComputerResult struct {
	ID, Status               Optional[string]
	CallID                   string
	Output                   ComputerScreenshotOutput
	AcknowledgedSafetyChecks Optional[[]ComputerSafetyCheck]
	CreatedBy                Optional[string]
}

func (OpenAIComputerTool) isTool()            {}
func (OpenAIComputerPreviewTool) isTool()     {}
func (OpenAIComputerChoice) isToolChoice()    {}
func (OpenAIComputerCall) isItem()            {}
func (OpenAIComputerResult) isItem()          {}
func (ComputerClick) isComputerAction()       {}
func (ComputerDoubleClick) isComputerAction() {}
func (ComputerDrag) isComputerAction()        {}
func (ComputerKeypress) isComputerAction()    {}
func (ComputerMove) isComputerAction()        {}
func (ComputerScreenshot) isComputerAction()  {}
func (ComputerScroll) isComputerAction()      {}
func (ComputerType) isComputerAction()        {}
func (ComputerWait) isComputerAction()        {}
