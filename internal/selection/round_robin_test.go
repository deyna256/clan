package selection_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/deyna256/clan/internal/selection"
)

type selectionResult struct {
	id selection.AccountID
	ok bool
}

func TestRoundRobinRotation(t *testing.T) {
	tests := []struct {
		candidates []selection.AccountID
		want       []selection.AccountID
	}{
		{candidates: []selection.AccountID{"a", "b", "c"}, want: []selection.AccountID{"a", "b", "c", "a"}},
		{candidates: []selection.AccountID{"a", "c", "b"}, want: []selection.AccountID{"a", "b", "c", "a"}},
		{candidates: []selection.AccountID{"b", "a", "c"}, want: []selection.AccountID{"a", "b", "c", "a"}},
		{candidates: []selection.AccountID{"b", "c", "a"}, want: []selection.AccountID{"a", "b", "c", "a"}},
		{candidates: []selection.AccountID{"c", "a", "b"}, want: []selection.AccountID{"a", "b", "c", "a"}},
		{candidates: []selection.AccountID{"c", "b", "a"}, want: []selection.AccountID{"a", "b", "c", "a"}},
		{candidates: []selection.AccountID{"b"}, want: []selection.AccountID{"b", "b"}},
		{candidates: []selection.AccountID{"2", "10", "1"}, want: []selection.AccountID{"1", "10", "2", "1"}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.candidates), func(t *testing.T) {
			// Arrange.
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
			results := make([]selectionResult, len(tt.want))

			// Act: run a full cycle and wrap, changing input order between calls.
			for i := range results {
				results[i].id, results[i].ok = selector.Select(scope, tt.candidates)
				slices.Reverse(tt.candidates)
			}

			// Assert.
			for i, result := range results {
				if !result.ok || result.id != tt.want[i] {
					t.Errorf("call %d = (%q, %t), want (%q, true)", i+1, result.id, result.ok, tt.want[i])
				}
			}
		})
	}
}

func TestRoundRobinCandidateChanges(t *testing.T) {
	tests := []struct {
		name       string
		previous   []selection.AccountID
		candidates []selection.AccountID
		want       selection.AccountID
	}{
		{
			name:       "removed last choice preserves position",
			previous:   []selection.AccountID{"b"},
			candidates: []selection.AccountID{"d", "a", "c"},
			want:       "c",
		},
		{
			name:       "wrap after removing last choice",
			previous:   []selection.AccountID{"d"},
			candidates: []selection.AccountID{"c", "a", "b"},
			want:       "a",
		},
		{
			name:       "new candidate becomes next",
			previous:   []selection.AccountID{"c", "a"},
			candidates: []selection.AccountID{"c", "b", "a"},
			want:       "b",
		},
		{
			name:       "new candidates on both sides of current position",
			previous:   []selection.AccountID{"c"},
			candidates: []selection.AccountID{"b", "d", "c"},
			want:       "d",
		},
		{
			name:       "only eligible candidate is selected",
			previous:   []selection.AccountID{"a", "b"},
			candidates: []selection.AccountID{"c"},
			want:       "c",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: establish the previous position through the public API.
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
			selector.Select(scope, tt.previous)

			// Act.
			got, ok := selector.Select(scope, tt.candidates)

			// Assert.
			if !ok || got != tt.want {
				t.Fatalf("Select(%v) = (%q, %t), want (%q, true)", tt.candidates, got, ok, tt.want)
			}
		})
	}
}

func TestRoundRobinReturningCandidate(t *testing.T) {
	// Arrange: select a, then remove it while selecting c.
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	selector.Select(scope, []selection.AccountID{"c", "a"})
	selector.Select(scope, []selection.AccountID{"c"})

	// Act: a returns to the candidate set.
	got, ok := selector.Select(scope, []selection.AccountID{"c", "a", "b"})

	// Assert.
	if !ok || got != "a" {
		t.Fatalf("choice after account returns = (%q, %t), want (a, true)", got, ok)
	}
}

func TestRoundRobinEmptyInputPreservesPosition(t *testing.T) {
	tests := []struct {
		name     string
		previous []selection.AccountID
		empty    []selection.AccountID
		wantNext selection.AccountID
	}{
		{name: "nil on fresh selector", wantNext: "a"},
		{name: "empty on fresh selector", empty: []selection.AccountID{}, wantNext: "a"},
		{name: "nil after selection", previous: []selection.AccountID{"b"}, wantNext: "c"},
		{name: "empty after selection", previous: []selection.AccountID{"b"}, empty: []selection.AccountID{}, wantNext: "c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange.
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
			selector.Select(scope, tt.previous)

			// Act: the next nonempty call makes position preservation observable.
			got, ok := selector.Select(scope, tt.empty)
			next, nextOK := selector.Select(scope, []selection.AccountID{"c", "a", "b"})

			// Assert.
			if got != "" || ok {
				t.Errorf("empty selection = (%q, %t), want (\"\", false)", got, ok)
			}
			if !nextOK || next != tt.wantNext {
				t.Errorf("choice after empty input = (%q, %t), want (%q, true)", next, nextOK, tt.wantNext)
			}
		})
	}
}

