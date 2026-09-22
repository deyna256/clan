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
	if value != "" && value[0] >= '0' && value[0] <= '9' {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err == nil {
			if seconds > uint64(math.MaxInt64/int64(time.Second)) {
				return Cooldown{Kind: RetryBlocked}, nil
			}
			return Cooldown{Kind: RetryAt, Until: receivedAt.Add(time.Duration(seconds) * time.Second)}, nil
		}
		// ParseUint can overflow before reaching an invalid suffix.
		if errors.Is(err, strconv.ErrRange) && strings.Trim(value, "0123456789") == "" {
			return Cooldown{Kind: RetryBlocked}, nil
		}
	}
	until, err := http.ParseTime(value)
	if err != nil {
		return Cooldown{}, nil
	}
	return Cooldown{Kind: RetryAt, Until: until}, nil
}
