package admission_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/admission"
	"github.com/deyna256/clan/internal/budget"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/ratelimit"
)

func TestInitialLoadFailureNeverStartsFromEmpty(t *testing.T) {
	for _, capped := range []bool{false, true} {
		name := "uncapped"
		if capped {
			name = "capped"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newStore()
				opened := time.Now().Add(-time.Hour)
				store.states["key"] = budget.State{
					FiveHours: budget.Window{OpenedAt: opened, Used: 40},
					SevenDays: budget.Window{OpenedAt: opened, Used: 70},
				}
				failure := errors.New("read failed")
				store.loadHook = func(context.Context, accesskey.ID) error { return failure }
				c, rpm := coordinator(t, store)
				if err := rpm.Configure("key", ratelimit.Config{RPM: 1}); err != nil {
					t.Fatal(err)
				}
				p := policy(t, "key")
				cap := int64(100)
				if capped {
					p.Budgets.FiveHours = &cap
				}

				if _, _, err := c.Start(t.Context(), p, model(), candidate()); !errors.Is(err, admission.ErrUnavailable) || !errors.Is(err, failure) {
					t.Fatalf("failed restore = %v; want unavailable read failure", err)
				}
				store.loadHook = nil
				request, attempt := start(t, c, p)
				defer request.Release()
				observe(t, attempt, 10)
				if err := c.Flush(t.Context()); err != nil {
					t.Fatal(err)
				}

				assertSaved(t, store, "key", budget.State{
					FiveHours: budget.Window{OpenedAt: opened, Used: 50},
					SevenDays: budget.Window{OpenedAt: opened, Used: 80},
				})
				if store.loadCount("key") != 2 {
					t.Fatalf("loads=%d; want failed read then restore", store.loadCount("key"))
				}
			})
		})
	}
}

func TestLoadWaitersCancelAndOtherKeysProceed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		opened := time.Now().Add(-time.Hour)
		store.states["key"] = budget.State{
			FiveHours: budget.Window{OpenedAt: opened, Used: 40}, SevenDays: budget.Window{OpenedAt: opened, Used: 40},
		}
		store.loadHook = func(ctx context.Context, id accesskey.ID) error {
			if id == "key" && store.loadCount(id) == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
		c, rpm := coordinator(t, store)
		p := policy(t, "key")
		ownerCtx, cancelOwner := context.WithCancel(t.Context())
		defer cancelOwner()
		ownerDone := make(chan error, 1)
		go func() {
			request, _, err := c.Start(ownerCtx, p, model(), candidate())
			request.Release()
			ownerDone <- err
		}()
		synctest.Wait()
		waiterCtx, cancelWaiter := context.WithCancel(t.Context())
		waiterDone, liveDone := make(chan error, 1), make(chan error, 1)
		go func() {
			request, _, err := c.Start(waiterCtx, p, model(), candidate())
			request.Release()
			waiterDone <- err
		}()
		go func() {
			request, attempt, err := c.Start(t.Context(), p, model(), candidate())
			if err == nil {
				_, err = attempt.Observe(total(10))
			}
			request.Release()
			liveDone <- err
		}()
		synctest.Wait()

		cancelWaiter()
		if err := <-waiterDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled load waiter = %v", err)
		}
		if store.loadCount("key") != 1 {
			t.Fatalf("overlapping initial loads=%d; want 1", store.loadCount("key"))
		}
		if err := rpm.Configure("other", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
			t.Fatal(err)
		}
		other, _ := start(t, c, policy(t, "other"))
		other.Release()
		cancelOwner()
		if err := <-ownerDone; !errors.Is(err, context.Canceled) || !errors.Is(err, admission.ErrUnavailable) {
			t.Fatalf("cancelled loader = %v", err)
		}
		if err := <-liveDone; err != nil {
			t.Fatalf("live waiter did not retry restoration: %v", err)
		}
		if err := c.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertSaved(t, store, "key", budget.State{
			FiveHours: budget.Window{OpenedAt: opened, Used: 50}, SevenDays: budget.Window{OpenedAt: opened, Used: 50},
		})
		if store.loadCount("key") != 2 {
			t.Fatalf("loads=%d; want cancelled owner and successful waiter", store.loadCount("key"))
		}
	})
}

func TestInvalidRestoredStateIsNotPublished(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		opening := time.Now().Add(time.Hour)
		store.states["key"] = budget.State{
			FiveHours: budget.Window{OpenedAt: opening, Used: 40},
			SevenDays: budget.Window{OpenedAt: opening, Used: 50},
		}
		c, _ := coordinator(t, store)
		if _, _, err := c.Start(t.Context(), policy(t, "key"), model(), candidate()); !errors.Is(err, admission.ErrUnavailable) {
			t.Fatalf("future restored opening = %v; want unavailable", err)
		}
		time.Sleep(time.Hour)
		request, attempt := start(t, c, policy(t, "key"))
		defer request.Release()
		observe(t, attempt, 10)
		if err := c.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertSaved(t, store, "key", budget.State{
			FiveHours: budget.Window{OpenedAt: opening, Used: 50},
			SevenDays: budget.Window{OpenedAt: opening, Used: 60},
		})
		if store.loadCount("key") != 2 {
			t.Fatalf("invalid load published state: reads=%d; want 2", store.loadCount("key"))
		}
	})
}

