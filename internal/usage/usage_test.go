package usage_test

import (
	"math"
	"testing"

	"github.com/deyna256/clan/internal/usage"
)

func TestAdvanceCountsOnlyKnownTokens(t *testing.T) {
	tests := []struct {
		name     string
		snapshot usage.Snapshot
		amount   int64
		complete bool
	}{
		{name: "unknown"},
		{name: "observed zero", snapshot: usage.Snapshot{Total: known(0)}, complete: true},
		{name: "total only", snapshot: usage.Snapshot{Total: known(100)}, amount: 100, complete: true},
		{name: "input only", snapshot: usage.Snapshot{Input: known(80)}, amount: 80},
		{
			name:     "disjoint partial subsets",
			snapshot: usage.Snapshot{CacheRead: known(70), CacheWrite: known(10), Reasoning: known(5)},
			amount:   85,
		},
		{
			name:     "inclusive input and partial output",
			snapshot: usage.Snapshot{Input: known(100), CacheRead: known(70), Reasoning: known(5)},
			amount:   105,
		},
		{
			name:     "partial input and inclusive output",
			snapshot: usage.Snapshot{CacheWrite: known(10), Output: known(20), Reasoning: known(5)},
			amount:   30,
		},
		{
			name:     "total covers incomplete breakdown",
			snapshot: usage.Snapshot{Total: known(100), Input: known(80), Reasoning: known(5)},
			amount:   100, complete: true,
		},
		{
			name: "inclusive parents do not double count subsets",
			snapshot: usage.Snapshot{
				Input: known(1000), Output: known(200), CacheRead: known(700), Reasoning: known(50),
			},
			amount: 1200, complete: true,
		},
		{
			name: "largest representable inclusive sum",
			snapshot: usage.Snapshot{
				Input: known(math.MaxInt64 - 1), Output: known(1), CacheRead: known(math.MaxInt64 - 1), Reasoning: known(1),
			},
			amount: math.MaxInt64, complete: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := usage.State{}

			next, delta, err := usage.Advance(previous, tt.snapshot)

			if err != nil {
				t.Fatal(err)
			}
			if next != (usage.State{Usage: tt.snapshot, Charged: tt.amount}) || delta != tt.amount {
				t.Errorf("Advance() = (%+v, %d), want snapshot preserved and charge %d", next, delta, tt.amount)
			}
			if got := next.Usage.Complete(); got != tt.complete {
				t.Errorf("Complete() = %t, want %t", got, tt.complete)
			}
		})
	}
}

func TestCumulativeSnapshotsAndLateBreakdowns(t *testing.T) {
	steps := []struct {
		snapshot usage.Snapshot
		delta    int64
	}{
		{snapshot: usage.Snapshot{Total: known(100)}, delta: 100},
		{snapshot: usage.Snapshot{Total: known(150)}, delta: 50},
		{snapshot: usage.Snapshot{Total: known(150)}},
		{snapshot: usage.Snapshot{Total: known(150), Input: known(100)}},
		{snapshot: usage.Snapshot{Total: known(150), Input: known(100), Output: known(50), CacheRead: known(70)}},
		{snapshot: usage.Snapshot{Total: known(160), Input: known(100), Output: known(60), CacheRead: known(70)}, delta: 10},
	}
	var state usage.State

	for i, step := range steps {
		next, delta, err := usage.Advance(state, step.snapshot)

		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if delta != step.delta || next.Charged != step.snapshot.Total.Tokens || next.Usage != step.snapshot {
			t.Fatalf("step %d: Advance() = (%+v, %d), want snapshot preserved and delta %d", i, next, delta, step.delta)
		}
		state = next
	}
}

