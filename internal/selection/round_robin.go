// Package selection chooses accounts from candidates supplied by request execution.
package selection

import "sync"

// AccountID identifies an account within the installation.
type AccountID string

// UpstreamID identifies a configured upstream within the installation.
type UpstreamID string

// Scope identifies the upstream and concrete model that share a selection position.
type Scope struct {
	UpstreamID UpstreamID
	Model      string
}

// RoundRobin takes turns selecting accounts in ascending ID order.
// It is safe for concurrent calls. Construct it with NewRoundRobin and do not copy it.
type RoundRobin struct {
	mu        sync.Mutex
	positions map[Scope]AccountID
}

// NewRoundRobin creates a selector with no previous selections.
func NewRoundRobin() *RoundRobin {
	return &RoundRobin{positions: make(map[Scope]AccountID)}
}

// Select returns the next candidate for scope, or the zero ID and false if empty.
// An empty call leaves the position unchanged. IDs use Go string ordering.
// The candidate slice is neither changed nor retained; callers must not modify it
// during the call. Selecting an account does not authorize or reserve its use.
func (r *RoundRobin) Select(scope Scope, candidates []AccountID) (AccountID, bool) {
	if len(candidates) == 0 {
		return "", false
	}

	// ponytail: one lock serializes scopes; split only if measured contention warrants it.
	r.mu.Lock()
	defer r.mu.Unlock()

	last, hasLast := r.positions[scope]
	smallest := candidates[0]
	var next AccountID
	foundNext := false
	for _, id := range candidates {
		if id < smallest {
			smallest = id
		}
		if hasLast && id > last && (!foundNext || id < next) {
			next = id
			foundNext = true
		}
	}
	if !foundNext {
		next = smallest
	}

	// Keep the last ID even if it later disappears from the candidates.
	r.positions[scope] = next
	return next, true
}
