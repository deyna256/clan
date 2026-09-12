package generation

// OpenAIAllowedToolsChoice limits selection to the listed individual tools.
// Mode is ToolAuto or ToolRequired; Tools contains object choices, not modes
// or another allowed-tools selection.
type OpenAIAllowedToolsChoice struct {
	Mode  ToolMode
	Tools []ToolChoice
}

func (OpenAIAllowedToolsChoice) isToolChoice() {}