func TestLostAcknowledgementAndUpdatesDuringRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		c, rpm := coordinator(t, store)
		p := policy(t, "key")
		cap := int64(300)
		p.Budgets.FiveHours = &cap
		request, attempt := start(t, c, p)
		defer request.Release()
		observe(t, attempt, 100)
		lost := errors.New("lost acknowledgement")
		store.saveHook = func(_ context.Context, id accesskey.ID, state budget.State) error {
			store.mu.Lock()
			store.states[id] = state
			store.mu.Unlock()
			return lost
		}

		if err := c.Flush(t.Context()); !errors.Is(err, lost) {
			t.Fatalf("Flush = %v; want lost acknowledgement", err)
		}
		if _, err := request.NextAttempt(t.Context(), p, candidate()); !errors.Is(err, admission.ErrUnavailable) {
			t.Fatalf("failed save retry = %v; want unavailable", err)
		}
		uncapped := policy(t, "key")
		uncapped.Concurrency = concurrency.Unlimited
		another, _ := start(t, c, uncapped)
		another.Release()
		gate := make(chan struct{})
		store.saveHook = func(context.Context, accesskey.ID, budget.State) error { <-gate; return nil }
		flushDone := make(chan error, 1)
		go func() { flushDone <- c.Flush(t.Context()) }()
		synctest.Wait()
		observe(t, attempt, 150)
		if err := rpm.Configure("other", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
			t.Fatal(err)
		}
		other, otherAttempt := start(t, c, policy(t, "other"))
		observe(t, otherAttempt, 7)
		other.Release()
		waitCtx, cancel := context.WithCancel(t.Context())
		waiting := make(chan error, 1)
		go func() { waiting <- c.Flush(waitCtx) }()
		synctest.Wait()
		cancel()
		if err := <-waiting; !errors.Is(err, context.Canceled) {
			t.Fatalf("writer waiter = %v; want cancelled", err)
		}
		close(gate)
		if err := <-flushDone; err != nil {
			t.Fatal(err)
		}

		if store.saved("key").FiveHours.Used != 100 || store.saved("other") != (budget.State{}) || store.saveCount() != 2 {
			t.Fatal("Flush added usage on retry or included a key added after capture")
		}
		if _, err := request.NextAttempt(t.Context(), p, candidate()); err != nil {
			t.Fatalf("successful retry did not clear failure while newer usage pending: %v", err)
		}
		if err := c.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		if store.saved("key").FiveHours.Used != 150 || store.saved("other").FiveHours.Used != 7 {
			t.Fatal("newer or other-key usage was lost during save")
		}
		if err := c.Flush(t.Context()); err != nil || store.saveCount() != 4 {
			t.Fatalf("clean Flush repeated writes: calls=%d err=%v", store.saveCount(), err)
		}
	})
}

func TestSaveTimeoutContinuesWithOtherKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		c, rpm := coordinator(t, store)
		if err := rpm.Configure("other", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
			t.Fatal(err)
		}
		requests := make(map[accesskey.ID]admission.Request)
		for _, id := range []accesskey.ID{"key", "other"} {
			request, attempt := start(t, c, policy(t, id))
			requests[id] = request
			defer request.Release()
			observe(t, attempt, 10)
		}
		var failed accesskey.ID
		store.saveHook = func(ctx context.Context, id accesskey.ID, _ budget.State) error {
			if store.saveCount() == 1 {
				failed = id
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
		started := time.Now()

		err := c.Flush(t.Context())

		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 5*time.Second || store.saveCount() != 2 {
			t.Fatalf("Flush timeout: err=%v elapsed=%v saves=%d", err, time.Since(started), store.saveCount())
		}
		cap := int64(100)
		for id, request := range requests {
			p := policy(t, id)
			p.Budgets.FiveHours = &cap
			_, err := request.NextAttempt(t.Context(), p, candidate())
			if id == failed && !errors.Is(err, admission.ErrUnavailable) || id != failed && err != nil {
				t.Fatalf("retry for %q = %v; failed key=%q", id, err, failed)
			}
		}
		if err := c.Flush(t.Context()); err != nil || store.saveCount() != 3 {
			t.Fatalf("failed key was not retried: err=%v saves=%d", err, store.saveCount())
		}
	})
}

