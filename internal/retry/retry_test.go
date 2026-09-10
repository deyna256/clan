package retry_test

import (
	"math"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/retry"
)

func TestParseRetryAfter(t *testing.T) {
	received := time.Date(1994, 11, 6, 8, 49, 35, 0, time.UTC)
	for _, tt := range []struct {
		name  string
		value string
		want  retry.Cooldown
	}{
		{name: "zero seconds", value: "0", want: retry.Cooldown{Until: received}},
		{name: "seconds", value: "2", want: retry.Cooldown{Until: received.Add(2 * time.Second)}},
		{name: "leading zeros and HTTP whitespace", value: " \t0002\t ", want: retry.Cooldown{Until: received.Add(2 * time.Second)}},
		{name: "HTTP date", value: "Sun, 06 Nov 1994 08:49:37 GMT", want: retry.Cooldown{Until: received.Add(2 * time.Second)}},
		{name: "RFC850 date", value: "Sunday, 06-Nov-94 08:49:37 GMT", want: retry.Cooldown{Until: received.Add(2 * time.Second)}},
		{name: "asctime date", value: "Sun Nov  6 08:49:37 1994", want: retry.Cooldown{Until: received.Add(2 * time.Second)}},
		{name: "past date", value: "Sun, 06 Nov 1994 08:49:34 GMT", want: retry.Cooldown{Until: received.Add(-time.Second)}},
		{name: "duration boundary", value: "9223372036", want: retry.Cooldown{Until: received.Add(9223372036 * time.Second)}},
		{name: "duration overflow", value: "9223372037", want: retry.Cooldown{Unrepresentable: true}},
		{name: "largest uint64", value: "18446744073709551615", want: retry.Cooldown{Unrepresentable: true}},
		{name: "uint64 overflow", value: "18446744073709551616", want: retry.Cooldown{Unrepresentable: true}},
		{name: "many digits", value: "999999999999999999999999999999999999", want: retry.Cooldown{Unrepresentable: true}},
		{name: "missing"},
		{name: "whitespace only", value: " \t"},
		{name: "negative", value: "-1"},
		{name: "plus sign", value: "+1"},
		{name: "fraction", value: "1.5"},
		{name: "exponent", value: "1e3"},
		{name: "non-ASCII digits", value: "１２"},
		{name: "newline is not optional whitespace", value: "2\n"},
		{name: "CRLF is not optional whitespace", value: "\r\n2"},
		{name: "Unicode space", value: "\u00a02"},
		{name: "overflow with invalid suffix", value: "999999999999999999999999999999999999x"},
		{name: "invalid date", value: "Sun, 99 Nov 1994 08:49:37 GMT"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := retry.ParseRetryAfter(tt.value, received)

			if err != nil || got.Unrepresentable != tt.want.Unrepresentable || !got.Until.Equal(tt.want.Until) {
				t.Fatalf("ParseRetryAfter() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
}

func TestParseRetryAfterRejectsZeroReceiptTime(t *testing.T) {
	for _, value := range []string{"", "1", "malformed"} {
		got, err := retry.ParseRetryAfter(value, time.Time{})

		if err == nil || got != (retry.Cooldown{}) {
			t.Fatalf("ParseRetryAfter(%q) = (%+v, %v), want zero cooldown and error", value, got, err)
		}
	}
}

func TestRetryTimeAtZeroSentinel(t *testing.T) {
	for _, tt := range []struct {
		name     string
		value    string
		received time.Time
		want     retry.Cooldown
	}{
		{name: "seconds reach zero", value: "1", received: time.Time{}.Add(-time.Second), want: retry.Cooldown{Unrepresentable: true}},
		{name: "future zero date", value: "Mon, 01 Jan 0001 00:00:00 GMT", received: time.Time{}.Add(-time.Second), want: retry.Cooldown{Unrepresentable: true}},
		{name: "past zero date", value: "Mon, 01 Jan 0001 00:00:00 GMT", received: time.Time{}.Add(time.Second)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := retry.ParseRetryAfter(tt.value, tt.received)

			if err != nil || got.Unrepresentable != tt.want.Unrepresentable || !got.Until.Equal(tt.want.Until) {
				t.Fatalf("ParseRetryAfter() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
}

func TestEvaluateFailureFacts(t *testing.T) {
	for _, tt := range []struct {
		name           string
		retryable      bool
		outcomeUnknown bool
		committed      bool
		cancelled      bool
		want           retry.Decision
	}{
		{name: "classified retryable", retryable: true, want: retry.Decision{Retry: true}},
		{name: "not classified retryable"},
		{name: "unknown outcome overrides retryable", retryable: true, outcomeUnknown: true},
		{name: "any client commitment", retryable: true, committed: true},
		{name: "cancelled", retryable: true, cancelled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := eligibleInput()
			input.Retryable, input.OutcomeUnknown = tt.retryable, tt.outcomeUnknown
			input.Committed, input.Cancelled = tt.committed, tt.cancelled

			got, err := retry.Evaluate(input)

			if err != nil || got != tt.want {
				t.Fatalf("Evaluate() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
}

func TestAttemptAndJitterLimits(t *testing.T) {
	for _, tt := range []struct {
		name     string
		attempts int
		jitter   time.Duration
		want     retry.Decision
	}{
		{name: "second attempt maximum", attempts: 1, jitter: 500 * time.Millisecond, want: retry.Decision{Retry: true, Delay: 500 * time.Millisecond}},
		{name: "third attempt maximum", attempts: 2, jitter: time.Second, want: retry.Decision{Retry: true, Delay: time.Second}},
		{name: "three attempts used", attempts: 3},
		{name: "no jitter range for a fourth attempt", attempts: 3, jitter: math.MaxInt64},
		{name: "more than three attempts used", attempts: 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := eligibleInput()
			input.Attempts, input.Jitter = tt.attempts, tt.jitter

			got, err := retry.Evaluate(input)

			if err != nil || got != tt.want {
				t.Fatalf("Evaluate() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
	if retry.SecondAttemptJitter != 500*time.Millisecond || retry.ThirdAttemptJitter != time.Second {
		t.Fatalf("published jitter bounds = %s, %s; want 500ms, 1s", retry.SecondAttemptJitter, retry.ThirdAttemptJitter)
	}
}

func TestOverlappingWaits(t *testing.T) {
	for _, tt := range []struct {
		name     string
		cooldown time.Duration
		want     time.Duration
	}{
		{name: "cooldown exceeds jitter", cooldown: 2 * time.Second, want: 2 * time.Second},
		{name: "jitter exceeds cooldown", cooldown: 250 * time.Millisecond, want: 500 * time.Millisecond},
		{name: "past cooldown", cooldown: -time.Second, want: 500 * time.Millisecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := eligibleInput()
			input.Jitter = 500 * time.Millisecond
			input.ApplicableCooldown.Until = input.Now.Add(tt.cooldown)

			got, err := retry.Evaluate(input)

			if err != nil || got != (retry.Decision{Retry: true, Delay: tt.want}) {
				t.Fatalf("Evaluate() = (%+v, %v), want delay %s", got, err, tt.want)
			}
		})
	}
}

func TestReevaluationKeepsJitterAnchor(t *testing.T) {
	input := eligibleInput()
	input.Jitter = 500 * time.Millisecond

	for _, step := range []struct {
		after time.Duration
		want  time.Duration
	}{
		{want: 500 * time.Millisecond},
		{after: 200 * time.Millisecond, want: 300 * time.Millisecond},
		{after: 500 * time.Millisecond, want: 0},
	} {
		input.Now = input.FailureAt.Add(step.after)
		expectedInput := input

		got, err := retry.Evaluate(input)

		if err != nil || got != (retry.Decision{Retry: true, Delay: step.want}) || input != expectedInput {
			t.Fatalf("after %s: Evaluate() = (%+v, %v), want delay %s and unchanged input", step.after, got, err, step.want)
		}
	}
}

func TestWaitAllowance(t *testing.T) {
	for _, tt := range []struct {
		name   string
		waited time.Duration
		jitter time.Duration
		want   retry.Decision
	}{
		{name: "exact remaining allowance", waited: 4500 * time.Millisecond, jitter: 500 * time.Millisecond, want: retry.Decision{Retry: true, Delay: 500 * time.Millisecond}},
		{name: "above remaining allowance", waited: 4500*time.Millisecond + time.Nanosecond, jitter: 500 * time.Millisecond},
		{name: "zero delay at exhausted allowance", waited: 5 * time.Second, want: retry.Decision{Retry: true}},
		{name: "nonzero delay at exhausted allowance", waited: 5 * time.Second, jitter: time.Nanosecond},
		{name: "scheduler overshoot", waited: 5*time.Second + time.Nanosecond},
		{name: "large wait cannot overflow", waited: math.MaxInt64},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := eligibleInput()
			input.Waited, input.Jitter = tt.waited, tt.jitter

			got, err := retry.Evaluate(input)

			if err != nil || got != tt.want {
				t.Fatalf("Evaluate() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
}

func TestDeadlineBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name     string
		deadline time.Duration
		jitter   time.Duration
		want     retry.Decision
	}{
		{name: "deadline already passed", deadline: -time.Nanosecond},
		{name: "deadline reached"},
		{name: "retry exactly at deadline", deadline: 500 * time.Millisecond, jitter: 500 * time.Millisecond},
		{name: "retry 1ns before deadline", deadline: 500*time.Millisecond + time.Nanosecond, jitter: 500 * time.Millisecond, want: retry.Decision{Retry: true, Delay: 500 * time.Millisecond}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := eligibleInput()
			input.Deadline, input.Jitter = input.Now.Add(tt.deadline), tt.jitter

			got, err := retry.Evaluate(input)

			if err != nil || got != tt.want {
				t.Fatalf("Evaluate() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
}

func TestReceiptTimeAndCleanup(t *testing.T) {
	for _, tt := range []struct {
		name    string
		header  string
		cleanup time.Duration
		want    retry.Decision
	}{
		{name: "six seconds elapsed during cleanup", header: "6", cleanup: 8 * time.Second, want: retry.Decision{Retry: true}},
		{name: "hundred seconds must not shrink", header: "100", cleanup: 10 * time.Second},
		{name: "overflow remains blocked", header: "99999999999999999999999999999", cleanup: 24 * time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := eligibleInput()
			input.Jitter = 500 * time.Millisecond
			cooldown, err := retry.ParseRetryAfter(tt.header, input.FailureAt)
			if err != nil {
				t.Fatal(err)
			}
			input.ApplicableCooldown = cooldown
			input.Now = input.FailureAt.Add(tt.cleanup)

			got, err := retry.Evaluate(input)

			if err != nil || got != tt.want {
				t.Fatalf("Evaluate() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
}

func TestCandidateChangePreservesAllowances(t *testing.T) {
	input := eligibleInput()
	input.Attempts, input.Waited, input.Jitter = 2, 4750*time.Millisecond, 250*time.Millisecond
	input.ApplicableCooldown = retry.Cooldown{Until: input.Now.Add(time.Second)}

	got, err := retry.Evaluate(input)
	if err != nil || got != (retry.Decision{}) {
		t.Fatalf("affected candidate = (%+v, %v), want stop", got, err)
	}
	input.ApplicableCooldown = retry.Cooldown{}
	got, err = retry.Evaluate(input)
	if err != nil || got != (retry.Decision{Retry: true, Delay: 250 * time.Millisecond}) {
		t.Fatalf("unaffected candidate = (%+v, %v), want 250ms delay", got, err)
	}
	input.Attempts = 3
	got, err = retry.Evaluate(input)
	if err != nil || got != (retry.Decision{}) {
		t.Fatalf("allowance shared after third attempt = (%+v, %v), want stop", got, err)
	}
}

func TestInvalidCountsAndDurations(t *testing.T) {
	for _, tt := range []struct {
		name     string
		attempts int
		waited   time.Duration
		jitter   time.Duration
	}{
		{name: "zero attempts"},
		{name: "negative attempts", attempts: -1},
		{name: "negative waiting", attempts: 1, waited: -1},
		{name: "negative jitter", attempts: 1, jitter: -1},
		{name: "second attempt jitter above maximum", attempts: 1, jitter: 500*time.Millisecond + time.Nanosecond},
		{name: "third attempt jitter above maximum", attempts: 2, jitter: time.Second + time.Nanosecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := eligibleInput()
			input.Attempts, input.Waited, input.Jitter = tt.attempts, tt.waited, tt.jitter

			got, err := retry.Evaluate(input)

			if err == nil || got != (retry.Decision{}) {
				t.Fatalf("Evaluate() = (%+v, %v), want zero decision and error", got, err)
			}
		})
	}
}

func TestInvalidTimesAndCooldown(t *testing.T) {
	valid := eligibleInput()
	for _, tt := range []struct {
		name     string
		failure  time.Time
		now      time.Time
		cooldown retry.Cooldown
	}{
		{name: "zero failure time", now: valid.Now},
		{name: "zero current time", failure: valid.FailureAt},
		{name: "time before failure", failure: valid.FailureAt, now: valid.Now.Add(-time.Nanosecond)},
		{name: "contradictory cooldown", failure: valid.FailureAt, now: valid.Now, cooldown: retry.Cooldown{Until: valid.Now, Unrepresentable: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := eligibleInput()
			input.FailureAt, input.Now, input.ApplicableCooldown = tt.failure, tt.now, tt.cooldown

			got, err := retry.Evaluate(input)

			if err == nil || got != (retry.Decision{}) {
				t.Fatalf("Evaluate() = (%+v, %v), want zero decision and error", got, err)
			}
		})
	}
	got, err := retry.Evaluate(retry.Input{})
	if err == nil || got != (retry.Decision{}) {
		t.Fatalf("zero input = (%+v, %v), want zero decision and error", got, err)
	}
}

func TestAbsentCooldownIsNotAnAbsoluteDate(t *testing.T) {
	input := eligibleInput()
	input.FailureAt = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	input.Now = input.FailureAt

	got, err := retry.Evaluate(input)

	if err != nil || got != (retry.Decision{Retry: true}) {
		t.Fatalf("Evaluate() = (%+v, %v), want immediate retry", got, err)
	}
}

func eligibleInput() retry.Input {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	return retry.Input{Attempts: 1, FailureAt: now, Now: now, Retryable: true}
}
