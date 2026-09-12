package admission_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/admission"
	"github.com/deyna256/clan/internal/budget"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/ratelimit"
	"github.com/deyna256/clan/internal/usage"
)

func TestRejectedAdmissionHasNoSideEffects(t *testing.T) {
	zero, negative := int64(0), int64(-1)
	for _, tc := range []struct {
		name        string
		budgets     budget.Limits
		concurrency int
		rpm         *int
		denied      bool
		cancelled   bool
		want        error
	}{
		{name: "access", concurrency: 1, rpm: new(1), denied: true, want: admission.ErrDenied},
		{name: "budget", budgets: budget.Limits{FiveHours: &zero}, concurrency: 1, rpm: new(1), want: budget.ErrExhausted},
		{name: "invalid budget", budgets: budget.Limits{SevenDays: &negative}, concurrency: 1, rpm: new(1), want: admission.ErrUnavailable},
		{name: "concurrency", concurrency: 0, rpm: new(1), want: concurrency.ErrLimitReached},
		{name: "RPM", concurrency: 1, rpm: new(0), want: ratelimit.ErrLimitReached},
		{name: "unconfigured RPM", concurrency: 1, want: ratelimit.ErrNotConfigured},
		{name: "cancelled", concurrency: 1, rpm: new(1), cancelled: true, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newStore()
				c, rpm := coordinator(t, store)
				if tc.rpm == nil {
					rpm.Remove("key")
				} else if err := rpm.Configure("key", ratelimit.Config{RPM: *tc.rpm}); err != nil {
					t.Fatal(err)
				}
				p := policy(t, "key")
				p.Budgets, p.Concurrency = tc.budgets, tc.concurrency
				if tc.denied {
					key, err := accesskey.New(p.Key.Identity(), true, accesskey.Permissions{})
					if err != nil {
						t.Fatal(err)
					}
					p.Key = key
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.cancelled {
					cancel()
				}

				request, attempt, err := c.Start(ctx, p, model(), candidate())

				if !errors.Is(err, tc.want) || request != (admission.Request{}) || attempt != (admission.Attempt{}) {
					t.Fatalf("Start = %v, %v, %v; want zero handles and %v", request, attempt, err, tc.want)
				}
				if tc.denied && store.loadCount("key") != 0 {
					t.Fatal("permission denial loaded budget state")
				}
				if err := c.Flush(t.Context()); err != nil || store.saveCount() != 0 {
					t.Fatalf("rejected request left pending writes: saves=%d err=%v", store.saveCount(), err)
				}
				if tc.rpm != nil && *tc.rpm == 1 {
					if err := rpm.TryAcquire("key"); err != nil {
						t.Fatalf("rejection consumed RPM: %v", err)
					}
				}
				if err := rpm.Configure("key", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Second)
				opened := time.Now()
				request, _, err = c.Start(t.Context(), policy(t, "key"), model(), candidate())
				if err != nil {
					t.Fatalf("subsequent admission leaked a slot: %v", err)
				}
				defer request.Release()
				if err := c.Flush(t.Context()); err != nil {
					t.Fatal(err)
				}
				assertSaved(t, store, "key", budget.State{
					FiveHours: budget.Window{OpenedAt: opened}, SevenDays: budget.Window{OpenedAt: opened},
				})
			})
		})
	}
}

func TestRetriesAndLateUsageShareRequestLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		c, rpm := coordinator(t, store)
		if err := rpm.Configure("key", ratelimit.Config{RPM: 1}); err != nil {
			t.Fatal(err)
		}
		p := policy(t, "key")
		opened := time.Now()
		request, first := start(t, c, p)
		requestCopy := request
		firstCopy := first
		observe(t, first, 100)

		cap := int64(100)
		p.Budgets.FiveHours = &cap
		if _, err := request.NextAttempt(t.Context(), p, candidate()); !errors.Is(err, budget.ErrExhausted) {
			t.Fatalf("retry ignored freshly exhausted budget: %v", err)
		}
		p.Budgets = budget.Limits{}
		revoked := p
		var err error
		revoked.Key, err = accesskey.New(p.Key.Identity(), false, accesskey.Permissions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := request.NextAttempt(t.Context(), revoked, candidate()); !errors.Is(err, admission.ErrDenied) {
			t.Fatalf("retry ignored revoked access: %v", err)
		}
		if _, err := request.NextAttempt(t.Context(), policy(t, "other"), candidate()); !errors.Is(err, admission.ErrDenied) {
			t.Fatalf("changed key = %v; want ErrDenied", err)
		}
		otherUpstream := candidate()
		otherUpstream.UpstreamID = "other"
		if _, err := request.NextAttempt(t.Context(), p, otherUpstream); !errors.Is(err, admission.ErrDenied) {
			t.Fatalf("changed upstream = %v; want ErrDenied", err)
		}
		p.Concurrency = 0 // Retries retain the original slot, despite a changed cap.
		second, err := request.NextAttempt(t.Context(), p, candidate())
		if err != nil {
			t.Fatalf("retry reacquired RPM or concurrency: %v", err)
		}
		observe(t, second, 50)
		observe(t, firstCopy, 150)
		requestCopy.Release()
		observe(t, first, 200)
		if _, err := request.NextAttempt(t.Context(), p, candidate()); !errors.Is(err, admission.ErrReleased) {
			t.Fatalf("retry after releasing copy = %v; want ErrReleased", err)
		}
		request.Release()
		if _, _, err := c.Start(t.Context(), policy(t, "key"), model(), candidate()); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("second request = %v; want consumed RPM", err)
		}
		if err := c.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertSaved(t, store, "key", budget.State{
			FiveHours: budget.Window{OpenedAt: opened, Used: 250},
			SevenDays: budget.Window{OpenedAt: opened, Used: 250},
		})
	})
}

