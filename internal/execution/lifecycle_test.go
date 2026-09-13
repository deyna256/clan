package execution_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/execution"
	"github.com/deyna256/clan/internal/storage"
)

func TestConcurrentAdmissionsAndIndependentKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return response(http.StatusOK, ""), nil
		}))
		otherKey := f.addKey(t, "other", 1)
		type result struct {
			stream *execution.Stream
			err    error
		}
		results := make(chan result, 8)

		for range cap(results) {
			go func() {
				s, err := f.executor.Stream(t.Context(), f.key, "request", f.request)
				results <- result{s, err}
			}()
		}
		var admitted, rejected int
		var active *execution.Stream
		for range cap(results) {
			r := <-results
			if r.err == nil {
				admitted++
				active = r.stream
				t.Cleanup(func() { r.stream.Close() })
			} else if errors.Is(r.err, concurrency.ErrLimitReached) {
				rejected++
			} else {
				t.Errorf("admission error = %v", r.err)
			}
		}
		f.open(t, otherKey)

		if admitted != 1 || rejected != 7 || calls.Load() != 2 {
			t.Fatalf("admitted/rejected/provider calls = %d/%d/%d, want 1/7/2", admitted, rejected, calls.Load())
		}
		if err := active.Close(); err != nil {
			t.Fatal(err)
		}
		f.open(t, f.key)
	})
}

func TestTerminalDeliveryRetainsSlotUntilClose(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"done\",\"output\":[],\"usage\":{\"input_tokens\":7}}}\n\n"), nil
	}))
	s := f.open(t, f.key)

	if event, err := s.Next(); err != nil || !strings.Contains(string(event), "response.completed") {
		t.Fatalf("terminal event = %s, error = %v", event, err)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("read after terminal = %v, want EOF", err)
	}
	if _, err := f.executor.Stream(t.Context(), f.key, "overlap", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("admission during terminal delivery = %v, want limit", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.Result().Usage.Input; !got.Known || got.Tokens != 7 {
		t.Fatalf("usage after Close = %+v, want known 7", got)
	}
	f.open(t, f.key)
}

func TestOrdinaryDeliveryRetainsSlotUntilClose(t *testing.T) {
	for _, tt := range []struct {
		name, wire string
		failed     bool
	}{
		{name: "completed", wire: completed},
		{name: "partial failure", wire: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"usage\":{\"input_tokens\":7}}}\n\n", failed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, tt.wire), nil
			}))

			result, err := f.executor.Generate(t.Context(), f.key, "ordinary", f.request)
			defer result.Close()

			if (err != nil) != tt.failed {
				t.Fatalf("Generate error = %v, want failure %v", err, tt.failed)
			}
			if tt.failed {
				var failure *codex.Failure
				if !errors.As(err, &failure) || failure.Category != codex.InvalidResponse {
					t.Fatalf("partial failure = %v, want invalid response", err)
				}
			}
			overlap, err := f.executor.Generate(t.Context(), f.key, "overlap", f.request)
			defer overlap.Close()
			if !errors.Is(err, concurrency.ErrLimitReached) {
				t.Fatalf("admission during ordinary delivery = %v, want limit", err)
			}
			if err := overlap.Close(); err != nil {
				t.Fatalf("Close rejected result = %v", err)
			}
			copied := result
			if err := result.Close(); err != nil {
				t.Fatal(err)
			}
			if err := copied.Close(); err != nil {
				t.Fatalf("repeated Close = %v", err)
			}
			if got := result.Usage.Input; !got.Known || got.Tokens != 7 {
				t.Fatalf("usage after Close = %+v, want known 7", got)
			}
			f.open(t, f.key)
		})
	}
}

func TestStreamReadFailureRetainsSlotUntilClose(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, ""), nil
	}))
	s := f.open(t, f.key)

	if _, err := s.Next(); err == nil {
		t.Fatal("empty upstream stream returned no error")
	}
	if _, err := f.executor.Stream(t.Context(), f.key, "overlap", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("admission before error delivery Close = %v, want limit", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, f.key)
}