func TestStaleTotalRetainsAccountingWatermark(t *testing.T) {
	state := usage.State{Usage: usage.Snapshot{Total: known(100), Input: known(80)}, Charged: 100}
	partial := usage.Snapshot{Input: known(90)}

	next, delta, err := usage.Advance(state, partial)

	if err != nil || delta != 0 || next != (usage.State{Usage: partial, Charged: 100}) || next.Usage.Complete() {
		t.Fatalf("stale total: Advance() = (%+v, %d, %v), want incomplete snapshot and charge watermark 100", next, delta, err)
	}
	state = next
	next, delta, err = usage.Advance(state, partial)
	if err != nil || delta != 0 || next != state {
		t.Fatalf("repeated partial: Advance() = (%+v, %d, %v), want unchanged state", next, delta, err)
	}

	belowWatermark := usage.Snapshot{Input: known(90), Output: known(5)}
	next, delta, err = usage.Advance(state, belowWatermark)
	if err == nil || delta != 0 || next != state {
		t.Fatalf("complete total below watermark: Advance() = (%+v, %d, %v), want error and unchanged state", next, delta, err)
	}

	complete := usage.Snapshot{Input: known(90), Output: known(20)}
	next, delta, err = usage.Advance(state, complete)
	if err != nil || delta != 10 || next != (usage.State{Usage: complete, Charged: 110}) || !next.Usage.Complete() {
		t.Fatalf("new complete total: Advance() = (%+v, %d, %v), want complete snapshot and delta 10", next, delta, err)
	}
}

func TestLateParentsDoNotRecountSubsets(t *testing.T) {
	steps := []struct {
		snapshot usage.Snapshot
		delta    int64
		charged  int64
	}{
		{snapshot: usage.Snapshot{CacheRead: known(70), CacheWrite: known(10), Reasoning: known(5)}, delta: 85, charged: 85},
		{snapshot: usage.Snapshot{Input: known(100), CacheRead: known(70), CacheWrite: known(10), Reasoning: known(5)}, delta: 20, charged: 105},
		{snapshot: usage.Snapshot{Input: known(100), Output: known(20), CacheRead: known(70), CacheWrite: known(10), Reasoning: known(5)}, delta: 15, charged: 120},
	}
	var state usage.State

	for i, step := range steps {
		next, delta, err := usage.Advance(state, step.snapshot)

		if err != nil || delta != step.delta || next != (usage.State{Usage: step.snapshot, Charged: step.charged}) {
			t.Fatalf("step %d: Advance() = (%+v, %d, %v), want charge %d and delta %d", i, next, delta, err, step.charged, step.delta)
		}
		state = next
	}
}

func TestStaleTotalCanBeExceededByPartialUsage(t *testing.T) {
	previous := usage.State{Usage: usage.Snapshot{Total: known(100)}, Charged: 100}
	snapshot := usage.Snapshot{Input: known(120)}

	next, delta, err := usage.Advance(previous, snapshot)

	if err != nil || delta != 20 || next != (usage.State{Usage: snapshot, Charged: 120}) || next.Usage.Complete() {
		t.Fatalf("Advance() = (%+v, %d, %v), want incomplete snapshot and delta 20", next, delta, err)
	}
}

func TestAttemptsAreAccountedIndependently(t *testing.T) {
	failed := usage.Snapshot{Total: known(500)}
	succeeded := usage.Snapshot{Total: known(1000)}

	first, firstCharge, firstErr := usage.Advance(usage.State{}, failed)
	second, secondCharge, secondErr := usage.Advance(usage.State{}, succeeded)
	_, repeatedCharge, repeatedErr := usage.Advance(first, failed)

	if firstErr != nil || secondErr != nil || repeatedErr != nil {
		t.Fatalf("Advance() errors: %v, %v, %v", firstErr, secondErr, repeatedErr)
	}
	if firstCharge != 500 || secondCharge != 1000 || repeatedCharge != 0 || first.Charged+second.Charged != 1500 {
		t.Errorf("attempt charges = %d, %d; repeated charge = %d; want 500, 1000, 0", firstCharge, secondCharge, repeatedCharge)
	}
}

