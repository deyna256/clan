// Package concurrency limits active requests per access key.
package concurrency

import (
	"errors"
	"strings"
	"sync"

	"github.com/deyna256/clan/internal/accesskey"
)

// Unlimited disables the cap while retaining active-slot counts.
const Unlimited = -1

// ErrLimitReached means the supplied limit permits no further acquisitions.
var ErrLimitReached = errors.New("concurrency: limit reached")

// Limiter counts active slots. It is safe for concurrent use.
// Construct it with [New] and do not copy it.
type Limiter struct {
	mu     sync.Mutex
	active map[accesskey.ID]int
}

// New creates a limiter with no active slots.
func New() *Limiter {
	return &Limiter{active: make(map[accesskey.ID]int)}
}

// TryAcquire takes one slot using the supplied limit, without queuing for capacity.
// Zero denies access; [Unlimited] removes the cap. Limits below Unlimited and blank
// IDs are invalid. Errors leave counts unchanged and return a zero slot.
func (l *Limiter) TryAcquire(keyID accesskey.ID, limit int) (Slot, error) {
	if strings.TrimSpace(string(keyID)) == "" {
		return Slot{}, errors.New("concurrency: access key id is required")
	}
	if limit < Unlimited {
		return Slot{}, errors.New("concurrency: limit must be Unlimited or nonnegative")
	}

	// ponytail: one lock serializes keys; split only if measured contention warrants it.
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit != Unlimited && l.active[keyID] >= limit {
		return Slot{}, ErrLimitReached
	}
	l.active[keyID]++
	return Slot{release: sync.OnceFunc(func() { l.release(keyID) })}, nil
}

func (l *Limiter) release(keyID accesskey.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active[keyID]--
	if l.active[keyID] == 0 {
		delete(l.active, keyID)
	}
}

// Slot owns one acquisition. Copies share the same release operation.
type Slot struct {
	release func()
}

// Release frees the slot at most once, including concurrent calls through copies.
// Call it after request cleanup. Releasing a zero slot is a no-op.
func (s Slot) Release() {
	if s.release != nil {
		s.release()
	}
}
