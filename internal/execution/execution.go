// Package execution owns authenticated generation, account selection and cleanup.
package execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/selection"
	"github.com/deyna256/clan/internal/storage"
)

var (
	ErrUnauthorized = errors.New("execution: invalid or revoked access key")
	ErrClosed       = errors.New("execution: closed")
	ErrNoAccounts   = errors.New("execution: no eligible accounts")
	ErrUnavailable  = errors.New("execution: service unavailable")
	ErrDelivery     = errors.New("execution: response delivery failed")
)

// Executor coordinates one installation. Construct it with New; do not copy it.
// Management must use its mutation methods instead of changing storage directly.
type Executor struct {
	store     *storage.Store
	oauth     *codexoauth.Manager
	client    *codex.Client
	catalog   *codex.Catalog
	logger    *slog.Logger
	limiter   *concurrency.Limiter
	selector  *selection.RoundRobin
	gate      sync.RWMutex
	mu        sync.Mutex
	closed    bool // Protected by gate; mu protects active requests and cooldowns.
	active    map[*requestState]struct{}
	cooldowns map[account.ID]time.Time
}

type requestState struct {
	ctx                  context.Context
	cancel               context.CancelFunc
	done                 chan struct{}
	keyID                accesskey.ID
	accountID            account.ID // Protected by Executor.mu.
	lastAttemptAccountID account.ID // Protected by Executor.mu.
	id                   string
	model                string
	started              time.Time
	slot                 concurrency.Slot
	responseStarted      atomic.Bool
}

// New borrows the store, OAuth manager, client and logger; Close owns only execution and catalog work.
func New(store *storage.Store, oauth *codexoauth.Manager, client *codex.Client, logger *slog.Logger) (*Executor, error) {
	if store == nil || oauth == nil || client == nil || logger == nil {
		return nil, errors.New("execution: dependencies are required")
	}
	catalog, err := codex.NewCatalog(modelFetcher{oauth: oauth, client: client, logger: logger}.fetchModels)
	if err != nil {
		return nil, err
	}
	return &Executor{store: store, oauth: oauth, client: client, catalog: catalog, logger: logger,
		limiter: concurrency.New(), selector: selection.NewRoundRobin(),
		active: make(map[*requestState]struct{}), cooldowns: make(map[account.ID]time.Time)}, nil
}

// RequestInfo carries the gateway request ID and start time.
// A zero Started value uses the execution start time.
type RequestInfo struct {
	ID      string
	Started time.Time
}

// Result holds the ordinary response and its key slot until Close.
// Its zero value is safe to close; copies share the same cleanup.
type Result struct {
	codex.Result
	stream     *Stream
	cleanupErr error
}

// Admitted reports whether the result owns a request that must be finalized by Close.
func (r Result) Admitted() bool { return r.stream != nil }

// CleanupError reports failure to close the upstream response before delivery.
func (r Result) CleanupError() error { return r.cleanupErr }

// Close releases the slot after delivery. It is safe to repeat or call concurrently.
func (r Result) Close() error {
	if r.stream == nil {
		return nil
	}
	return r.stream.Close()
}

// Context reports execution cancellation. A zero result has no active request.
func (r Result) Context() context.Context {
	if r.stream == nil {
		return context.Background()
	}
	return r.stream.Context()
}

// DeliveryFailed records a downstream encoding or write failure before Close.
func (r Result) DeliveryFailed() {
	if r.stream != nil {
		r.stream.DeliveryFailed()
	}
}

// ResponseStarted records HTTP response commitment before body delivery.
func (r Result) ResponseStarted() {
	if r.stream != nil {
		r.stream.ResponseStarted()
	}
}

// Generate returns a complete Responses result, retaining observed usage on failure.
// The caller must Close the result after delivery, even when an error is returned.
// If Admitted is false, the caller owns rejection delivery and recording.
func (e *Executor) Generate(ctx context.Context, key string, info RequestInfo, input codex.Request) (Result, error) {
	stream, err := e.Stream(ctx, key, info, input)
	if err != nil {
		return Result{stream: stream}, err
	}
	for {
		if _, err := stream.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			cleanupErr := stream.attempt.Close()
			return Result{Result: stream.Result(), stream: stream, cleanupErr: cleanupErr}, errors.Join(err, cleanupErr)
		}
	}
}

// Stream returns an admitted request, including when opening its attempt fails.
// The caller must Close every nonnil stream after delivery or abort, even on error.
// A nil stream leaves rejection delivery and recording to the caller.
func (e *Executor) Stream(ctx context.Context, key string, info RequestInfo, input codex.Request) (*Stream, error) {
	if strings.TrimSpace(info.ID) == "" {
		return nil, errors.New("execution: request ID is required")
	}
	r, err := e.admit(ctx, key, info, input.Model())
	if err != nil {
		return nil, err
	}
	if input.OmittedMaxOutputTokens() {
		e.logger.LogAttrs(ctx, slog.LevelWarn, "max_output_tokens omitted for Codex",
			slog.String("request_id", info.ID), slog.String("key_id", string(r.keyID)), slog.String("model", r.model))
	}
	attempt, err := e.open(r, input)
	s := &Stream{attempt: attempt, owner: e, request: r, openErr: err}
	s.stop = context.AfterFunc(r.ctx, s.abort)
	if err != nil {
		return s, err
	}
	if err := r.ctx.Err(); err != nil {
		return s, err
	}
	return s, nil
}

func (e *Executor) admit(ctx context.Context, rawKey string, info RequestInfo, model string) (*requestState, error) {
	if info.Started.IsZero() {
		info.Started = time.Now()
	}
	e.gate.RLock()
	defer e.gate.RUnlock()
	if e.closed {
		return nil, ErrClosed
	}
	key, err := e.authenticate(ctx, rawKey)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slot, err := e.limiter.TryAcquire(key.Key.Identity().ID, key.ConcurrencyLimit)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &requestState{ctx: ctx, cancel: cancel, done: make(chan struct{}), slot: slot,
		keyID: key.Key.Identity().ID, id: info.ID, model: model, started: info.Started}
	e.active[r] = struct{}{}
	return r, nil
}

func (e *Executor) authenticate(ctx context.Context, rawKey string) (storage.AccessKeyRecord, error) {
	hash, ok := accesskey.Hash(rawKey)
	if !ok {
		return storage.AccessKeyRecord{}, ErrUnauthorized
	}
	record, err := e.store.FindAccessKeyByHash(ctx, hash)
	if errors.Is(err, storage.ErrNotFound) || (err == nil && !record.Key.Enabled()) {
		return storage.AccessKeyRecord{}, ErrUnauthorized
	}
	if err != nil {
		return storage.AccessKeyRecord{}, operationError(ctx, err)
	}
	return record, nil
}

func (e *Executor) finish(r *requestState, result codex.Result, err error) {
	r.cancel()
	e.mu.Lock()
	accountID := r.lastAttemptAccountID
	e.mu.Unlock()
	finished := time.Now()
	e.recordResult(r.ctx, storage.RequestRecord{ID: r.id, FinishedAt: finished, KeyID: r.keyID,
		AccountID: accountID, Model: r.model, Duration: finished.Sub(r.started),
		Result: resultCode(result, err), ResponseStarted: r.responseStarted.Load(), Usage: result.Usage})
	e.mu.Lock()
	r.slot.Release()
	delete(e.active, r)
	close(r.done)
	e.mu.Unlock()
}

func operationError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return ErrUnavailable
	}
}