func TestUsageCrossesExpiryWithoutRecharging(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		c, _ := coordinator(t, store)
		opened := time.Now()
		request, attempt := start(t, c, policy(t, "key"))
		observe(t, attempt, 100)
		request.Release()

		time.Sleep(5 * time.Hour)
		observe(t, attempt, 150)
		observe(t, attempt, 150)
		cap := int64(50)
		p := policy(t, "key")
		p.Budgets.FiveHours = &cap
		if _, _, err := c.Start(t.Context(), p, model(), candidate()); !errors.Is(err, budget.ErrExhausted) {
			t.Fatalf("late usage did not exhaust successor: %v", err)
		}
		if err := c.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertSaved(t, store, "key", budget.State{
			FiveHours: budget.Window{OpenedAt: opened.Add(5 * time.Hour), Used: 50},
			SevenDays: budget.Window{OpenedAt: opened, Used: 150},
		})
	})
}

func TestObserveCommitsUsageAndBudgetTogether(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		opened := time.Now()
		store.states["key"] = budget.State{
			FiveHours: budget.Window{OpenedAt: opened, Used: 7},
			SevenDays: budget.Window{OpenedAt: opened.Add(-168*time.Hour + time.Nanosecond), Used: math.MaxInt64},
		}
		c, _ := coordinator(t, store)
		request, attempt := start(t, c, policy(t, "key"))
		defer request.Release()

		previous, err := attempt.Observe(total(1))
		if err == nil || previous != (usage.State{}) {
			t.Fatalf("overflow = %+v, %v; want unchanged zero usage and error", previous, err)
		}
		time.Sleep(time.Nanosecond)
		observe(t, attempt, 1)
		invalid := total(-1)
		previous, err = attempt.Observe(invalid)
		if err == nil || previous.Charged != 1 || previous.Usage != total(1) {
			t.Fatalf("invalid usage = %+v, %v; want previous state charged for 1 token and error", previous, err)
		}
		if err := c.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertSaved(t, store, "key", budget.State{
			FiveHours: budget.Window{OpenedAt: opened, Used: 8},
			SevenDays: budget.Window{OpenedAt: opened.Add(time.Nanosecond), Used: 1},
		})
	})
}

func TestConcurrentRequestsCannotOveradmitOrLoseUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		c, _ := coordinator(t, store)
		p := policy(t, "key")
		type result struct {
			request admission.Request
			err     error
		}
		run := func(p admission.Policy) result {
			request, attempt, err := c.Start(t.Context(), p, model(), candidate())
			if err == nil {
				_, err = attempt.Observe(total(10))
			}
			return result{request: request, err: err}
		}
		var group sync.WaitGroup
		results := make(chan result, 20)
		for range 20 {
			group.Go(func() {
				results <- run(p)
			})
		}
		group.Wait()
		admitted := 0
		for range 20 {
			got := <-results
			got.request.Release()
			if got.err == nil {
				admitted++
			} else if !errors.Is(got.err, concurrency.ErrLimitReached) {
				t.Errorf("concurrent request: %v", got.err)
			}
		}
		if admitted != 1 || store.loadCount("key") != 1 {
			t.Fatalf("admissions=%d loads=%d; want one each", admitted, store.loadCount("key"))
		}
		p.Concurrency = concurrency.Unlimited
		for range 20 {
			group.Go(func() {
				results <- run(p)
			})
		}
		group.Wait()
		for range 20 {
			got := <-results
			got.request.Release()
			if got.err != nil {
				t.Errorf("concurrent uncapped request: %v", got.err)
			}
		}
		if err := c.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := store.saved("key")
		if got.FiveHours.Used != 210 || got.SevenDays.Used != 210 {
			t.Fatalf("concurrent usage = %+v; want 210 in both windows", got)
		}
	})
}

