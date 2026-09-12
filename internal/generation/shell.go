package generation

type OpenAIShellTool struct {
	AllowedCallers Optional[[]string]
	Environment    Optional[ShellEnvironment]
}
type OpenAIShellChoice struct{}
type OpenAILocalShellTool struct{}
type OpenAILocalShellChoice struct{}

type ShellEnvironment interface{ isShellEnvironment() }
type ShellLocalEnvironment struct{ Skills []ShellLocalSkill }
type ShellLocalSkill struct{ Name, Description, Path string }
type ShellContainerReference struct{ ContainerID string }
type ShellAutoContainer struct {
	FileIDs       []string
	MemoryLimit   Optional[string]
	NetworkPolicy InterpreterNetworkPolicy
	Skills        []ShellSkill
}
type ShellSkill interface{ isShellSkill() }
type ShellSkillReference struct {
	SkillID string
	Version Optional[string]
}
type ShellInlineSkill struct{ Name, Description, Data string }

// ShellAction contains commands for the selected runtime; CLAN does not execute them.
type ShellAction struct {
	Commands                   []string
	TimeoutMs, MaxOutputLength Optional[int64]
}
type OpenAIShellCall struct {
	ID          Optional[string]
	CallID      string
	Status      Optional[string]
	Action      ShellAction
	Environment Optional[ShellEnvironment]
	Caller      Optional[OpenAIToolCaller]
	CreatedBy   Optional[string]
}
type OpenAIShellResult struct {
	ID              Optional[string]
	CallID          string
	Status          Optional[string]
	MaxOutputLength Optional[int64]
	Output          []ShellOutput
	Caller          Optional[OpenAIToolCaller]
	CreatedBy       Optional[string]
}
type ShellOutput struct {
	Stdout, Stderr string
	Outcome        ShellOutcome
	CreatedBy      Optional[string]
}
type ShellOutcome interface{ isShellOutcome() }
type ShellExit struct{ ExitCode int64 }
type ShellTimeout struct{}

type OpenAILocalShellCall struct {
	ID, CallID, Status string
	Action             LocalShellAction
}
type LocalShellAction struct {
	Command                []string
	Env                    map[string]string
	TimeoutMs              Optional[int64]
	User, WorkingDirectory Optional[string]
}

// OpenAILocalShellResult.ID references the call's CallID, not its item ID.
type OpenAILocalShellResult struct {
	ID, Output string
	Status     Optional[string]
}

func (OpenAIShellTool) isTool()                     {}
func (OpenAILocalShellTool) isTool()                {}
func (OpenAIShellChoice) isToolChoice()             {}
func (OpenAILocalShellChoice) isToolChoice()        {}
func (ShellLocalEnvironment) isShellEnvironment()   {}
func (ShellContainerReference) isShellEnvironment() {}
func (ShellAutoContainer) isShellEnvironment()      {}
func (ShellSkillReference) isShellSkill()           {}
func (ShellInlineSkill) isShellSkill()              {}
func (ShellExit) isShellOutcome()                   {}
func (ShellTimeout) isShellOutcome()                {}
func (OpenAIShellCall) isItem()                     {}
func (OpenAIShellResult) isItem()                   {}
func (OpenAILocalShellCall) isItem()                {}
func (OpenAILocalShellResult) isItem()              {}
