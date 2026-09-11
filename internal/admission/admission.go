// Package admission coordinates request admission, observed usage and budget saves.
package admission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/budget"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/ratelimit"
)

var (
	// ErrDenied means the supplied access key does not permit this attempt.
	ErrDenied = errors.New("admission: access denied")
	// ErrUnavailable means required budget state cannot be used reliably.
	ErrUnavailable = errors.New("admission: budget state unavailable")
	// ErrReleased means the request no longer permits new attempts.
	ErrReleased = errors.New("admission: request released")
)

// SnapshotStore restores and replaces absolute budget snapshots. Successive
// Saves must not let an earlier write overwrite a later snapshot, including
// after cancellation. Errors may leave commit success uncertain.
type SnapshotStore interface {
	Load(context.Context, accesskey.ID) (budget.State, bool, error)
	Save(context.Context, accesskey.ID, budget.State) error
}

// Policy is one immutable admission input. Budget pointers are read only during
// the call and never retained; callers must not mutate them during that call.
// Concurrency uses concurrency.Unlimited or a nonnegative request cap. The
// configuration owner separately configures RPM, including unlimited keys.
type Policy struct {
	Key         accesskey.AccessKey
	Budgets     budget.Limits
	Concurrency int
}

// Coordinator owns live accounting and request slots. Construct it with [New]
// and do not copy it. Methods are safe for concurrent use; the application owns
// the store, RPM configuration, worker goroutine and shutdown order.
type Coordinator struct {
	mu     sync.Mutex
	keys   map[accesskey.ID]*keyState
	store  SnapshotStore
	rpm    *ratelimit.Limiter
	slots  *concurrency.Limiter
	writer chan struct{}
}

type keyState struct {
	budget  budget.State
	loaded  bool
	loading chan struct{}
	dirty   bool
	failed  bool
}

// New creates a coordinator without starting workers or taking ownership of store.
func New(store SnapshotStore, rpm *ratelimit.Limiter) (*Coordinator, error) {
	if store == nil || rpm == nil {
		return nil, errors.New("admission: snapshot store and RPM limiter are required")
	}
	return &Coordinator{
		keys: make(map[accesskey.ID]*keyState), store: store, rpm: rpm,
		slots: concurrency.New(), writer: make(chan struct{}, 1),
	}, nil
}

// Start admits a request and its first attempt. Permission, restoration, budget
// and concurrency failures consume no RPM and open no windows. After RPM succeeds,
// admission is final; later cancellation or execution failure does not refund it.
func (c *Coordinator) Start(ctx context.Context, policy Policy, model accesskey.Model, candidate account.Identity) (Request, Attempt, error) {
	if !policy.Key.Allows(model, candidate) {
		return Request{}, Attempt{}, ErrDenied
	}
	id := policy.Key.Identity().ID
	key, err := c.load(ctx, id)
	if err != nil {
		return Request{}, Attempt{}, err
	}

	// ponytail: one short state lock serializes keys; split if contention warrants it.
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Request{}, Attempt{}, err
	}
	next, err := prepare(key, policy.Budgets)
	if err != nil {
		return Request{}, Attempt{}, err
	}
	slot, err := c.slots.TryAcquire(id, policy.Concurrency)
	if err != nil {
		return Request{}, Attempt{}, err
	}
	if err := c.rpm.TryAcquire(id); err != nil {
		slot.Release()
		return Request{}, Attempt{}, err
	}
	key.publish(next)
	request := Request{state: &requestState{coordinator: c, id: id, key: key, model: model, slot: slot}}
	return request, c.attempt(key), nil
}

// Request owns the original request's RPM charge and concurrency slot. Copies
// share lifecycle state. Release it only after provider work and cleanup finish.
type Request struct {
	state *requestState
}

type requestState struct {
	coordinator *Coordinator
	id          accesskey.ID
	key         *keyState
	model       accesskey.Model
	slot        concurrency.Slot
	released    bool
}

// NextAttempt checks fresh access and token budgets for a retry or fallback.
// It retains the original key, model, RPM charge and slot; Concurrency is ignored.
// The executor owns sequential attempt execution and provider cleanup.
func (r Request) NextAttempt(ctx context.Context, policy Policy, candidate account.Identity) (Attempt, error) {
	if r.state == nil {
		return Attempt{}, ErrReleased
	}
	s := r.state
	c := s.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.released {
		return Attempt{}, ErrReleased
	}
	if err := ctx.Err(); err != nil {
		return Attempt{}, err
	}
	if policy.Key.Identity().ID != s.id || !policy.Key.Allows(s.model, candidate) {
		return Attempt{}, ErrDenied
	}
	next, err := prepare(s.key, policy.Budgets)
	if err != nil {
		return Attempt{}, err
	}
	s.key.publish(next)
	return c.attempt(s.key), nil
}

// Release frees the request slot once. Releasing a zero request is a no-op.
// Existing attempt handles may still account late usage after release.
func (r Request) Release() {
	if r.state == nil {
		return
	}
	s := r.state
	s.coordinator.mu.Lock()
	defer s.coordinator.mu.Unlock()
	if !s.released {
		s.released = true
		s.slot.Release()
	}
}

func prepare(key *keyState, limits budget.Limits) (budget.State, error) {
	if key.failed && (limits.FiveHours != nil || limits.SevenDays != nil) {
		return budget.State{}, ErrUnavailable
	}
	now := time.Now()
	if err := budget.Check(key.budget, limits, now); err != nil {
		if errors.Is(err, budget.ErrExhausted) {
			return budget.State{}, err
		}
		return budget.State{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return budget.Admit(key.budget, now)
}

func (k *keyState) publish(next budget.State) {
	if next != k.budget {
		k.budget = next
		k.dirty = true
	}
}
