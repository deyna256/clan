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

			_, err := f.executor.Generate(t.Context(), f.key, "retry", f.request)

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

	result, err := f.executor.Generate(t.Context(), f.key, "retry", f.request)

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

			_, err := f.executor.Generate(t.Context(), f.key, "unknown", f.request)

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

				if _, err := f.executor.Generate(t.Context(), f.key, "limited", f.request); err == nil {
					t.Fatal("rate limit succeeded")
				}
				time.Sleep(tt.delay - time.Nanosecond)
				if _, err := f.executor.Generate(t.Context(), f.key, "early", f.request); !errors.Is(err, execution.ErrNoAccounts) {
					t.Fatalf("early retry = %v", err)
				}
				if calls != 1 {
					t.Fatalf("cooldown sent %d upstream requests", calls)
				}
				models, err := f.executor.Models(t.Context(), f.key)
				if err != nil || len(models) != 1 {
					t.Fatalf("cooldown hid model: %v, %v", models, err)
				}
				time.Sleep(time.Nanosecond)
				if _, err := f.executor.Generate(t.Context(), f.key, "recovered", f.request); err != nil {
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

	result, err := f.executor.Generate(t.Context(), f.key, "terminal", f.request)

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
	if _, err := f.executor.Generate(t.Context(), f.key, "next", f.request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accounts, []string{"one", "two"}) {
		t.Fatalf("next request used limited account: %v", accounts)
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
		if _, err := f.executor.Generate(t.Context(), f.key, "early", f.request); !errors.Is(err, execution.ErrNoAccounts) {
			t.Fatalf("shorter failure replaced longer cooldown: %v", err)
		}
		time.Sleep(time.Minute)
		if _, err := f.executor.Generate(t.Context(), f.key, "ready", f.request); err != nil {
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
