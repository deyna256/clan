package selection_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/selection"
)

func TestRoundRobinRotation(t *testing.T) {
	tests := []struct {
		candidates []account.ID
		want       []account.ID
	}{
		{candidates: []account.ID{"a", "b", "c"}, want: []account.ID{"a", "b", "c", "a"}},
		{candidates: []account.ID{"a", "c", "b"}, want: []account.ID{"a", "b", "c", "a"}},
		{candidates: []account.ID{"b", "a", "c"}, want: []account.ID{"a", "b", "c", "a"}},
		{candidates: []account.ID{"b", "c", "a"}, want: []account.ID{"a", "b", "c", "a"}},
		{candidates: []account.ID{"c", "a", "b"}, want: []account.ID{"a", "b", "c", "a"}},
		{candidates: []account.ID{"c", "b", "a"}, want: []account.ID{"a", "b", "c", "a"}},
		{candidates: []account.ID{"b"}, want: []account.ID{"b", "b"}},
		{candidates: []account.ID{"2", "10", "1"}, want: []account.ID{"1", "10", "2", "1"}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.candidates), func(t *testing.T) {
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}

			for i, want := range tt.want {
				got, ok := selector.Select(scope, tt.candidates)

				if !ok || got != want {
					t.Errorf("Select() call %d = (%q, %t), want (%q, true)", i+1, got, ok, want)
				}
				// Reordering candidates must not change the rotation.
				slices.Reverse(tt.candidates)
			}
		})
	}
}

func TestRoundRobinCandidateChanges(t *testing.T) {
	tests := []struct {
		name       string
		previous   []account.ID
		candidates []account.ID
		want       account.ID
	}{
		{
			name:       "removed last choice preserves position",
			previous:   []account.ID{"b"},
			candidates: []account.ID{"d", "a", "c"},
			want:       "c",
		},
		{
			name:       "wrap after removing last choice",
			previous:   []account.ID{"d"},
			candidates: []account.ID{"c", "a", "b"},
			want:       "a",
		},
		{
			name:       "new candidate becomes next",
			previous:   []account.ID{"c", "a"},
			candidates: []account.ID{"c", "b", "a"},
			want:       "b",
		},
		{
			name:       "new candidates on both sides of current position",
			previous:   []account.ID{"c"},
			candidates: []account.ID{"b", "d", "c"},
			want:       "d",
		},
		{
			name:       "only eligible candidate is selected",
			previous:   []account.ID{"a", "b"},
			candidates: []account.ID{"c"},
			want:       "c",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: establish the previous position through the public API.
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
			selector.Select(scope, tt.previous)

			got, ok := selector.Select(scope, tt.candidates)

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
	selector.Select(scope, []account.ID{"c", "a"})
	selector.Select(scope, []account.ID{"c"})

	// Act: a returns to the candidate set.
	got, ok := selector.Select(scope, []account.ID{"c", "a", "b"})

	if !ok || got != "a" {
		t.Fatalf("choice after account returns = (%q, %t), want (a, true)", got, ok)
	}
}

func TestRoundRobinEmptyInputPreservesPosition(t *testing.T) {
	tests := []struct {
		name     string
		previous []account.ID
		empty    []account.ID
		wantNext account.ID
	}{
		{name: "nil on fresh selector", wantNext: "a"},
		{name: "empty on fresh selector", empty: []account.ID{}, wantNext: "a"},
		{name: "nil after selection", previous: []account.ID{"b"}, wantNext: "c"},
		{name: "empty after selection", previous: []account.ID{"b"}, empty: []account.ID{}, wantNext: "c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
			selector.Select(scope, tt.previous)

			// Act: the next nonempty call makes position preservation observable.
			got, ok := selector.Select(scope, tt.empty)
			next, nextOK := selector.Select(scope, []account.ID{"c", "a", "b"})

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
	selector := selection.NewRoundRobin()
	first := selection.Scope{UpstreamID: "first", Model: "model"}
	otherUpstream := selection.Scope{UpstreamID: "second", Model: "model"}
	otherModel := selection.Scope{UpstreamID: "first", Model: "other"}
	candidates := []account.ID{"b", "a", "c"}
	calls := []struct {
		scope selection.Scope
		want  account.ID
	}{
		{scope: first, want: "a"},
		{scope: first, want: "b"},
		{scope: otherUpstream, want: "a"},
		{scope: otherModel, want: "a"},
		{scope: first, want: "c"},
		{scope: otherUpstream, want: "b"},
		{scope: otherModel, want: "b"},
	}

	for i, call := range calls {
		got, ok := selector.Select(call.scope, candidates)

		if !ok || got != call.want {
			t.Errorf("Select(%+v) call %d = (%q, %t), want (%q, true)",
				call.scope, i+1, got, ok, call.want)
		}
	}
}

func TestRoundRobinIndependentInstances(t *testing.T) {
	first := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []account.ID{"b", "a", "c"}
	first.Select(scope, []account.ID{"b"})

	second := selection.NewRoundRobin()
	newID, newOK := second.Select(scope, candidates)
	existingID, existingOK := first.Select(scope, candidates)

	if !newOK || newID != "a" {
		t.Errorf("new instance choice = (%q, %t), want (a, true)", newID, newOK)
	}
	if !existingOK || existingID != "c" {
		t.Errorf("existing instance choice = (%q, %t), want (c, true)", existingID, existingOK)
	}
}

func TestRoundRobinLeavesCandidatesUnchanged(t *testing.T) {
	for _, previous := range [][]account.ID{nil, {"a"}} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			// Arrange: check both the first and subsequent selections.
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
			selector.Select(scope, previous)
			candidates := []account.ID{"c", "a", "b"}
			want := slices.Clone(candidates)

			selector.Select(scope, candidates)

			if !slices.Equal(candidates, want) {
				t.Fatalf("candidates changed: got %v, want %v", candidates, want)
			}
		})
	}
}

func TestRoundRobinCallerCanReuseCandidates(t *testing.T) {
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []account.ID{"c", "a", "b"}
	selector.Select(scope, candidates)
	for i := range candidates {
		candidates[i] = "z"
	}

	got, ok := selector.Select(scope, []account.ID{"c", "a", "b"})

	if !ok || got != "b" {
		t.Fatalf("choice after reusing input = (%q, %t), want (b, true)", got, ok)
	}
}

func TestRoundRobinConcurrentSelection(t *testing.T) {
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []account.ID{"c", "a", "b"}
	const calls = 300
	results := make([]struct {
		id account.ID
		ok bool
	}, calls)
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

	counts := make(map[account.ID]int)
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
