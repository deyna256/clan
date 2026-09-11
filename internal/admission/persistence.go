package admission

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/budget"
)

func (c *Coordinator) load(ctx context.Context, id accesskey.ID) (*keyState, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		key := c.keys[id]
		if key == nil {
			// ponytail: retain touched keys for process lifetime; add eviction only
			// with rules preserving active handles, pending saves and saved windows.
			key = &keyState{}
			c.keys[id] = key
		}
		if key.loaded {
			c.mu.Unlock()
			return key, nil
		}
		if pending := key.loading; pending != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-pending:
				continue
			}
		}
		key.loading = make(chan struct{})
		c.mu.Unlock()

		state, found, err := c.store.Load(ctx, id)
		c.mu.Lock()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			if !found {
				state = budget.State{}
			}
			err = budget.Check(state, budget.Limits{}, time.Now())
		}
		if err == nil {
			key.budget, key.loaded = state, true
		}
		close(key.loading)
		key.loading = nil
		c.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("%w: load budget: %w", ErrUnavailable, err)
		}
		return key, nil
	}
}

// Flush saves a finite set of pending keys with one ordered writer. Waiting for
// that writer respects cancellation. Each save has a five-second timeout; an
// individual failure does not stop other keys unless ctx ends. Changes accepted
// during I/O remain pending. A successful save clears that key's failure flag,
// even when newer changes remain pending for the next flush.
func (c *Coordinator) Flush(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.writer <- struct{}{}:
	}
	defer func() { <-c.writer }()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	var pending []accesskey.ID
	for id, key := range c.keys {
		if key.dirty {
			pending = append(pending, id)
		}
	}
	c.mu.Unlock()

	var failures []error
	for _, id := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		c.mu.Lock()
		key := c.keys[id]
		state := key.budget
		key.dirty = false
		c.mu.Unlock()

		saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.store.Save(saveCtx, id, state)
		cancel()
		c.mu.Lock()
		key.failed = err != nil
		if err != nil {
			key.dirty = true
		}
		c.mu.Unlock()
		if err != nil {
			failures = append(failures, fmt.Errorf("admission: save budget for %q: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

// Run flushes on each interval, reporting failures and retrying on later ticks.
// Supply a positive interval (initially five seconds) and a non-nil reporter;
// reporting is synchronous. Run starts no goroutine and returns ctx.Err() on
// cancellation. The application drains request cleanup, cancels and joins Run,
// then calls Flush with a separate bounded shutdown context before closing storage.
func (c *Coordinator) Run(ctx context.Context, interval time.Duration, report func(error)) error {
	if interval <= 0 || report == nil {
		return errors.New("admission: positive save interval and error reporter are required")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.Flush(ctx); err != nil && ctx.Err() == nil {
				report(err)
			}
		}
	}
}
