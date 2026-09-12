// Package retry interprets provider retry timing.
package retry

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CooldownKind identifies the restriction on a retry candidate.
type CooldownKind uint8

const (
	NoCooldown   CooldownKind = iota // No additional restriction.
	RetryAt                          // Retry at or after Until.
	RetryBlocked                     // No retry for this candidate during this request.
)

// Cooldown is a restriction resolved by the executor for one candidate.
// Its zero value means no restriction. RetryAt uses Until, including the zero date;
// other kinds require a zero Until.
type Cooldown struct {
	Kind  CooldownKind
	Until time.Time
}

// ParseRetryAfter parses decimal seconds from receipt or an HTTP-date.
// Missing or malformed text gives no cooldown. A retry time that cannot be
// represented gives RetryBlocked. A zero receipt time is invalid.
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
			return Cooldown{Kind: RetryBlocked}, nil
		}
		until = receivedAt.Add(time.Duration(seconds) * time.Second)
	} else {
		var err error
		until, err = http.ParseTime(value)
		if err != nil {
			return Cooldown{}, nil
		}
	}
	return Cooldown{Kind: RetryAt, Until: until}, nil
}
