package generation

// CompactRequest compresses provider conversation state without generating an answer.
type CompactRequest struct {
	Model, Instructions, PreviousResponseID           Optional[string]
	Input                                             Optional[[]Item]
	PromptCacheKey, PromptCacheRetention, ServiceTier Optional[string]
	PromptCacheOptions                                Optional[CompactCacheOptions]
}

type CompactCacheOptions struct{ Mode, TTL Optional[string] }

// InputTokenRequest describes input to count; its result is not consumed usage.
type InputTokenRequest struct {
	Model, Conversation, Instructions, PreviousResponseID Optional[string]
	Input                                                 Optional[[]Item]
	ParallelToolCalls                                     Optional[bool]
	Reasoning                                             Optional[ReasoningOptions]
	Text                                                  Optional[TextOptions]
	Tools                                                 Optional[[]Tool]
	ToolChoice                                            Optional[ToolChoice]
	Personality, Truncation                               Optional[string]
}

type TextOptions struct {
	Format    OutputFormat
	Verbosity Optional[string]
}
