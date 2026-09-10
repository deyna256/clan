// Package selection chooses accounts from candidates supplied by request execution.
package selection

import (
	"sync"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/upstream"
)

// Scope identifies the upstream and concrete model that share a selection position.
type Scope struct {
	UpstreamID upstream.ID
	Model      string
}

// RoundRobin takes turns selecting accounts in ascending ID order.
// It is safe for concurrent calls. Construct it with [NewRoundRobin] and do not copy it.
type RoundRobin struct {
	mu        sync.Mutex
	positions map[Scope]account.ID
}

// NewRoundRobin creates a selector with no previous selections.
func NewRoundRobin() *RoundRobin {
	return &RoundRobin{positions: make(map[Scope]account.ID)}
}

// Select returns the smallest candidate ID above the last choice for scope and true,
// wrapping to the smallest ID. The first choice is the smallest ID.
// Empty input returns ("", false) without changing the position. IDs use Go string order.
// Candidates are neither modified nor retained and must not change during the call.
// Selection does not authorize or reserve an account.
func (r *RoundRobin) Select(scope Scope, candidates []account.ID) (account.ID, bool) {
	if len(candidates) == 0 {
		return "", false
	}

	// ponytail: one lock serializes scopes; split only if measured contention warrants it.
	r.mu.Lock()
	defer r.mu.Unlock()

	last, hasLast := r.positions[scope]
	smallest := candidates[0]
	var next account.ID
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

	r.positions[scope] = next
	return next, true
}