func TestZeroResultContext(t *testing.T) {
	var result execution.Result
	if ctx := result.Context(); ctx == nil || ctx.Err() != nil || ctx.Done() != nil {
		t.Fatalf("zero result context = %v, want an uncanceled context without a Done channel", ctx)
	}
	result.DeliveryFailed()
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationAndRevocationRetainOrdinaryResultsUntilDeliveryCloses(t *testing.T) {
	for _, action := range []string{"cancel", "revoke"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
					return response(http.StatusOK, completed), nil
				}))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result, err := f.executor.Generate(ctx, f.key, "held", f.request)
				defer result.Close()
				if err != nil {
					t.Fatal(err)
				}
				if f.logs.Len() != 0 {
					t.Fatal("ordinary result logged before delivery finished")
				}

				revoked := make(chan error, 1)
				if action == "cancel" {
					cancel()
				} else {
					go func() { revoked <- f.executor.RevokeKey(t.Context(), "key") }()
				}
				<-result.Context().Done()
				synctest.Wait()
				if f.logs.Len() != 0 {
					t.Fatal("canceled result logged before delivery finished")
				}
				select {
				case err := <-revoked:
					t.Fatalf("revocation returned before delivery Close: %v", err)
				default:
				}
				if action == "cancel" {
					if _, err := f.executor.Stream(t.Context(), f.key, "overlap", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
						t.Fatalf("admission before canceled delivery Close = %v, want limit", err)
					}
				}
				if err := result.Close(); err != nil {
					t.Fatalf("Close after %s = %v", action, err)
				}
				if action == "revoke" {
					if err := <-revoked; err != nil {
						t.Fatal(err)
					}
				}

				entries := logEntries(t, f.logs.String())
				last := entries[len(entries)-1]
				if last["request_id"] != "held" || last["input_tokens"] != float64(7) || last["result"] != "canceled" {
					t.Fatalf("cleanup log = %v, want canceled held result with known usage", last)
				}
				if action == "cancel" {
					f.open(t, f.key)
				} else if _, err := f.executor.Stream(t.Context(), f.key, "revoked", f.request); !errors.Is(err, execution.ErrUnauthorized) {
					t.Fatalf("admission after revocation = %v, want unauthorized", err)
				}
				if err := result.Close(); err != nil {
					t.Fatalf("Close after %s = %v", action, err)
				}
			})
		})
	}
}

func TestSlotSpansRetryAndAttemptCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body, unblock := newBlockedClose()
		defer unblock()
		var calls atomic.Int32
		f := newFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			if r.Header.Get("ChatGPT-Account-Id") == "one" {
				return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: body}, nil
			}
			return response(http.StatusOK, ""), nil
		}))
		f.addAccount(t, "two")
		opened := make(chan *execution.Stream, 1)
		failed := make(chan error, 1)
		go func() {
			s, err := f.executor.Stream(t.Context(), f.key, "retry", f.request)
			opened <- s
			failed <- err
		}()
		<-body.closing

		if _, err := f.executor.Stream(t.Context(), f.key, "overlap", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
			t.Errorf("admission during failed-attempt cleanup = %v, want limit", err)
		}
		if calls.Load() != 1 {
			t.Errorf("attempts before cleanup = %d, want 1", calls.Load())
		}
		if err := f.executor.SetConcurrency(t.Context(), "key", 0); err != nil {
			t.Fatal(err)
		}
		unblock()
		s := <-opened
		if err := <-failed; err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 2 || body.closes.Load() != 1 {
			t.Fatalf("attempts/closes = %d/%d, want 2/1", calls.Load(), body.closes.Load())
		}
		if _, err := f.executor.Stream(t.Context(), f.key, "zero", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
			t.Fatalf("admission after limit becomes zero = %v, want limit", err)
		}
	})
}

func TestRevocationDuringInitializationWaitsForCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		f := newFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			<-release
			return nil, r.Context().Err()
		}))
		requestDone := make(chan error, 1)
		go func() {
			_, err := f.executor.Stream(t.Context(), f.key, "initializing", f.request)
			requestDone <- err
		}()
		<-entered

		revoked := make(chan error, 1)
		go func() { revoked <- f.executor.RevokeKey(t.Context(), "key") }()
		<-canceled
		synctest.Wait()
		select {
		case err := <-revoked:
			t.Fatalf("revocation returned before upstream cleanup: %v", err)
		default:
		}
		if _, err := f.executor.Stream(t.Context(), f.key, "late", f.request); !errors.Is(err, execution.ErrUnauthorized) {
			t.Fatalf("admission after persisted revocation = %v", err)
		}
		unblock()
		if err := <-requestDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupted initialization = %v, want cancellation", err)
		}
		if err := <-revoked; err != nil {
			t.Fatal(err)
		}
		if err := f.executor.RevokeKey(t.Context(), "key"); err != nil {
			t.Fatalf("repeated revocation = %v", err)
		}
	})
}

func TestRevocationClosesUnreadStreamBeforeReturning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body, unblock := newBlockedClose()
		defer unblock()
		f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
		}))
		s := f.open(t, f.key)

		revoked := make(chan error, 1)
		go func() { revoked <- f.executor.RevokeKey(t.Context(), "key") }()
		<-body.closing
		synctest.Wait()
		select {
		case err := <-revoked:
			t.Fatalf("revocation returned during body Close: %v", err)
		default:
		}
		if _, err := f.executor.Stream(t.Context(), f.key, "late", f.request); !errors.Is(err, execution.ErrUnauthorized) {
			t.Fatalf("admission after revocation = %v", err)
		}
		unblock()
		synctest.Wait()
		select {
		case err := <-revoked:
			t.Fatalf("revocation returned before downstream delivery Close: %v", err)
		default:
		}
		if _, err := s.Next(); !errors.Is(err, context.Canceled) {
			t.Fatalf("unread revoked stream = %v, want cancellation", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-revoked; err != nil {
			t.Fatal(err)
		}
		if body.closes.Load() != 1 {
			t.Fatalf("body closes = %d, want 1", body.closes.Load())
		}
	})
}

func TestCancellationRetainsSlotUntilConcurrentCloseFinishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body, unblock := newBlockedClose()
		defer unblock()
		var calls atomic.Int32
		f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
			}
			return response(http.StatusOK, ""), nil
		}))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s, err := f.executor.Stream(ctx, f.key, "cancel", f.request)
		if err != nil {
			t.Fatal(err)
		}

		cancel()
		<-body.closing
		synctest.Wait()
		if _, err := f.executor.Stream(t.Context(), f.key, "overlap", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
			t.Errorf("admission during canceled request cleanup = %v, want limit", err)
		}
		closes := make(chan error, 4)
		for range cap(closes) {
			go func() { closes <- s.Close() }()
		}
		unblock()
		for range cap(closes) {
			if err := <-closes; err != nil {
				t.Error(err)
			}
		}
		if body.closes.Load() != 1 {
			t.Fatalf("concurrent body closes = %d, want 1", body.closes.Load())
		}
		if _, err := s.Next(); !errors.Is(err, context.Canceled) {
			t.Fatalf("stream after cancellation = %v", err)
		}
		f.open(t, f.key)
	})
}

func TestAdmissionRacingRevocationCannotEscapeCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, ""), nil
		}))
		if err := f.executor.SetConcurrency(t.Context(), "key", concurrency.Unlimited); err != nil {
			t.Fatal(err)
		}
		earlier := f.open(t, f.key)
		type result struct {
			stream *execution.Stream
			err    error
		}
		results := make(chan result, 12)
		start := make(chan struct{})
		for range cap(results) {
			go func() {
				<-start
				s, err := f.executor.Stream(t.Context(), f.key, "racing", f.request)
				results <- result{s, err}
			}()
		}
		revoked := make(chan error, 1)
		go func() {
			<-start
			revoked <- f.executor.RevokeKey(t.Context(), "key")
		}()

		close(start)
		<-earlier.Context().Done()
		for range cap(results) {
			r := <-results
			if r.err == nil {
				if _, err := r.stream.Next(); !errors.Is(err, context.Canceled) {
					t.Errorf("admitted stream survived successful revocation: %v", err)
				}
				r.stream.Close()
			} else if !errors.Is(r.err, execution.ErrUnauthorized) && !errors.Is(r.err, context.Canceled) {
				t.Errorf("racing admission = %v, want rejection or cancellation", r.err)
			}
		}
		if _, err := earlier.Next(); !errors.Is(err, context.Canceled) {
			t.Errorf("earlier admission survived revocation: %v", err)
		}
		if err := earlier.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-revoked; err != nil {
			t.Fatal(err)
		}
		if _, err := f.executor.Stream(t.Context(), f.key, "later", f.request); !errors.Is(err, execution.ErrUnauthorized) {
			t.Errorf("later admission = %v, want unauthorized", err)
		}
	})
}

