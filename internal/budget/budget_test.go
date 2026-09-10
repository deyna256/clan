package budget_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/budget"
	"github.com/deyna256/clan/internal/usage"
)

func TestCheckEligibility(t *testing.T) {
	start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	state := budget.State{
		FiveHours: budget.Window{OpenedAt: start, Used: 50},
		SevenDays: budget.Window{OpenedAt: start, Used: 100},
	}
	for _, tt := range []struct {
		name    string
		limits  budget.Limits
		after   time.Duration
		wantErr error
	}{
		{name: "absent caps"},
		{name: "both below cap", limits: budget.Limits{FiveHours: new(int64(51)), SevenDays: new(int64(101))}},
		{name: "short equality", limits: budget.Limits{FiveHours: new(int64(50))}, wantErr: budget.ErrExhausted},
		{name: "long equality", limits: budget.Limits{SevenDays: new(int64(100))}, wantErr: budget.ErrExhausted},
		{name: "overrun", limits: budget.Limits{FiveHours: new(int64(49))}, wantErr: budget.ErrExhausted},
		{name: "short expiry", limits: budget.Limits{FiveHours: new(int64(50))}, after: 5 * time.Hour},
		{name: "long still exhausted", limits: budget.Limits{SevenDays: new(int64(100))}, after: 5 * time.Hour, wantErr: budget.ErrExhausted},
		{name: "long expiry", limits: budget.Limits{SevenDays: new(int64(100))}, after: 168 * time.Hour},
		{name: "zero short after expiry", limits: budget.Limits{FiveHours: new(int64(0))}, after: 5 * time.Hour, wantErr: budget.ErrExhausted},
		{name: "zero long after expiry", limits: budget.Limits{SevenDays: new(int64(0))}, after: 168 * time.Hour, wantErr: budget.ErrExhausted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := budget.Check(state, tt.limits, start.Add(tt.after))

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Check() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckZeroCapDeniesUnopenedWindow(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	limits := budget.Limits{SevenDays: new(int64(0))}

	err := budget.Check(budget.State{}, limits, now)

	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("Check() = %v, want ErrExhausted", err)
	}
}

func TestCheckDoesNotOpenWindows(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	previous := budget.State{}
	cap := int64(1)

	if err := budget.Check(previous, budget.Limits{FiveHours: &cap}, now); err != nil {
		t.Fatal(err)
	}
	next, err := budget.Admit(previous, now.Add(time.Hour))

	want := budget.State{
		FiveHours: budget.Window{OpenedAt: now.Add(time.Hour)},
		SevenDays: budget.Window{OpenedAt: now.Add(time.Hour)},
	}
	if err != nil || next != want || cap != 1 {
		t.Fatalf("Admit() = (%+v, %v), cap = %d; want %+v and unchanged cap", next, err, cap, want)
	}
}

func TestAdmitRenewsOnlyExpiredWindows(t *testing.T) {
	start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	previous := budget.State{
		FiveHours: budget.Window{OpenedAt: start, Used: 10},
		SevenDays: budget.Window{OpenedAt: start, Used: 20},
	}
	for _, tt := range []struct {
		name  string
		after time.Duration
		want  budget.State
	}{
		{name: "before expiry", after: 5*time.Hour - time.Nanosecond, want: previous},
		{
			name: "retry admitted at expiry", after: 5 * time.Hour,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start.Add(5 * time.Hour)},
				SevenDays: budget.Window{OpenedAt: start, Used: 20},
			},
		},
		{
			name: "late fallback anchors at admission", after: 7 * time.Hour,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start.Add(7 * time.Hour)},
				SevenDays: budget.Window{OpenedAt: start, Used: 20},
			},
		},
		{
			name: "long idle gap", after: 200 * time.Hour,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start.Add(200 * time.Hour)},
				SevenDays: budget.Window{OpenedAt: start.Add(200 * time.Hour)},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next, err := budget.Admit(previous, start.Add(tt.after))

			if err != nil || next != tt.want {
				t.Fatalf("Admit() = (%+v, %v), want %+v", next, err, tt.want)
			}
		})
	}
}

func TestLongExpiryKeepsRecentShortWindow(t *testing.T) {
	start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	previous := budget.State{
		FiveHours: budget.Window{OpenedAt: start.Add(166 * time.Hour), Used: 3},
		SevenDays: budget.Window{OpenedAt: start, Used: 100},
	}
	now := start.Add(168 * time.Hour)

	next, err := budget.Admit(previous, now)

	want := budget.State{
		FiveHours: budget.Window{OpenedAt: start.Add(166 * time.Hour), Used: 3},
		SevenDays: budget.Window{OpenedAt: now},
	}
	if err != nil || next != want {
		t.Fatalf("Admit() = (%+v, %v), want %+v", next, err, want)
	}
}