func TestNewRequiresDependencies(t *testing.T) {
	if _, err := admission.New(nil, ratelimit.New()); err == nil {
		t.Fatal("New accepted nil store")
	}
	if _, err := admission.New(newStore(), nil); err == nil {
		t.Fatal("New accepted nil RPM limiter")
	}
}

func TestZeroHandles(t *testing.T) {
	var request admission.Request
	request.Release()
	if _, err := request.NextAttempt(t.Context(), admission.Policy{}, account.Identity{}); !errors.Is(err, admission.ErrReleased) {
		t.Fatalf("zero request = %v; want ErrReleased", err)
	}
	var attempt admission.Attempt
	if _, err := attempt.Observe(total(1)); err == nil {
		t.Fatal("zero attempt accepted usage")
	}
}

func TestCancelledFlushWithoutPendingWrites(t *testing.T) {
	c, _ := coordinator(t, newStore())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled empty Flush = %v; want context.Canceled", err)
	}
}

type testStore struct {
	mu       sync.Mutex
	states   map[accesskey.ID]budget.State
	loads    map[accesskey.ID]int
	saves    int
	loadHook func(context.Context, accesskey.ID) error
	saveHook func(context.Context, accesskey.ID, budget.State) error
}

func newStore() *testStore {
	return &testStore{states: make(map[accesskey.ID]budget.State), loads: make(map[accesskey.ID]int)}
}

func (s *testStore) Load(ctx context.Context, id accesskey.ID) (budget.State, bool, error) {
	s.mu.Lock()
	s.loads[id]++
	hook := s.loadHook
	s.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, id); err != nil {
			return budget.State{}, false, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, found := s.states[id]
	return state, found, nil
}

func (s *testStore) Save(ctx context.Context, id accesskey.ID, state budget.State) error {
	s.mu.Lock()
	s.saves++
	hook := s.saveHook
	s.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, id, state); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[id] = state
	return nil
}

func (s *testStore) saved(id accesskey.ID) budget.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[id]
}

func (s *testStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func (s *testStore) loadCount(id accesskey.ID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads[id]
}

func coordinator(t *testing.T, store *testStore) (*admission.Coordinator, *ratelimit.Limiter) {
	t.Helper()
	rpm := ratelimit.New()
	if err := rpm.Configure("key", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
		t.Fatal(err)
	}
	c, err := admission.New(store, rpm)
	if err != nil {
		t.Fatal(err)
	}
	return c, rpm
}

func policy(t *testing.T, id accesskey.ID) admission.Policy {
	t.Helper()
	key, err := accesskey.New(accesskey.Identity{ID: id, Name: "test key"}, true,
		accesskey.Permissions{AllUpstreams: true, AllModels: true})
	if err != nil {
		t.Fatal(err)
	}
	return admission.Policy{Key: key, Concurrency: 1}
}

func model() accesskey.Model {
	return accesskey.Model{UpstreamID: "upstream", Name: "model"}
}

func candidate() account.Identity {
	return account.Identity{ID: "account", UpstreamID: "upstream", Name: "test account"}
}

func start(t *testing.T, c *admission.Coordinator, p admission.Policy) (admission.Request, admission.Attempt) {
	t.Helper()
	request, attempt, err := c.Start(t.Context(), p, model(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	return request, attempt
}

func total(tokens int64) usage.Snapshot {
	return usage.Snapshot{Total: usage.Counter{Known: true, Tokens: tokens}}
}

func observe(t *testing.T, attempt admission.Attempt, tokens int64) {
	t.Helper()
	state, err := attempt.Observe(total(tokens))
	if err != nil || state.Charged != tokens {
		t.Fatalf("Observe(%d) = %+v, %v; want charged %d", tokens, state, err, tokens)
	}
}

func assertSaved(t *testing.T, store *testStore, id accesskey.ID, want budget.State) {
	t.Helper()
	got := store.saved(id)
	if got.FiveHours.Used != want.FiveHours.Used || got.SevenDays.Used != want.SevenDays.Used ||
		!got.FiveHours.OpenedAt.Equal(want.FiveHours.OpenedAt) || !got.SevenDays.OpenedAt.Equal(want.SevenDays.OpenedAt) {
		t.Fatalf("saved %q = %+v; want %+v", id, got, want)
	}
}