func TestFlushCapturesLatestStateBeforeEachSave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		c, rpm := coordinator(t, store)
		if err := rpm.Configure("other", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
			t.Fatal(err)
		}
		attempts := make(map[accesskey.ID]admission.Attempt)
		for _, id := range []accesskey.ID{"key", "other"} {
			request, attempt := start(t, c, policy(t, id))
			defer request.Release()
			observe(t, attempt, 10)
			attempts[id] = attempt
		}
		entered := make(chan accesskey.ID, 1)
		gate := make(chan struct{})
		store.saveHook = func(_ context.Context, id accesskey.ID, _ budget.State) error {
			if store.saveCount() == 1 {
				entered <- id
				<-gate
			}
			return nil
		}
		done := make(chan error, 1)
		go func() { done <- c.Flush(t.Context()) }()
		first := <-entered
		for id, attempt := range attempts {
			if id != first {
				observe(t, attempt, 20)
			}
		}
		close(gate)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		for id := range attempts {
			want := int64(20)
			if id == first {
				want = 10
			}
			if got := store.saved(id).FiveHours.Used; got != want {
				t.Fatalf("saved %q=%d; want %d from immediately before its save", id, got, want)
			}
		}
		if err := c.Flush(t.Context()); err != nil || store.saveCount() != 2 {
			t.Fatalf("already captured update remained pending: err=%v saves=%d", err, store.saveCount())
		}
	})
}

func TestCancelledFlushLeavesUnreachedKeysPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		c, rpm := coordinator(t, store)
		if err := rpm.Configure("other", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
			t.Fatal(err)
		}
		requests := make(map[accesskey.ID]admission.Request)
		for _, id := range []accesskey.ID{"key", "other"} {
			request, _ := start(t, c, policy(t, id))
			requests[id] = request
			defer request.Release()
		}
		entered := make(chan accesskey.ID, 1)
		store.saveHook = func(ctx context.Context, id accesskey.ID, _ budget.State) error {
			entered <- id
			<-ctx.Done()
			return ctx.Err()
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- c.Flush(ctx) }()
		failed := <-entered
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) || store.saveCount() != 1 {
			t.Fatalf("cancelled Flush = %v, saves=%d; want cancellation after one key", err, store.saveCount())
		}
		cap := int64(100)
		for id, request := range requests {
			p := policy(t, id)
			p.Budgets.FiveHours = &cap
			_, err := request.NextAttempt(t.Context(), p, candidate())
			if id == failed && !errors.Is(err, admission.ErrUnavailable) || id != failed && err != nil {
				t.Fatalf("retry for %q = %v; only reached key %q should fail", id, err, failed)
			}
		}
		store.saveHook = nil
		if err := c.Flush(t.Context()); err != nil || store.saveCount() != 3 {
			t.Fatalf("pending keys lost: err=%v saves=%d", err, store.saveCount())
		}
	})
}

func TestRunRetriesAndLeavesFinalFlushToCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore()
		c, _ := coordinator(t, store)
		request, attempt := start(t, c, policy(t, "key"))
		observe(t, attempt, 10)
		failure := errors.New("save failed")
		store.saveHook = func(context.Context, accesskey.ID, budget.State) error { return failure }
		ctx, cancel := context.WithCancel(t.Context())
		reports := make(chan error, 1)
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx, 5*time.Second, func(err error) { reports <- err }) }()
		synctest.Wait()

		time.Sleep(4 * time.Second)
		if store.saveCount() != 0 {
			t.Fatal("worker saved before its first tick")
		}
		time.Sleep(time.Second)
		if err := <-reports; !errors.Is(err, failure) {
			t.Fatalf("reported error=%v; want save failure", err)
		}
		store.mu.Lock()
		store.saveHook = nil
		store.mu.Unlock()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if store.saved("key").FiveHours.Used != 10 || store.saveCount() != 2 {
			t.Fatal("worker did not retry on next tick")
		}
		observe(t, attempt, 20)
		request.Release()
		saving := make(chan struct{})
		store.mu.Lock()
		store.saveHook = func(ctx context.Context, _ accesskey.ID, _ budget.State) error {
			close(saving)
			<-ctx.Done()
			return ctx.Err()
		}
		store.mu.Unlock()
		time.Sleep(5 * time.Second)
		<-saving
		cancelledAt := time.Now()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("worker shutdown=%v; want context.Canceled", err)
		}
		if time.Now() != cancelledAt || store.saveCount() != 3 {
			t.Fatalf("worker shutdown: elapsed=%v saves=%d; want immediate cancellation and three saves", time.Since(cancelledAt), store.saveCount())
		}
		store.saveHook = nil
		if err := c.Flush(t.Context()); err != nil || store.saved("key").FiveHours.Used != 20 {
			t.Fatalf("final Flush lost pending usage: %v", err)
		}
	})
}