func TestChargeAtExpiry(t *testing.T) {
	start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	previous := budget.State{
		FiveHours: budget.Window{OpenedAt: start, Used: 100},
		SevenDays: budget.Window{OpenedAt: start, Used: 100},
	}
	for _, tt := range []struct {
		name  string
		after time.Duration
		want  budget.State
	}{
		{
			name: "before", after: 5*time.Hour - time.Nanosecond,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start, Used: 150},
				SevenDays: budget.Window{OpenedAt: start, Used: 150},
			},
		},
		{
			name: "at", after: 5 * time.Hour,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start.Add(5 * time.Hour), Used: 50},
				SevenDays: budget.Window{OpenedAt: start, Used: 150},
			},
		},
		{
			name: "after", after: 5*time.Hour + time.Nanosecond,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start.Add(5*time.Hour + time.Nanosecond), Used: 50},
				SevenDays: budget.Window{OpenedAt: start, Used: 150},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next, err := budget.Charge(previous, 50, start.Add(tt.after))

			if err != nil || next != tt.want {
				t.Fatalf("Charge() = (%+v, %v), want %+v", next, err, tt.want)
			}
		})
	}
}

func TestLateChargeOpensAndExhaustsWindows(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	limits := budget.Limits{FiveHours: new(int64(100)), SevenDays: new(int64(200))}

	next, err := budget.Charge(budget.State{}, 150, now)

	want := budget.State{
		FiveHours: budget.Window{OpenedAt: now, Used: 150},
		SevenDays: budget.Window{OpenedAt: now, Used: 150},
	}
	if err != nil || next != want {
		t.Fatalf("Charge() = (%+v, %v), want full overrun %+v", next, err, want)
	}
	if err := budget.Check(next, limits, now); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("Check() = %v, want ErrExhausted before first admitted attempt", err)
	}
}

func TestCumulativeUsageAcrossExpiry(t *testing.T) {
	start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	var attempt usage.State
	var windows budget.State
	for i, step := range []struct {
		after time.Duration
		total int64
		delta int64
		want  budget.State
	}{
		{
			total: 100, delta: 100,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start, Used: 100},
				SevenDays: budget.Window{OpenedAt: start, Used: 100},
			},
		},
		{
			after: 5 * time.Hour, total: 150, delta: 50,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start.Add(5 * time.Hour), Used: 50},
				SevenDays: budget.Window{OpenedAt: start, Used: 150},
			},
		},
		{
			after: 168 * time.Hour, total: 150, delta: 0,
			want: budget.State{
				FiveHours: budget.Window{OpenedAt: start.Add(5 * time.Hour), Used: 50},
				SevenDays: budget.Window{OpenedAt: start, Used: 150},
			},
		},
	} {
		nextAttempt, delta, err := usage.Advance(attempt, usage.Snapshot{Total: usage.Counter{Known: true, Tokens: step.total}})
		if err != nil {
			t.Fatalf("step %d usage: %v", i, err)
		}
		nextWindows, err := budget.Charge(windows, delta, start.Add(step.after))

		if err != nil || delta != step.delta || nextWindows != step.want {
			t.Fatalf("step %d: delta %d, Charge() = (%+v, %v), want delta %d and %+v", i, delta, nextWindows, err, step.delta, step.want)
		}
		attempt, windows = nextAttempt, nextWindows
	}
}

func TestCapChangesPreserveAccounting(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	previous := budget.State{
		FiveHours: budget.Window{OpenedAt: now, Used: 100},
		SevenDays: budget.Window{OpenedAt: now, Used: 100},
	}
	if err := budget.Check(previous, budget.Limits{}, now); err != nil {
		t.Fatal(err)
	}

	next, err := budget.Charge(previous, 50, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		cap  *int64
		want error
	}{
		{name: "exhausted", cap: new(int64(100)), want: budget.ErrExhausted},
		{name: "removed"},
		{name: "restored", cap: new(int64(100)), want: budget.ErrExhausted},
		{name: "raised", cap: new(int64(151))},
		{name: "lowered", cap: new(int64(150)), want: budget.ErrExhausted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := budget.Check(next, budget.Limits{SevenDays: tt.cap}, now)

			if !errors.Is(err, tt.want) {
				t.Fatalf("Check() = %v, want %v", err, tt.want)
			}
		})
	}
	want := budget.State{
		FiveHours: budget.Window{OpenedAt: now, Used: 150},
		SevenDays: budget.Window{OpenedAt: now, Used: 150},
	}
	if next != want {
		t.Fatalf("after cap changes = %+v, want %+v", next, want)
	}
}

