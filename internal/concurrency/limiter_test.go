package concurrency_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/concurrency"
)

func TestReleaseMakesOneSlotAvailable(t *testing.T) {
	limiter := concurrency.New()
	first := acquire(t, limiter, "key", 2)
	acquire(t, limiter, "key", 2)

	first.Release()
	replacement, err := limiter.TryAcquire("key", 2)

	if err != nil {
		t.Fatalf("TryAcquire() after release: %v", err)
	}
	t.Cleanup(replacement.Release)
	_, err = limiter.TryAcquire("key", 2)
	if !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("after replacing one slot, error = %v, want ErrLimitReached", err)
	}
}

func TestRejectedAcquisitionsLeaveCountsUnchanged(t *testing.T) {
	tests := []struct {
		name         string
		keyID        accesskey.ID
		limit        int
		limitReached bool
	}{
		{name: "full", keyID: "key", limit: 1, limitReached: true},
		{name: "zero", keyID: "key", limit: 0, limitReached: true},
		{name: "missing key", limit: 1},
		{name: "blank key", keyID: " \t\n", limit: 1},
		{name: "invalid limit", keyID: "key", limit: -2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter := concurrency.New()
			held := acquire(t, limiter, "key", 1)

			rejected, err := limiter.TryAcquire(tt.keyID, tt.limit)

			if err == nil || errors.Is(err, concurrency.ErrLimitReached) != tt.limitReached {
				t.Fatalf("TryAcquire(%q, %d) error = %v, want error with limitReached=%t",
					tt.keyID, tt.limit, err, tt.limitReached)
			}
			rejected.Release()
			if _, err := limiter.TryAcquire("key", 1); !errors.Is(err, concurrency.ErrLimitReached) {
				t.Fatalf("rejected slot released active work: error = %v, want ErrLimitReached", err)
			}

			held.Release()
			next, err := limiter.TryAcquire("key", 1)
			if err != nil {
				t.Fatalf("rejection consumed capacity: TryAcquire() after release: %v", err)
			}
			t.Cleanup(next.Release)
		})
	}
}

func TestLowerLimitCountsExistingSlots(t *testing.T) {
	for _, initial := range []int{3, concurrency.Unlimited} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			limiter := concurrency.New()
			first := acquire(t, limiter, "key", initial)
			second := acquire(t, limiter, "key", initial)
			acquire(t, limiter, "key", initial)

			if _, err := limiter.TryAcquire("key", 2); !errors.Is(err, concurrency.ErrLimitReached) {
				t.Fatalf("three active slots, limit two: error = %v, want ErrLimitReached", err)
			}
			first.Release()
			if _, err := limiter.TryAcquire("key", 2); !errors.Is(err, concurrency.ErrLimitReached) {
				t.Fatalf("two active slots, limit two: error = %v, want ErrLimitReached", err)
			}

			second.Release()
			next, err := limiter.TryAcquire("key", 2)
			if err != nil {
				t.Fatalf("one active slot, limit two: %v", err)
			}
			t.Cleanup(next.Release)
		})
	}
}

func TestHigherLimitAllowsNewSlots(t *testing.T) {
	for _, updated := range []int{2, concurrency.Unlimited} {
		t.Run(fmt.Sprint(updated), func(t *testing.T) {
			limiter := concurrency.New()
			acquire(t, limiter, "key", 1)

			slot, err := limiter.TryAcquire("key", updated)

			if err != nil {
				t.Fatalf("TryAcquire() with increased limit: %v", err)
			}
			t.Cleanup(slot.Release)
		})
	}
}

func TestKeysHaveIndependentCounts(t *testing.T) {
	limiter := concurrency.New()
	keys := []accesskey.ID{"key", "Key", " key ", "other"}

	for _, keyID := range keys {
		slot, err := limiter.TryAcquire(keyID, 1)
		if err != nil {
			t.Fatalf("first TryAcquire(%q): %v", keyID, err)
		}
		t.Cleanup(slot.Release)
	}

	for _, keyID := range keys {
		if _, err := limiter.TryAcquire(keyID, 1); !errors.Is(err, concurrency.ErrLimitReached) {
			t.Errorf("second TryAcquire(%q) error = %v, want ErrLimitReached", keyID, err)
		}
	}
}

