package generation

import "github.com/deyna256/clan/internal/usage"

// Identity remains available when a provider response fails after creation.
type Identity struct{ ID, Model string }

// Result retains observed usage and identity even when Response is zero on error.
type Result struct {
	Identity Identity
	Response Response
	Usage    usage.Snapshot
}

type Response struct {
	Output []Item
	Finish Finish
}

type Finish struct {
	Status string // completed or incomplete; failure is returned as an error.
	Reason string // stop, tool_calls, max_output_tokens, content_filter, or a provider reason.
}

// Failure reports safe facts for execution policy. It never contains raw payloads.
type Failure struct {
	Kind           FailureKind
	Retryable      bool
	OutcomeUnknown bool
}

type FailureKind string

const (
	InvalidRequest FailureKind = "invalid_request"
	Authentication FailureKind = "authentication"
	RateLimited    FailureKind = "rate_limited"
	QuotaExhausted FailureKind = "quota_exhausted"
	Unavailable    FailureKind = "unavailable"
	ProtocolError  FailureKind = "protocol_error"
	Unsupported    FailureKind = "unsupported"
	TransportError FailureKind = "transport_error"
)

func (e *Failure) Error() string { return "generation: " + string(e.Kind) }