func TestFailedPersistenceDoesNotCancelActiveRequest(t *testing.T) {
	for _, tt := range []struct {
		name    string
		trigger string
		change  func(context.Context, *execution.Executor) error
	}{
		{name: "revoke", trigger: `CREATE TRIGGER reject_write BEFORE UPDATE OF enabled ON access_keys BEGIN SELECT RAISE(ABORT, 'write rejected'); END`,
			change: func(ctx context.Context, e *execution.Executor) error { return e.RevokeKey(ctx, "key") }},
		{name: "disable", trigger: `CREATE TRIGGER reject_write BEFORE UPDATE OF enabled ON accounts BEGIN SELECT RAISE(ABORT, 'write rejected'); END`,
			change: func(ctx context.Context, e *execution.Executor) error { return e.DisableAccount(ctx, "one") }},
		{name: "delete", trigger: `CREATE TRIGGER reject_write BEFORE DELETE ON accounts BEGIN SELECT RAISE(ABORT, 'write rejected'); END`,
			change: func(ctx context.Context, e *execution.Executor) error { return e.DeleteAccount(ctx, "one") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, "data: {\"type\":\"response.created\"}\n\n"), nil
			}))
			s := f.open(t, f.key)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(tt.trigger); err != nil {
				t.Fatal(err)
			}

			if err := tt.change(t.Context(), f.executor); err == nil {
				t.Fatal("management change succeeded despite rejected persistence")
			}
			if event, err := s.Next(); err != nil || !strings.Contains(string(event), "response.created") {
				t.Fatalf("active stream after failed write = %s, %v", event, err)
			}
			if _, err := f.executor.Stream(t.Context(), f.key, "overlap", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
				t.Fatalf("admission after failed write = %v, want existing slot limit", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			f.open(t, f.key)
		})
	}
}

func TestAccountRemovalCancelsWithoutFailover(t *testing.T) {
	for _, remove := range []string{"disable", "delete"} {
		t.Run(remove, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				body, unblock := newBlockedClose()
				defer unblock()
				f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
				}))
				f.addAccount(t, "two")
				s := f.open(t, f.key)
				removed := make(chan error, 1)

				go func() {
					if remove == "disable" {
						removed <- f.executor.DisableAccount(t.Context(), "one")
					} else {
						removed <- f.executor.DeleteAccount(t.Context(), "one")
					}
				}()
				<-body.closing
				synctest.Wait()
				select {
				case err := <-removed:
					t.Fatalf("account removal returned before stream cleanup: %v", err)
				default:
				}
				unblock()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if err := <-removed; err != nil {
					t.Fatal(err)
				}
				if _, err := s.Next(); !errors.Is(err, context.Canceled) {
					t.Fatalf("interrupted stream = %v, want cancellation", err)
				}
				if calls.Load() != 1 {
					t.Fatalf("generation calls = %d, want no failover", calls.Load())
				}
				if err := f.executor.DisableAccount(t.Context(), "two"); err != nil {
					t.Fatal(err)
				}
				models, err := f.executor.Models(t.Context(), f.key)
				if err != nil || len(models) != 0 {
					t.Fatalf("models after removing enabled accounts = %v, %v", models, err)
				}
				if _, err := f.executor.Stream(t.Context(), f.key, "removed", f.request); !errors.Is(err, execution.ErrNoAccounts) {
					t.Fatalf("generation after account removal = %v", err)
				}
				record, err := f.store.GetAccount(t.Context(), "one")
				if remove == "delete" && !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("deleted record = %v", err)
				}
				if remove == "disable" && (err != nil || record.Enabled) {
					t.Fatalf("disabled record enabled = %v, error = %v", record.Enabled, err)
				}
			})
		})
	}
}

