// Package retry evaluates classified failures without executing or waiting.
package retry

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SecondAttemptJitter is the maximum jitter before the second attempt.
const SecondAttemptJitter = 500 * time.Millisecond

// ThirdAttemptJitter is the maximum jitter before the third attempt.
const ThirdAttemptJitter = time.Second

// Cooldown is an applicable retry time. Zero means no restriction.
// Unrepresentable blocks the affected target for this request and requires a zero
// Until. The executor resolves account, model and provider scope before evaluation.
type Cooldown struct {
	Until           time.Time
	Unrepresentable bool
}

// Input contains caller-owned facts for one retry candidate. Attempts counts all
// attempts already made; Waited counts actual pauses across all accounts. Draw
// Jitter once per next attempt and reuse it, anchored to FailureAt, when replanning.
// FailureAt and Now are required, with FailureAt <= Now. Zero Deadline means none.
// Provider adapters classify failures; any unknown outcome overrides Retryable.
// Committed includes client headers, stream lifecycle events and heartbeats.
type Input struct {
	Attempts           int
	Waited             time.Duration
	Jitter             time.Duration
	FailureAt          time.Time
	Now                time.Time
	Deadline           time.Time
	Retryable          bool
	OutcomeUnknown     bool
	Committed          bool
	Cancelled          bool
	ApplicableCooldown Cooldown
}

// Decision reports whether this candidate can retry after Delay.
// The executor owns cleanup, waiting and rechecking cancellation, access and budgets.
type Decision struct {
	Retry bool
	Delay time.Duration
}

// ParseRetryAfter parses decimal seconds from receipt or an HTTP-date.
// Missing or malformed text gives no cooldown. A retry time that cannot be
// represented gives an Unrepresentable cooldown. A zero receipt time is invalid.
func ParseRetryAfter(value string, receivedAt time.Time) (Cooldown, error) {
	if receivedAt.IsZero() {
		return Cooldown{}, errors.New("retry: receipt time is required")
	}
	value = strings.Trim(value, " \t")
	digits := value != ""
	for _, char := range value {
		if char < '0' || char > '9' {
			digits = false
			break
		}
	}
	var until time.Time
	if digits {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64(math.MaxInt64/int64(time.Second)) {
			return Cooldown{Unrepresentable: true}, nil
		}
		until = receivedAt.Add(time.Duration(seconds) * time.Second)
	} else {
		var err error
		until, err = http.ParseTime(value)
		if err != nil {
			return Cooldown{}, nil
		}
	}
	if until.IsZero() && until.After(receivedAt) {
		return Cooldown{Unrepresentable: true}, nil
	}
	return Cooldown{Until: until}, nil
}

// Evaluate checks shared attempt/wait allowances and overlapping jitter/cooldown.
// A retry must start strictly before Deadline. Three attempts or more than five
// seconds already waited gives a normal stop. Invalid inputs give a zero decision
// and an error. Evaluate never changes input or chooses a new jitter draw.
func Evaluate(input Input) (Decision, error) {
	if input.Attempts <= 0 || input.Waited < 0 || input.Jitter < 0 {
		return Decision{}, errors.New("retry: attempts must be positive and durations nonnegative")
	}
	if (input.Attempts == 1 && input.Jitter > SecondAttemptJitter) || (input.Attempts == 2 && input.Jitter > ThirdAttemptJitter) {
		return Decision{}, errors.New("retry: jitter exceeds the attempt limit")
	}
	if input.FailureAt.IsZero() || input.Now.IsZero() || input.FailureAt.After(input.Now) {
		return Decision{}, errors.New("retry: observation times are required and must be ordered")
	}
	if input.ApplicableCooldown.Unrepresentable && !input.ApplicableCooldown.Until.IsZero() {
		return Decision{}, errors.New("retry: unrepresentable cooldown cannot have a retry time")
	}
	if !input.Retryable || input.OutcomeUnknown || input.Committed || input.Cancelled {
		return Decision{}, nil
	}
	if input.Attempts >= 3 || input.Waited > 5*time.Second || input.ApplicableCooldown.Unrepresentable {
		return Decision{}, nil
	}
	delay := max(0, input.FailureAt.Add(input.Jitter).Sub(input.Now))
	if !input.ApplicableCooldown.Until.IsZero() {
		delay = max(delay, input.ApplicableCooldown.Until.Sub(input.Now))
	}
	if delay > 5*time.Second-input.Waited {
		return Decision{}, nil
	}
	if !input.Deadline.IsZero() && !input.Now.Add(delay).Before(input.Deadline) {
		return Decision{}, nil
	}
	return Decision{Retry: true, Delay: delay}, nil
}