func TestInvalidSnapshotsLeaveStateUnchanged(t *testing.T) {
	tests := []struct {
		name     string
		snapshot usage.Snapshot
	}{
		{name: "negative", snapshot: usage.Snapshot{Input: known(-1)}},
		{name: "unknown nonzero payload", snapshot: usage.Snapshot{Output: usage.Counter{Tokens: 1}}},
		{name: "cache exceeds input", snapshot: usage.Snapshot{Input: known(5), CacheRead: known(3), CacheWrite: known(3)}},
		{name: "reasoning exceeds output", snapshot: usage.Snapshot{Output: known(5), Reasoning: known(6)}},
		{name: "total below partial subsets", snapshot: usage.Snapshot{Total: known(5), CacheWrite: known(3), Reasoning: known(3)}},
		{name: "total exceeds complete parents", snapshot: usage.Snapshot{Total: known(6), Input: known(3), Output: known(2)}},
		{name: "total below complete parents", snapshot: usage.Snapshot{Total: known(4), Input: known(3), Output: known(2)}},
		{name: "cache sum overflow", snapshot: usage.Snapshot{CacheRead: known(math.MaxInt64), CacheWrite: known(1)}},
		{name: "parent sum overflow", snapshot: usage.Snapshot{Input: known(math.MaxInt64), Output: known(1)}},
		{name: "partial sum overflow despite total", snapshot: usage.Snapshot{Total: known(math.MaxInt64), CacheRead: known(math.MaxInt64), Reasoning: known(1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := usage.State{Usage: usage.Snapshot{Total: known(3)}, Charged: 3}

			next, delta, err := usage.Advance(previous, tt.snapshot)

			if err == nil || next != previous || delta != 0 {
				t.Errorf("Advance() = (%+v, %d, %v), want previous state, zero charge and error", next, delta, err)
			}
		})
	}
}

func TestKnownCountersCannotDecreaseOrDisappear(t *testing.T) {
	tests := []struct {
		name     string
		previous usage.Snapshot
		next     usage.Snapshot
	}{
		{name: "input decreases", previous: usage.Snapshot{Input: known(5)}, next: usage.Snapshot{Input: known(4)}},
		{name: "output decreases", previous: usage.Snapshot{Output: known(5)}, next: usage.Snapshot{Output: known(4)}},
		{name: "total decreases", previous: usage.Snapshot{Total: known(5)}, next: usage.Snapshot{Total: known(4)}},
		{name: "cache read decreases", previous: usage.Snapshot{CacheRead: known(5)}, next: usage.Snapshot{CacheRead: known(4)}},
		{name: "cache write decreases", previous: usage.Snapshot{CacheWrite: known(5)}, next: usage.Snapshot{CacheWrite: known(4)}},
		{name: "reasoning decreases", previous: usage.Snapshot{Reasoning: known(5)}, next: usage.Snapshot{Reasoning: known(4)}},
		{name: "known zero input disappears", previous: usage.Snapshot{Input: known(0)}},
		{name: "known zero output disappears", previous: usage.Snapshot{Output: known(0)}},
		{name: "cache read disappears", previous: usage.Snapshot{CacheRead: known(0)}},
		{name: "cache write disappears", previous: usage.Snapshot{CacheWrite: known(0)}},
		{name: "reasoning disappears", previous: usage.Snapshot{Reasoning: known(0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous, _, err := usage.Advance(usage.State{}, tt.previous)
			if err != nil {
				t.Fatal(err)
			}

			next, delta, err := usage.Advance(previous, tt.next)

			if err == nil || next != previous || delta != 0 {
				t.Errorf("Advance() = (%+v, %d, %v), want previous state, zero charge and error", next, delta, err)
			}
		})
	}
}

func TestInvalidPreviousStateIsRejected(t *testing.T) {
	tests := []struct {
		name     string
		previous usage.State
	}{
		{name: "negative charge", previous: usage.State{Charged: -1}},
		{name: "invalid snapshot", previous: usage.State{Usage: usage.Snapshot{Reasoning: known(-1)}}},
		{name: "charge below partial amount", previous: usage.State{Usage: usage.Snapshot{Input: known(5)}, Charged: 4}},
		{name: "charge above complete amount", previous: usage.State{Usage: usage.Snapshot{Total: known(5)}, Charged: 6}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := usage.Snapshot{Input: known(10), Output: known(10), Total: known(20)}

			next, delta, err := usage.Advance(tt.previous, snapshot)

			if err == nil || next != tt.previous || delta != 0 {
				t.Errorf("Advance() = (%+v, %d, %v), want previous state, zero charge and error", next, delta, err)
			}
		})
	}
}

func known(tokens int64) usage.Counter {
	return usage.Counter{Tokens: tokens, Known: true}
}
