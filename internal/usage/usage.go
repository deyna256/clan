// Package usage describes observed token counts for diagnostics.
package usage

// Counter distinguishes observed zero tokens from an unknown count.
// Tokens must be nonnegative, and must be zero when Known is false.
type Counter struct {
	Tokens int64
	Known  bool
}

// Snapshot contains the latest observed counts for one attempt.
// Counts are not added across snapshots; the zero value means unknown usage.
type Snapshot struct {
	Input, Output, Total Counter
}