func TestInvalidStateAndTimeLeaveStateUnchanged(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name     string
		previous budget.State
		now      time.Time
	}{
		{name: "zero time"},
		{name: "unopened short with usage", previous: budget.State{FiveHours: budget.Window{Used: 1}}, now: now},
		{name: "unopened long with usage", previous: budget.State{SevenDays: budget.Window{Used: 1}}, now: now},
		{name: "negative expired short", previous: budget.State{FiveHours: budget.Window{OpenedAt: now.Add(-time.Hour * 6), Used: -1}}, now: now},
		{name: "negative long", previous: budget.State{SevenDays: budget.Window{OpenedAt: now, Used: -1}}, now: now},
		{name: "before short opening", previous: budget.State{FiveHours: budget.Window{OpenedAt: now.Add(time.Nanosecond)}}, now: now},
		{name: "before long opening", previous: budget.State{SevenDays: budget.Window{OpenedAt: now.Add(time.Nanosecond)}}, now: now},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := budget.Check(tt.previous, budget.Limits{}, tt.now); err == nil || errors.Is(err, budget.ErrExhausted) {
				t.Fatalf("Check() = %v, want validation error", err)
			}
			next, err := budget.Admit(tt.previous, tt.now)
			if err == nil || next != tt.previous {
				t.Fatalf("Admit() = (%+v, %v), want previous state and error", next, err)
			}
			for _, increment := range []int64{0, 1} {
				next, err := budget.Charge(tt.previous, increment, tt.now)
				if err == nil || next != tt.previous {
					t.Fatalf("Charge(%d) = (%+v, %v), want previous state and error", increment, next, err)
				}
			}
		})
	}
}

func TestInvalidLimits(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	for _, limits := range []budget.Limits{
		{FiveHours: new(int64(-1))},
		{SevenDays: new(int64(-2))},
		{FiveHours: new(int64(0)), SevenDays: new(int64(-1))},
	} {
		err := budget.Check(budget.State{}, limits, now)

		if err == nil || errors.Is(err, budget.ErrExhausted) {
			t.Fatalf("Check() = %v, want validation error", err)
		}
	}
}

func TestChargeRejectsInvalidIncrement(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		previous  budget.State
		increment int64
	}{
		{name: "negative increment", increment: -1},
		{name: "short overflow", previous: budget.State{FiveHours: budget.Window{OpenedAt: now, Used: math.MaxInt64}}, increment: 1},
		{
			name: "long overflow must not open short",
			previous: budget.State{
				FiveHours: budget.Window{OpenedAt: now.Add(-6 * time.Hour), Used: 10},
				SevenDays: budget.Window{OpenedAt: now, Used: math.MaxInt64},
			},
			increment: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next, err := budget.Charge(tt.previous, tt.increment, now)

			if err == nil || next != tt.previous {
				t.Fatalf("Charge() = (%+v, %v), want previous state and error", next, err)
			}
		})
	}
}

func TestChargeAcceptsMaxInt64(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	previous := budget.State{
		FiveHours: budget.Window{OpenedAt: now, Used: math.MaxInt64 - 1},
		SevenDays: budget.Window{OpenedAt: now, Used: math.MaxInt64 - 1},
	}

	next, err := budget.Charge(previous, 1, now)

	want := budget.State{
		FiveHours: budget.Window{OpenedAt: now, Used: math.MaxInt64},
		SevenDays: budget.Window{OpenedAt: now, Used: math.MaxInt64},
	}
	if err != nil || next != want {
		t.Fatalf("maximum usage = (%+v, %v), want %+v", next, err, want)
	}
	if err := budget.Check(next, budget.Limits{FiveHours: new(int64(math.MaxInt64))}, now); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("maximum cap at equality = %v, want ErrExhausted", err)
	}
	next, err = budget.Charge(next, math.MaxInt64, now.Add(168*time.Hour))
	want.FiveHours.OpenedAt, want.SevenDays.OpenedAt = now.Add(168*time.Hour), now.Add(168*time.Hour)
	if err != nil || next != want {
		t.Fatalf("expired counters = (%+v, %v), want fresh windows %+v", next, err, want)
	}
}

func TestZeroChargeDoesNotOpenWindows(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	previous := budget.State{}

	next, err := budget.Charge(previous, 0, now)

	if err != nil || next != previous {
		t.Fatalf("Charge(0) = (%+v, %v), want unchanged state", next, err)
	}
}
