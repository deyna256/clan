package execution_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/execution"
)

const completed = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n"

func TestRetriesOnlySafeAccountFailures(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		wantCalls int
	}{
		{"unauthorized", 401, 3}, {"forbidden", 403, 3}, {"limited", 429, 3},
		{"bad input", 400, 1}, {"model rejected", 404, 1}, {"invalid input", 422, 1},
		{"unknown server outcome", 500, 1}, {"unavailable server", 503, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var accounts []string
			f := newFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				accounts = append(accounts, r.Header.Get("ChatGPT-Account-Id"))
				return response(tt.status, `{}`), nil
			}))
			f.addAccount(t, "two")
			f.addAccount(t, "three")
			f.addAccount(t, "z")

			result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "retry"}, f.request)
			defer result.Close()

			var failure *codex.Failure
			if !errors.As(err, &failure) || failure.HTTPStatus != tt.status {
				t.Fatalf("failure = %v, want HTTP %d", err, tt.status)
			}
			want := []string{"one", "three", "two"}[:tt.wantCalls]
			if !reflect.DeepEqual(accounts, want) {
				t.Fatalf("attempt accounts = %v, want %v", accounts, want)
			}
		})
	}
}

func TestSafeRetryClosesPreviousAttemptBeforeDispatch(t *testing.T) {
	closed := false
	var accounts []string
	f := newFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		accounts = append(accounts, r.Header.Get("ChatGPT-Account-Id"))
		if len(accounts) == 1 {
			resp := response(429, `{}`)
			resp.Body = &observedClose{ReadCloser: resp.Body, close: func() { closed = true }}
			return resp, nil
		}
		if !closed {
			return nil, errors.New("previous body is still open")
		}
		return response(200, completed), nil
	}))
	f.addAccount(t, "two")

	result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "retry"}, f.request)
	defer result.Close()

	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accounts, []string{"one", "two"}) {
		t.Fatalf("attempts = %v", accounts)
	}
	if !result.Usage.Total.Known || result.Usage.Total.Tokens != 10 {
		t.Fatalf("usage = %+v, want known total10", result.Usage)
	}
}

func TestTransportFailureNeverRetries(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, cause
			}))
			f.addAccount(t, "two")

			result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "unknown"}, f.request)
			defer result.Close()

			var failure *codex.Failure
			if !errors.As(err, &failure) || failure.SafeToRetry || calls != 1 {
				t.Fatalf("failure = %v, attempts = %d", err, calls)
			}
		})
	}
}

func TestCooldownExpiresOnDemand(t *testing.T) {
	for _, tt := range []struct {
		name, header string
		delay        time.Duration
	}{
		{"fallback", "", time.Minute}, {"provider", "120", 2 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					if calls > 1 {
						return response(200, completed), nil
					}
					resp := response(429, `{}`)
					resp.Header.Set("Retry-After", tt.header)
					return resp, nil
				}))

				limited, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "limited"}, f.request)
				defer limited.Close()
				if err == nil {
					t.Fatal("rate limit succeeded")
				}
				if err := limited.Close(); err != nil {
					t.Fatal(err)
				}
				time.Sleep(tt.delay - time.Nanosecond)
				early, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "early"}, f.request)
				defer early.Close()
				if !errors.Is(err, execution.ErrNoAccounts) {
					t.Fatalf("early retry = %v", err)
				}
				if err := early.Close(); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatalf("cooldown sent %d upstream requests", calls)
				}
				models, err := f.executor.Models(t.Context(), f.key)
				if err != nil || len(models) != 1 {
					t.Fatalf("cooldown hid model: %v, %v", models, err)
				}
				time.Sleep(time.Nanosecond)
				recovered, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "recovered"}, f.request)
				defer recovered.Close()
				if err != nil {
					t.Fatal(err)
				}
				if calls != 2 {
					t.Fatalf("attempts after expiry = %d", calls)
				}
			})
		})
	}
}

