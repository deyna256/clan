package generation

type OpenAICodeInterpreterTool struct {
	Container      InterpreterContainer
	AllowedCallers Optional[[]string]
}
type InterpreterContainer interface{ isInterpreterContainer() }
type InterpreterContainerID string
type InterpreterAutoContainer struct {
	FileIDs       []string
	MemoryLimit   Optional[string]
	NetworkPolicy InterpreterNetworkPolicy
}
type InterpreterNetworkPolicy interface{ isInterpreterNetworkPolicy() }
type InterpreterNetworkDisabled struct{}
type InterpreterNetworkAllowlist struct {
	Domains []string
	Secrets []InterpreterDomainSecret
}
type InterpreterDomainSecret struct{ Domain, Name, Value string }

func (OpenAICodeInterpreterTool) isTool()                       {}
func (InterpreterContainerID) isInterpreterContainer()          {}
func (InterpreterAutoContainer) isInterpreterContainer()        {}
func (InterpreterNetworkDisabled) isInterpreterNetworkPolicy()  {}
func (InterpreterNetworkAllowlist) isInterpreterNetworkPolicy() {}

type OpenAICodeInterpreterChoice struct{}

func (OpenAICodeInterpreterChoice) isToolChoice() {}

type OpenAICodeInterpreterCall struct {
	ID, ContainerID, Status string
	Code                    Optional[string]
	Outputs                 Optional[[]InterpreterOutput]
}
type InterpreterOutput interface{ isInterpreterOutput() }
type InterpreterLogs struct{ Logs string }
type InterpreterImage struct{ URL string }

func (OpenAICodeInterpreterCall) isItem()     {}
func (InterpreterLogs) isInterpreterOutput()  {}
func (InterpreterImage) isInterpreterOutput() {}
