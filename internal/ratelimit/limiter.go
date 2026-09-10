// Package ratelimit limits client request rates per access key.
package ratelimit

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"golang.org/x/time/rate"
)

// Unlimited disables request-rate limiting.
const Unlimited = -1

// ErrLimitReached means no request permit is available.
var ErrLimitReached = errors.New("ratelimit: limit reached")

// ErrNotConfigured means the access key has no rate configuration.
var ErrNotConfigured = errors.New("ratelimit: access key is not configured")

// Config is a complete rate configuration. Nil Burst defaults to positive RPM.
// RPM must be Unlimited or nonnegative; explicit Burst must be positive even when
// RPM is zero or Unlimited. Positive values must not exceed 2^53 or platform int.
type Config struct {
	RPM   int
	Burst *int
}

// Limiter stores the current configuration and bucket for each access key.
// It is safe for concurrent use. Construct it with [New] and do not copy it.
type Limiter struct {
	mu   sync.Mutex
	keys map[accesskey.ID]entry
}

type entry struct {
	rpm    int
	bucket *rate.Limiter
}

// New creates a limiter without configured keys.
func New() *Limiter {
	return &Limiter{keys: make(map[accesskey.ID]entry)}
}

// Configure atomically replaces a key's configuration. Invalid input changes nothing.
// Positive updates preserve the refilled balance, capped at the new burst. Zero
// and Unlimited discard the bucket; returning to positive starts full.
// Configure reads but does not retain config.Burst; do not mutate it during the call.
// The configuration owner retains any explicit burst while RPM is disabled.
func (l *Limiter) Configure(keyID accesskey.ID, config Config) error {
	if strings.TrimSpace(string(keyID)) == "" {
		return errors.New("ratelimit: access key id is required")
	}
	if config.RPM < Unlimited || int64(config.RPM) > 1<<53 {
		return errors.New("ratelimit: RPM must be Unlimited or between 0 and 2^53")
	}
	burst := config.RPM
	if config.Burst != nil {
		burst = *config.Burst
		if burst <= 0 || int64(burst) > 1<<53 {
			return errors.New("ratelimit: burst must be between 1 and 2^53")
		}
	}

	// ponytail: one lock serializes keys; split only if measured contention warrants it.
	l.mu.Lock()
	defer l.mu.Unlock()
	current := entry{rpm: config.RPM}
	if config.RPM > 0 {
		refill := rate.Limit(config.RPM) / 60
		current.bucket = l.keys[keyID].bucket
		if current.bucket == nil {
			current.bucket = rate.NewLimiter(refill, burst)
		} else {
			now := time.Now()
			// SetBurstAt alone leaves excess tokens until the next advance.
			current.bucket.SetBurstAt(now, burst)
			current.bucket.SetLimitAt(now, refill)
		}
	}
	l.keys[keyID] = current
	return nil
}

// TryAcquire consumes one permit without queuing for capacity or offering refunds.
// It uses the latest completed configuration; overlapping calls are serialized.
// Call it only when the other admission checks have passed.
func (l *Limiter) TryAcquire(keyID accesskey.ID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	current, ok := l.keys[keyID]
	if !ok {
		return ErrNotConfigured
	}
	if current.rpm == Unlimited {
		return nil
	}
	if current.rpm == 0 {
		return ErrLimitReached
	}
	if !current.bucket.Allow() {
		return ErrLimitReached
	}
	return nil
}

// Remove forgets a key's configuration and bucket. Unknown keys are ignored.
// Reconfiguring the key creates fresh state; do not use Remove for idle cleanup.
func (l *Limiter) Remove(keyID accesskey.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, keyID)
}
