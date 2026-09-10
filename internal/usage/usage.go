// Package usage counts normalized cumulative token usage for one attempt.
package usage

import (
	"fmt"
	"math"
)

// Counter distinguishes observed zero tokens from an unknown count.
// Tokens must be nonnegative, and must be zero when Known is false.
type Counter struct {
	Tokens int64
	Known  bool
}

// Snapshot contains cumulative counters, after an adapter merges provider patches.
// Input includes the disjoint CacheRead and CacheWrite subsets; Output includes
// Reasoning. Known breakdowns must be retained. A stale Total may become unknown.
type Snapshot struct {
	Input, Output, Total             Counter
	CacheRead, CacheWrite, Reasoning Counter
}

// Complete reports whether the total is known, directly or through both parents.
// It describes coverage of token counts, not completion of the attempt or stream.
func (s Snapshot) Complete() bool {
	return s.Total.Known || (s.Input.Known && s.Output.Known)
}

// State retains an attempt's latest snapshot and cumulative accounted tokens.
// Charged can exceed the snapshot's partial known amount after a total goes stale.
// The zero value starts an attempt. Callers own persistence and synchronization.
type State struct {
	Usage   Snapshot
	Charged int64
}

// Advance returns the next state and newly chargeable tokens for the same attempt.
// It validates both inputs without modifying them. Invalid or decreasing counters
// return the previous state, zero charge and an error. Repeated snapshots charge
// nothing; separate attempts need separate states. Callers must apply the next
// state and its budget charge together.
func Advance(previous State, snapshot Snapshot) (State, int64, error) {
	previousAmount, err := previous.Usage.amount()
	if err != nil {
		return previous, 0, fmt.Errorf("usage: previous snapshot: %w", err)
	}
	if previous.Charged < previousAmount || (previous.Usage.Complete() && previous.Charged != previousAmount) {
		return previous, 0, fmt.Errorf("usage: previous charge does not match known usage")
	}
	amount, err := snapshot.amount()
	if err != nil {
		return previous, 0, fmt.Errorf("usage: snapshot: %w", err)
	}
	for _, field := range []struct {
		name     string
		previous Counter
		next     Counter
	}{
		{name: "input", previous: previous.Usage.Input, next: snapshot.Input},
		{name: "output", previous: previous.Usage.Output, next: snapshot.Output},
		{name: "cache read", previous: previous.Usage.CacheRead, next: snapshot.CacheRead},
		{name: "cache write", previous: previous.Usage.CacheWrite, next: snapshot.CacheWrite},
		{name: "reasoning", previous: previous.Usage.Reasoning, next: snapshot.Reasoning},
	} {
		if field.previous.Known && (!field.next.Known || field.next.Tokens < field.previous.Tokens) {
			return previous, 0, fmt.Errorf("usage: %s counter disappeared or decreased", field.name)
		}
	}
	if snapshot.Complete() && amount < previous.Charged {
		return previous, 0, fmt.Errorf("usage: complete total is below previously charged usage")
	}
	charged := max(previous.Charged, amount)
	return State{Usage: snapshot, Charged: charged}, charged - previous.Charged, nil
}

func (s Snapshot) amount() (int64, error) {
	for _, field := range []struct {
		name    string
		counter Counter
	}{
		{name: "input", counter: s.Input},
		{name: "output", counter: s.Output},
		{name: "total", counter: s.Total},
		{name: "cache read", counter: s.CacheRead},
		{name: "cache write", counter: s.CacheWrite},
		{name: "reasoning", counter: s.Reasoning},
	} {
		if field.counter.Tokens < 0 || (!field.counter.Known && field.counter.Tokens != 0) {
			return 0, fmt.Errorf("%s counter must be nonnegative and unknown counters must be zero", field.name)
		}
	}
	cache, err := add(s.CacheRead.Tokens, s.CacheWrite.Tokens)
	if err != nil {
		return 0, fmt.Errorf("cache counters: %w", err)
	}
	input := cache
	if s.Input.Known {
		if cache > s.Input.Tokens {
			return 0, fmt.Errorf("cache counters exceed input")
		}
		input = s.Input.Tokens
	}
	output := s.Reasoning.Tokens
	if s.Output.Known {
		if output > s.Output.Tokens {
			return 0, fmt.Errorf("reasoning counter exceeds output")
		}
		output = s.Output.Tokens
	}
	amount, err := add(input, output)
	if err != nil {
		return 0, fmt.Errorf("input and output counters: %w", err)
	}
	if s.Total.Known {
		if s.Total.Tokens < amount || (s.Input.Known && s.Output.Known && s.Total.Tokens != amount) {
			return 0, fmt.Errorf("total counter is inconsistent with input and output")
		}
		return s.Total.Tokens, nil
	}
	return amount, nil
}

func add(a, b int64) (int64, error) {
	if a > math.MaxInt64-b {
		return 0, fmt.Errorf("token count exceeds int64")
	}
	return a + b, nil
}
