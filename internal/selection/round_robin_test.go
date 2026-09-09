package selection_test

import (
	"slices"
	"sync"
	"testing"

	"github.com/deyna256/clan/internal/selection"
)

func TestRoundRobinRotation(t *testing.T) {
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []selection.AccountID{"c", "a", "b"}

	for i, want := range []selection.AccountID{"a", "b", "c", "a"} {
		got, ok := selector.Select(scope, candidates)
		if !ok || got != want {
			t.Fatalf("call %d: Select(%v) = (%q, %t), want (%q, true)", i+1, candidates, got, ok, want)
		}
	}
}

func TestRoundRobinCandidateChanges(t *testing.T) {
	type step struct {
		candidates []selection.AccountID
		want       selection.AccountID
		ok         bool
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "empty before first choice",
			steps: []step{
				{nil, "", false},
				{[]selection.AccountID{}, "", false},
				{[]selection.AccountID{"b", "a"}, "a", true},
			},
		},
		{
			name: "empty preserves position",
			steps: []step{
				{[]selection.AccountID{"b"}, "b", true},
				{nil, "", false},
				{[]selection.AccountID{}, "", false},
				{[]selection.AccountID{"a", "b", "c"}, "c", true},
			},
		},
		{
			name: "single candidate repeats",
			steps: []step{
				{[]selection.AccountID{"b"}, "b", true},
				{[]selection.AccountID{"b"}, "b", true},
			},
		},
		{
			name: "removed last choice preserves position",
			steps: []step{
				{[]selection.AccountID{"b"}, "b", true},
				{[]selection.AccountID{"d", "a", "c"}, "c", true},
			},
		},
		{
			name: "wrap after removing last choice",
			steps: []step{
				{[]selection.AccountID{"d"}, "d", true},
				{[]selection.AccountID{"c", "a", "b"}, "a", true},
			},
		},
		{
			name: "new candidate becomes next",
			steps: []step{
				{[]selection.AccountID{"c", "a"}, "a", true},
				{[]selection.AccountID{"c", "b", "a"}, "b", true},
			},
		},
		{
			name: "new and returning candidates follow ID order",
			steps: []step{
				{[]selection.AccountID{"c", "a"}, "a", true},
				{[]selection.AccountID{"c"}, "c", true},
				{[]selection.AccountID{"b", "d", "c"}, "d", true},
				{[]selection.AccountID{"c", "b", "a", "d"}, "a", true},
				{[]selection.AccountID{"d", "a", "c", "b"}, "b", true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := selection.NewRoundRobin()
			scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
			for i, step := range tt.steps {
				got, ok := selector.Select(scope, step.candidates)
				if got != step.want || ok != step.ok {
					t.Fatalf("step %d: Select(%v) = (%q, %t), want (%q, %t)",
						i+1, step.candidates, got, ok, step.want, step.ok)
				}
			}
		})
	}
}

func TestRoundRobinIndependentScopes(t *testing.T) {
	selector := selection.NewRoundRobin()
	first := selection.Scope{UpstreamID: "first", Model: "model"}
	otherUpstream := selection.Scope{UpstreamID: "second", Model: "model"}
	otherModel := selection.Scope{UpstreamID: "first", Model: "other"}
	candidates := []selection.AccountID{"b", "a", "c"}
	steps := []struct {
		scope selection.Scope
		want  selection.AccountID
	}{
		{first, "a"},
		{first, "b"},
		{otherUpstream, "a"},
		{otherModel, "a"},
		{first, "c"},
		{otherUpstream, "b"},
		{otherModel, "b"},
	}
	for _, step := range steps {
		got, ok := selector.Select(step.scope, candidates)
		if !ok || got != step.want {
			t.Fatalf("Select(%+v) = (%q, %t), want (%q, true)", step.scope, got, ok, step.want)
		}
	}
}

func TestRoundRobinIndependentInstances(t *testing.T) {
	first := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []selection.AccountID{"b", "a", "c"}
	for _, want := range []selection.AccountID{"a", "b"} {
		if got, ok := first.Select(scope, candidates); !ok || got != want {
			t.Fatalf("existing instance choice = (%q, %t), want (%q, true)", got, ok, want)
		}
	}
	second := selection.NewRoundRobin()
	if got, ok := second.Select(scope, candidates); !ok || got != "a" {
		t.Fatalf("new instance choice = (%q, %t), want (a, true)", got, ok)
	}
	if got, ok := first.Select(scope, candidates); !ok || got != "c" {
		t.Fatalf("existing instance choice = (%q, %t), want (c, true)", got, ok)
	}
}

func TestRoundRobinCandidateOwnership(t *testing.T) {
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []selection.AccountID{"c", "a", "b"}
	wantInput := slices.Clone(candidates)
	if got, ok := selector.Select(scope, candidates); !ok || got != "a" {
		t.Fatalf("first choice = (%q, %t), want (a, true)", got, ok)
	}
	if !slices.Equal(candidates, wantInput) {
		t.Fatalf("candidates changed: got %v, want %v", candidates, wantInput)
	}

	// The caller may reuse its slice after Select returns.
	for i := range candidates {
		candidates[i] = "z"
	}
	if got, ok := selector.Select(scope, wantInput); !ok || got != "b" {
		t.Fatalf("choice after reusing input = (%q, %t), want (b, true)", got, ok)
	}
}

func TestRoundRobinInputOrder(t *testing.T) {
	permutations := [][]selection.AccountID{
		{"a", "b", "c"}, {"a", "c", "b"}, {"b", "a", "c"},
		{"b", "c", "a"}, {"c", "a", "b"}, {"c", "b", "a"},
	}
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	for _, candidates := range permutations {
		t.Run(string(candidates[0])+string(candidates[1])+string(candidates[2]), func(t *testing.T) {
			selector := selection.NewRoundRobin()
			for i, want := range []selection.AccountID{"a", "b", "c", "a"} {
				got, ok := selector.Select(scope, candidates)
				if !ok || got != want {
					t.Fatalf("call %d: Select(%v) = (%q, %t), want (%q, true)", i+1, candidates, got, ok, want)
				}
				// Also change order between calls on the same selector.
				slices.Reverse(candidates)
			}
		})
	}
}

func TestRoundRobinConcurrentSelection(t *testing.T) {
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "upstream", Model: "model"}
	candidates := []selection.AccountID{"c", "a", "b"}
	const calls = 300
	type result struct {
		id selection.AccountID
		ok bool
	}
	results := make(chan result, calls)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range calls {
		workers.Go(func() {
			<-start
			id, ok := selector.Select(scope, candidates)
			results <- result{id, ok}
		})
	}
	close(start)
	workers.Wait()
	close(results)

	counts := make(map[selection.AccountID]int)
	for result := range results {
		if !result.ok || !slices.Contains(candidates, result.id) {
			t.Fatalf("concurrent choice = (%q, %t), want a candidate and true", result.id, result.ok)
		}
		counts[result.id]++
	}
	for _, id := range candidates {
		if counts[id] != 100 {
			t.Errorf("account %q selected %d times, want 100", id, counts[id])
		}
	}
	if got, ok := selector.Select(scope, candidates); !ok || got != "a" {
		t.Fatalf("choice after concurrent calls = (%q, %t), want (a, true)", got, ok)
	}
}