func TestLimitersHaveIndependentCounts(t *testing.T) {
	first := concurrency.New()
	acquire(t, first, "key", 1)

	second := concurrency.New()
	slot, err := second.TryAcquire("key", 1)
	if err != nil {
		t.Fatalf("first acquisition in another limiter: %v", err)
	}
	t.Cleanup(slot.Release)
	if _, err := first.TryAcquire("key", 1); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("original limiter lost active slot: error = %v, want ErrLimitReached", err)
	}
}

func TestOldSlotCannotReleaseNewWork(t *testing.T) {
	limiter := concurrency.New()
	old := acquire(t, limiter, "key", 1)
	old.Release()
	current := acquire(t, limiter, "key", 1)

	old.Release()

	if _, err := limiter.TryAcquire("key", 1); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("old slot released new work: error = %v, want ErrLimitReached", err)
	}
	current.Release()
	slot, err := limiter.TryAcquire("key", 1)
	if err != nil {
		t.Fatalf("TryAcquire() after releasing current work: %v", err)
	}
	t.Cleanup(slot.Release)
}

func TestConcurrentAcquisitionsRespectLimit(t *testing.T) {
	limiter := concurrency.New()
	const limit = 3
	results := make([]struct {
		slot concurrency.Slot
		err  error
	}, 24)

	concurrently(len(results), func(i int) {
		results[i].slot, results[i].err = limiter.TryAcquire("key", limit)
	})

	accepted := 0
	for i, result := range results {
		t.Cleanup(result.slot.Release)
		if result.err == nil {
			accepted++
		} else if !errors.Is(result.err, concurrency.ErrLimitReached) {
			t.Errorf("acquisition %d: unexpected error %v", i, result.err)
		}
	}
	if accepted != limit {
		t.Fatalf("accepted %d concurrent acquisitions, want %d", accepted, limit)
	}
}

func TestConcurrentReleaseThroughCopiesFreesOneSlot(t *testing.T) {
	limiter := concurrency.New()
	slot := acquire(t, limiter, "key", 2)
	acquire(t, limiter, "key", 2)

	concurrently(24, func(_ int) {
		copy := slot
		copy.Release()
	})
	slot.Release()

	next, err := limiter.TryAcquire("key", 2)
	if err != nil {
		t.Fatalf("TryAcquire() after releasing copies: %v", err)
	}
	t.Cleanup(next.Release)
	if _, err := limiter.TryAcquire("key", 2); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("copies freed another slot: error = %v, want ErrLimitReached", err)
	}
}

func TestConcurrentReleaseOfDistinctSlotsRestoresCapacity(t *testing.T) {
	limiter := concurrency.New()
	slots := make([]concurrency.Slot, 24)
	for i := range slots {
		slots[i] = acquire(t, limiter, "key", len(slots))
	}

	concurrently(len(slots), func(i int) { slots[i].Release() })

	slot, err := limiter.TryAcquire("key", 1)
	if err != nil {
		t.Fatalf("TryAcquire() after releasing all slots: %v", err)
	}
	t.Cleanup(slot.Release)
	if _, err := limiter.TryAcquire("key", 1); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("release lost the count: error = %v, want ErrLimitReached", err)
	}
}

func acquire(t *testing.T, limiter *concurrency.Limiter, keyID accesskey.ID, limit int) concurrency.Slot {
	t.Helper()
	slot, err := limiter.TryAcquire(keyID, limit)
	if err != nil {
		t.Fatalf("TryAcquire(%q, %d): %v", keyID, limit, err)
	}
	t.Cleanup(slot.Release)
	return slot
}

func concurrently(calls int, f func(int)) {
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range calls {
		workers.Go(func() {
			<-start
			f(i)
		})
	}
	close(start)
	workers.Wait()
}
