package generation

// OpenAIConfigurationUpdate changes provider conversation reasoning settings.
type OpenAIConfigurationUpdate struct {
	ID        Optional[string]
	Reasoning Optional[ConfigurationReasoning]
}

type ConfigurationReasoning struct{ Effort Optional[string] }

// OpenAICompaction retains opaque provider state for replay to a compatible origin.
type OpenAICompaction struct {
	ID               Optional[string]
	EncryptedContent Optional[string]
	CreatedBy        Optional[string]
}

// OpenAICompactionTrigger must be the final input item.
type OpenAICompactionTrigger struct{ ID Optional[string] }

// OpenAIItemReference references an existing provider item by ID.
type OpenAIItemReference struct{ ID string }

func (OpenAIConfigurationUpdate) isItem() {}
func (OpenAICompaction) isItem()          {}
func (OpenAICompactionTrigger) isItem()   {}
func (OpenAIItemReference) isItem()       {}
