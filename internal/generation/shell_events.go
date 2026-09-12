package generation

type ShellCommandDelta struct {
	ItemIndex, CommandIndex int
	Fragment                string
}

type ShellOutputDelta struct {
	ItemIndex, CommandIndex int
	Stdout, Stderr          string
}

// ShellOutputEnded carries a command's output snapshot, not additional bytes.
// ItemEnded carries the provider's complete output list without command addresses.
type ShellOutputEnded struct {
	ItemIndex, CommandIndex int
	Output                  []ShellOutput
}

func (ShellCommandDelta) isEvent() {}
func (ShellOutputDelta) isEvent()  {}
func (ShellOutputEnded) isEvent()  {}
