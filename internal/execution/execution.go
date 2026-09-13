// Package execution owns authenticated generation, account selection and cleanup.
package execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
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
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	keyID     accesskey.ID
	accountID account.ID // Protected by Executor.mu.
	id        string
	model     string
	started   time.Time
	slot      concurrency.Slot
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

// Generate returns a complete Responses result, retaining observed usage on failure.
func (e *Executor) Generate(ctx context.Context, key, requestID string, input codex.Request) (codex.Result, error) {
	stream, err := e.Stream(ctx, key, requestID, input)
	if err != nil {
		return codex.Result{}, err
	}
	defer stream.Close()
	for {
		if _, err := stream.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return stream.Result(), err
		}
	}
}

// Stream returns a live attempt. HTTP must wait for this call before starting its response.
// The caller must Close after delivery or a write failure, including after a terminal event.
func (e *Executor) Stream(ctx context.Context, key, requestID string, input codex.Request) (*Stream, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("execution: request ID is required")
	}
	r, err := e.admit(ctx, key, requestID, input.Model())
	if err != nil {
		return nil, err
	}
	if input.OmittedMaxOutputTokens() {
		e.logger.LogAttrs(ctx, slog.LevelWarn, "max_output_tokens omitted for Codex",
			slog.String("request_id", requestID), slog.String("key_id", string(r.keyID)), slog.String("model", r.model))
	}
	attempt, err := e.open(r, input)
	if err != nil {
		e.finish(r, codex.Result{}, err)
		return nil, err
	}
	s := &Stream{attempt: attempt, owner: e, request: r}
	s.stop = context.AfterFunc(r.ctx, s.finish)
	if err := r.ctx.Err(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (e *Executor) admit(ctx context.Context, rawKey, id, model string) (state *requestState, err error) {
	started := time.Now()
	var keyID accesskey.ID
	defer func() {
		if err != nil {
			e.logResult(ctx, id, keyID, model, time.Since(started), codex.Result{}, err)
		}
	}()
	e.gate.RLock()
	defer e.gate.RUnlock()
	if e.closed {
		return nil, ErrClosed
	}
	key, err := e.authenticate(ctx, rawKey)
	if err != nil {
		return nil, err
	}
	keyID = key.Key.Identity().ID
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
		keyID: key.Key.Identity().ID, id: id, model: model, started: started}
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
	e.logResult(r.ctx, r.id, r.keyID, r.model, time.Since(r.started), result, err)
	e.mu.Lock()
	r.slot.Release()
	delete(e.active, r)
	close(r.done)
	e.mu.Unlock()
}

func operationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return context.Canceled
	}
	return ErrUnavailable
}
