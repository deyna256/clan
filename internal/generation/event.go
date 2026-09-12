package generation

import "github.com/deyna256/clan/internal/usage"

// Event variants describe one response's ordered item/part lifecycle.
// End events carry snapshots; their contents must not be appended again.
type Event interface{ isEvent() }

type ResponseStarted struct{ Identity Identity }
type ItemStarted struct {
	Index int
	Item  Item
}
type PartAddress struct{ Item, Part int }
type PartStarted struct {
	Address PartAddress
	Part    Part
}
type TextDelta struct {
	Address PartAddress
	Text    string
}
type ArgumentsDelta struct {
	ItemIndex int
	Fragment  string
}

// MCPProgress describes a hosted tool operation, not generation completion.
type MCPProgress struct {
	ItemIndex int
	Status    string
}

type CodeDelta struct {
	ItemIndex int
	Fragment  string
}
type AnnotationAdded struct {
	Address    PartAddress
	Annotation Annotation
}
type LogprobsDelta struct {
	Address PartAddress
	Tokens  []TokenLogprob
}
type PartEnded struct {
	Address PartAddress
	Part    Part
}
type ItemEnded struct {
	Index int
	Item  Item
}
type UsageUpdated struct{ Usage usage.Snapshot }
type ResponseEnded struct{ Finish Finish }

func (ResponseStarted) isEvent() {}
func (ItemStarted) isEvent()     {}
func (PartStarted) isEvent()     {}
func (TextDelta) isEvent()       {}
func (ArgumentsDelta) isEvent()  {}
func (MCPProgress) isEvent()     {}
func (CodeDelta) isEvent()       {}
func (AnnotationAdded) isEvent() {}
func (LogprobsDelta) isEvent()   {}
func (PartEnded) isEvent()       {}
func (ItemEnded) isEvent()       {}
func (UsageUpdated) isEvent()    {}
func (ResponseEnded) isEvent()   {}