func TestTerminalFailurePreservesUsageAndCoolsAccountWithoutReplay(t *testing.T) {
	var accounts []string
	f := newFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		accounts = append(accounts, r.Header.Get("ChatGPT-Account-Id"))
		if len(accounts) > 1 {
			return response(200, completed), nil
		}
		return response(200, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"r\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"secret-body\"},\"usage\":{\"input_tokens\":9,\"output_tokens\":0}}}\n\n"), nil
	}))
	f.addAccount(t, "two")
	otherKey := f.addKey(t, "other", 1)

	result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "terminal"}, f.request)
	defer result.Close()

	var failure *codex.Failure
	if !errors.As(err, &failure) || failure.SafeToRetry || failure.Category != codex.ProviderLimit {
		t.Fatalf("failure = %v", err)
	}
	if !result.Usage.Input.Known || result.Usage.Input.Tokens != 9 || !result.Usage.Output.Known || result.Usage.Output.Tokens != 0 || result.Usage.Total.Known {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if !reflect.DeepEqual(accounts, []string{"one"}) {
		t.Fatalf("failed generation replayed: %v", accounts)
	}
	for range 2 {
		next, err := f.executor.Generate(t.Context(), otherKey, execution.RequestInfo{ID: "next"}, f.request)
		closeErr := next.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("generation = %v, cleanup = %v", err, closeErr)
		}
	}
	if !reflect.DeepEqual(accounts, []string{"one", "two", "two"}) {
		t.Fatalf("next request used limited account: %v", accounts)
	}
}

func TestTerminalCooldownStartsBeforeDeliveryAndIsNotExtendedByClose(t *testing.T) {
	for _, tt := range []struct{ name, event string }{
		{name: "failed response", event: `{"type":"response.failed","response":{"id":"r","error":{"code":"rate_limit_exceeded"}}}`},
		{name: "error event", event: `{"type":"error","code":"rate_limit_exceeded"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return response(200, "data: "+tt.event+"\n\n"), nil
					}
					return response(200, completed), nil
				}))
				otherKey := f.addKey(t, "other", 1)
				stream := f.open(t, f.key)

				if event, err := stream.Next(); err != nil || len(event) == 0 {
					t.Fatalf("terminal event = %s, error = %v", event, err)
				}
				same, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "same-key"}, f.request)
				defer same.Close()
				if !errors.Is(err, concurrency.ErrLimitReached) {
					t.Fatalf("terminal delivery released slot: %v", err)
				}
				other, err := f.executor.Generate(t.Context(), otherKey, execution.RequestInfo{ID: "other-key"}, f.request)
				defer other.Close()
				if !errors.Is(err, execution.ErrNoAccounts) || calls != 1 {
					t.Fatalf("limited account admitted: error = %v, upstream calls = %d", err, calls)
				}
				time.Sleep(30 * time.Second)
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				time.Sleep(30 * time.Second)
				result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "recovered"}, f.request)
				defer result.Close()

				if err != nil || calls != 2 {
					t.Fatalf("recovery after one minute: error = %v, upstream calls = %d", err, calls)
				}
				failures := 0
				for _, entry := range logEntries(t, f.logs.String()) {
					if entry["msg"] == "attempt failed" {
						failures++
					}
				}
				if failures != 1 {
					t.Fatalf("attempt failure logged %d times, want once", failures)
				}
			})
		})
	}
}

func TestOverlappingFailuresCannotShortenCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls > 2 {
				return response(200, completed), nil
			}
			resp := response(200, "data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\"}\n\n")
			resp.Header.Set("Retry-After", "60")
			if calls == 1 {
				resp.Header.Set("Retry-After", "120")
			}
			return resp, nil
		}))
		if err := f.executor.SetConcurrency(t.Context(), "key", 2); err != nil {
			t.Fatal(err)
		}
		longer, shorter := f.open(t, f.key), f.open(t, f.key)

		for _, stream := range []*execution.Stream{longer, shorter} {
			if _, err := stream.Next(); err != nil {
				t.Fatal(err)
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Minute)
		early, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "early"}, f.request)
		defer early.Close()
		if !errors.Is(err, execution.ErrNoAccounts) {
			t.Fatalf("shorter failure replaced longer cooldown: %v", err)
		}
		if err := early.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		ready, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "ready"}, f.request)
		defer ready.Close()
		if err != nil {
			t.Fatal(err)
		}

		if calls != 3 {
			t.Fatalf("generation calls = %d, want two overlapping attempts and one after recovery", calls)
		}
	})
}

type observedClose struct {
	io.ReadCloser
	once  sync.Once
	close func()
}

func (b *observedClose) Close() error {
	b.once.Do(b.close)
	return b.ReadCloser.Close()
}
