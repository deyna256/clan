package admission

import (
	"errors"
	"time"

	"github.com/deyna256/clan/internal/budget"
	"github.com/deyna256/clan/internal/usage"
)

// Attempt owns cumulative usage for one admitted upstream attempt. Copies share
// the same accounting state; separate attempts count independently.
type Attempt struct {
	state *attemptState
}

type attemptState struct {
	coordinator *Coordinator
	key         *keyState
	usage       usage.State
}

func (c *Coordinator) attempt(key *keyState) Attempt {
	return Attempt{state: &attemptState{coordinator: c, key: key}}
}

// Observe accepts normalized cumulative usage, returning its authoritative state.
// It charges only new usage at acceptance time, even after request cancellation,
// failure or release. Errors leave both attempt usage and windows unchanged and
// return the previous usage state.
func (a Attempt) Observe(snapshot usage.Snapshot) (usage.State, error) {
	if a.state == nil {
		return usage.State{}, errors.New("admission: invalid attempt handle")
	}
	s := a.state
	s.coordinator.mu.Lock()
	defer s.coordinator.mu.Unlock()
	next, increment, err := usage.Advance(s.usage, snapshot)
	if err != nil {
		return s.usage, err
	}
	windows, err := budget.Charge(s.key.budget, increment, time.Now())
	if err != nil {
		return s.usage, err
	}
	s.usage = next
	s.key.publish(windows)
	return next, nil
}
