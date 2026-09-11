// Package budget computes fixed token-budget windows from caller-owned state.
package budget

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrExhausted means at least one configured budget permits no new attempt.
var ErrExhausted = errors.New("budget: exhausted")

// Limits contains optional nonnegative token caps. Nil disables a check; zero
// denies all attempts. Caps never change the accounting windows or their usage.
type Limits struct {
	FiveHours, SevenDays *int64
}

// Window stores its opening and observed usage. Zero is an unopened window;
// an unopened window must have zero usage. Usage must never be negative.
type Window struct {
	OpenedAt time.Time
	Used     int64
}

// State contains independent windows of five and 168 elapsed hours.
// Both accrue usage regardless of configured caps. Callers own persistence and
// serialization, and must supply chronological accounting times for each key.
// Times must be nonzero and not precede either saved opening; backwards events
// within an active window cannot be detected from this state alone.
type State struct {
	FiveHours, SevenDays Window
}

// Check checks eligibility without changing state or opening windows.
// Expired usage does not count. It reads but never retains or changes limit
// pointers; callers must not mutate them during the call.
func Check(state State, limits Limits, now time.Time) error {
	if err := state.validate(now); err != nil {
		return err
	}
	for _, limit := range []*int64{limits.FiveHours, limits.SevenDays} {
		if limit != nil && *limit < 0 {
			return errors.New("budget: limits must be nonnegative")
		}
	}
	current := state.current(now)
	if limits.FiveHours != nil && current.FiveHours.Used >= *limits.FiveHours {
		return ErrExhausted
	}
	if limits.SevenDays != nil && current.SevenDays.Used >= *limits.SevenDays {
		return ErrExhausted
	}
	return nil
}

// Admit records an already admitted upstream attempt, including retries and
// fallback. It opens inactive or expired windows at now, without authorizing or
// charging the attempt. Invalid state or time returns the previous state.
func Admit(previous State, now time.Time) (State, error) {
	if err := previous.validate(now); err != nil {
		return previous, err
	}
	return previous.current(now), nil
}

// Charge adds a newly accounted increment to both windows, including overruns.
// Supply only new usage, such as the delta from usage.Advance, and its accounting
// acceptance time. Zero changes nothing after validation. Errors return the whole
// previous state unchanged. Callers own synchronization and persistence.
func Charge(previous State, increment int64, now time.Time) (State, error) {
	if increment < 0 {
		return previous, errors.New("budget: increment must be nonnegative")
	}
	next, err := Admit(previous, now)
	if err != nil || increment == 0 {
		return previous, err
	}
	if next.FiveHours.Used > math.MaxInt64-increment || next.SevenDays.Used > math.MaxInt64-increment {
		return previous, errors.New("budget: usage exceeds int64")
	}
	next.FiveHours.Used += increment
	next.SevenDays.Used += increment
	return next, nil
}

func (s State) validate(now time.Time) error {
	if now.IsZero() {
		return errors.New("budget: accounting time is required")
	}
	for _, field := range []struct {
		name   string
		window Window
	}{
		{name: "five-hour", window: s.FiveHours},
		{name: "seven-day", window: s.SevenDays},
	} {
		if field.window.Used < 0 || (field.window.OpenedAt.IsZero() && field.window.Used != 0) {
			return fmt.Errorf("budget: invalid %s usage", field.name)
		}
		if !field.window.OpenedAt.IsZero() && now.Before(field.window.OpenedAt) {
			return fmt.Errorf("budget: accounting time precedes %s opening", field.name)
		}
	}
	return nil
}

func (s State) current(now time.Time) State {
	return State{
		FiveHours: s.FiveHours.current(now, 5*time.Hour),
		SevenDays: s.SevenDays.current(now, 168*time.Hour),
	}
}

func (w Window) current(now time.Time, duration time.Duration) Window {
	if w.OpenedAt.IsZero() || now.Sub(w.OpenedAt) >= duration {
		return Window{OpenedAt: now}
	}
	return w
}