func TestRoundRobinIndependentScopes(t *testing.T) {
	// Arrange.
	selector := selection.NewRoundRobin()
	first := selection.Scope{UpstreamID: "first", Model: "model"}
	otherUpstream := selection.Scope{UpstreamID: "second", Model: "model"}
	otherModel := selection.Scope{UpstreamID: "first", Model: "other"}
	candidates := []selection.AccountID{"b", "a", "c"}
	calls := []struct {
		scope selection.Scope
		want  selection.AccountID
	}{
		{scope: first, want: "a"},
		{scope: first, want: "b"},
		{scope: otherUpstream, want: "a"},
		{scope: otherModel, want: "a"},
		{scope: first, want: "c"},
		{scope: otherUpstream, want: "b"},
		{scope: otherModel, want: "b"},
	}
	results := make([]selectionResult, len(calls))

	// Act.
	for i, call := range calls {
		results[i].id, results[i].ok = selector.Select(call.scope, candidates)
	}

	// Assert.
	for i, result := range results {
		if !result.ok || result.id != calls[i].want {
			t.Errorf("call %d for %+v = (%q, %t), want (%q, true)",
				i+1, calls[i].scope, result.id, result.ok, calls[i].want)
		}
	}
}

func TestRoundRobinIndependentInstances(t *testing.T) {
	// Arrange.
	first := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []selection.AccountID{"b", "a", "c"}
	first.Select(scope, []selection.AccountID{"b"})

	// Act.
	second := selection.NewRoundRobin()
	newID, newOK := second.Select(scope, candidates)
	existingID, existingOK := first.Select(scope, candidates)

	// Assert.
	if !newOK || newID != "a" {
		t.Errorf("new instance choice = (%q, %t), want (a, true)", newID, newOK)
	}
	if !existingOK || existingID != "c" {
		t.Errorf("existing instance choice = (%q, %t), want (c, true)", existingID, existingOK)
	}
}

func TestRoundRobinLeavesCandidatesUnchanged(t *testing.T) {
	for _, previous := range [][]selection.AccountID{nil, {"a"}} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			// Arrange: check both the first and subsequent selections.
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
			selector.Select(scope, previous)
			candidates := []selection.AccountID{"c", "a", "b"}
			want := slices.Clone(candidates)

			// Act.
			selector.Select(scope, candidates)

			// Assert.
			if !slices.Equal(candidates, want) {
				t.Fatalf("candidates changed: got %v, want %v", candidates, want)
			}
		})
	}
}

func TestRoundRobinCallerCanReuseCandidates(t *testing.T) {
	// Arrange.
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []selection.AccountID{"c", "a", "b"}
	selector.Select(scope, candidates)
	for i := range candidates {
		candidates[i] = "z"
	}

	// Act.
	got, ok := selector.Select(scope, []selection.AccountID{"c", "a", "b"})

	// Assert.
	if !ok || got != "b" {
		t.Fatalf("choice after reusing input = (%q, %t), want (b, true)", got, ok)
	}
}

func TestRoundRobinConcurrentSelection(t *testing.T) {
	// Arrange.
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []selection.AccountID{"c", "a", "b"}
	const calls = 300
	results := make([]selectionResult, calls)
	start := make(chan struct{})
	var workers sync.WaitGroup

	// Act: each goroutine writes to its own result slot.
	for i := range results {
		workers.Go(func() {
			<-start
			results[i].id, results[i].ok = selector.Select(scope, candidates)
		})
	}
	close(start)
	workers.Wait()
	next, nextOK := selector.Select(scope, candidates)

	// Assert.
	counts := make(map[selection.AccountID]int)
	for i, result := range results {
		if !result.ok || !slices.Contains(candidates, result.id) {
			t.Fatalf("call %d = (%q, %t), want a candidate and true", i+1, result.id, result.ok)
		}
		counts[result.id]++
	}
	for _, id := range candidates {
		if counts[id] != 100 {
			t.Errorf("account %q selected %d times, want 100", id, counts[id])
		}
	}
	if !nextOK || next != "a" {
		t.Errorf("choice after concurrent calls = (%q, %t), want (a, true)", next, nextOK)
	}
}