func TestConcurrencyEditsPreserveExistingRequests(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "data: {\"type\":\"response.created\"}\n\n"), nil
	}))
	if err := f.executor.SetConcurrency(t.Context(), "key", 2); err != nil {
		t.Fatal(err)
	}
	first, second := f.open(t, f.key), f.open(t, f.key)

	if err := f.executor.SetConcurrency(t.Context(), "key", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Next(); err != nil {
		t.Fatalf("existing request after lowering limit = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executor.Stream(t.Context(), f.key, "still-full", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("admission with one remaining request = %v, want limit", err)
	}
	if err := f.executor.SetConcurrency(t.Context(), "key", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Next(); err != nil {
		t.Fatalf("existing request after zero limit = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executor.Stream(t.Context(), f.key, "zero", f.request); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("admission with zero limit and no active requests = %v", err)
	}
	if err := f.executor.SetConcurrency(t.Context(), "key", concurrency.Unlimited); err != nil {
		t.Fatal(err)
	}
	f.open(t, f.key)
	f.open(t, f.key)
}

func TestEnableRestoresEligibilityWithoutRevivingCanceledWork(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "data: {\"type\":\"response.created\"}\n\n"), nil
	}))
	previous := f.open(t, f.key)
	disabled := make(chan error, 1)
	go func() { disabled <- f.executor.DisableAccount(t.Context(), "one") }()
	<-previous.Context().Done()
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	models, err := f.executor.AvailableModels(t.Context())
	if err != nil || len(models) != 0 {
		t.Fatalf("disabled catalog = %v, %v; want empty", models, err)
	}

	if err := f.executor.EnableAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	models, err = f.executor.AvailableModels(t.Context())
	if err != nil || len(models) != 1 || models[0].ID != "model" {
		t.Fatalf("enabled catalog = %v, %v; want model", models, err)
	}
	if _, err := previous.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("old stream after enable = %v, want canceled", err)
	}
	current := f.open(t, f.key)
	if err := f.executor.EnableAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if event, err := current.Next(); err != nil || !strings.Contains(string(event), "response.created") {
		t.Fatalf("active stream after repeated enable = %s, %v", event, err)
	}
	if err := f.executor.EnableAccount(t.Context(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("enable missing account = %v, want not found", err)
	}
}

func TestCloseWaitsForActiveCleanupAndRejectsNewWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body, unblock := newBlockedClose()
		defer unblock()
		f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
		}))
		s := f.open(t, f.key)
		closed := make(chan struct{})

		go func() { f.executor.Close(); close(closed) }()
		<-body.closing
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("executor Close returned before stream cleanup")
		default:
		}
		if _, err := f.executor.Stream(t.Context(), f.key, "closed", f.request); !errors.Is(err, execution.ErrClosed) {
			t.Fatalf("admission during Close = %v", err)
		}
		if _, err := f.executor.Models(t.Context(), f.key); !errors.Is(err, execution.ErrClosed) {
			t.Fatalf("model discovery during Close = %v", err)
		}
		unblock()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("executor Close returned before downstream delivery Close")
		default:
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		<-closed
		if _, err := s.Next(); !errors.Is(err, context.Canceled) {
			t.Fatalf("stream after executor Close = %v", err)
		}
		f.executor.Close()
	})
}

type blockedCloseBody struct {
	closing chan struct{}
	release <-chan struct{}
	closes  atomic.Int32
}

func newBlockedClose() (*blockedCloseBody, func()) {
	release := make(chan struct{})
	return &blockedCloseBody{closing: make(chan struct{}), release: release}, sync.OnceFunc(func() { close(release) })
}

func (*blockedCloseBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *blockedCloseBody) Close() error {
	if b.closes.Add(1) == 1 {
		close(b.closing)
	}
	<-b.release
	return nil
}
