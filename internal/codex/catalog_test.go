package codex_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/retry"
)

func catalog(t *testing.T, client *codex.Client, accounts ...account.Account) *codex.Catalog {
	t.Helper()
	c, err := codex.NewCatalog(client.FetchModels)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.SetAccounts(accounts); err != nil {
		t.Fatal(err)
	}
	return c
}

const modelJSON = `{"models":[{"slug":"m","display_name":"Model","visibility":"list","supported_in_api":false,"supported_reasoning_levels":[{"effort":"low"}],"support_verbosity":true}]}`

func TestFetchModelsUsesAuthenticatedCodexCatalog(t *testing.T) {
	seen := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[
			{"slug":"visible","display_name":"Visible","visibility":"list","supported_in_api":false,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"support_verbosity":true,"supports_image_detail_original":true},
			{"slug":"hidden","visibility":"hide","supports_reasoning_summary_parameter":false,"input_modalities":["text"]}
		]}`)
	}))
	defer server.Close()
	client := testClient(t, server.Client(), server.URL+"/backend-api/codex")

	models, err := client.FetchModels(t.Context(), testAccount(t, "one"))

	if err != nil {
		t.Fatal(err)
	}
	want := []codex.Model{
		{ID: "visible", Name: "Visible", Listed: true, ReasoningEfforts: []string{"low", "high"}, InputModalities: []string{"text", "image"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportsImageDetailOriginal: true},
		{ID: "hidden", InputModalities: []string{"text"}},
	}
	if !reflect.DeepEqual(models, want) {
		t.Errorf("models = %+v, want %+v", models, want)
	}
	r := <-seen
	if r.Method != "GET" || r.URL.Path != "/backend-api/codex/models" || r.URL.Query().Get("client_version") != "0.99.0" {
		t.Errorf("wrong catalog endpoint: %s %s", r.Method, r.URL)
	}
	if r.Header.Get("Authorization") != "Bearer secret-one" || r.Header.Get("ChatGPT-Account-Id") != "chatgpt-one" {
		t.Error("missing catalog credentials")
	}
}

func TestFetchModelsFailureRetainsRetryTiming(t *testing.T) {
	retryAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, tt := range []struct{ name, body string }{
		{name: "invalid JSON", body: `{`},
		{name: "missing models", body: `{}`},
		{name: "blank ID", body: `{"models":[{"slug":"","visibility":"list"}]}`},
		{name: "duplicate ID", body: `{"models":[{"slug":"m","visibility":"list"},{"slug":"m","visibility":"list"}]}`},
		{name: "invalid visibility", body: `{"models":[{"slug":"m","visibility":"invalid"}]}`},
		{name: "invalid modality", body: `{"models":[{"slug":"m","visibility":"list","input_modalities":["invalid"]}]}`},
		{name: "blank effort", body: `{"models":[{"slug":"m","visibility":"list","supported_reasoning_levels":[{"effort":""}]}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				response := httpResponse(200, "application/json", tt.body)
				response.Header.Set("Retry-After", retryAt.Format(http.TimeFormat))
				return response, nil
			})}, "https://example.test")

			_, err := client.FetchModels(t.Context(), testAccount(t, "one"))

			var failure *codex.Failure
			if !errors.As(err, &failure) || failure.Category != codex.InvalidResponse || failure.HTTPStatus != 200 || failure.SafeToRetry {
				t.Fatalf("catalog failure = %v, want invalid response with status 200 and no safe replay", err)
			}
			if failure.RetryAfter.Kind != retry.RetryAt || !failure.RetryAfter.Until.Equal(retryAt) {
				t.Errorf("retry timing = %+v, want %v", failure.RetryAfter, retryAt)
			}
		})
	}
}

func TestCatalogCombinesKnownAccountsAndReportsDiscoveryFailure(t *testing.T) {
	for _, tt := range []struct {
		name, good  string
		unavailable bool
	}{
		{name: "partial success", good: modelJSON},
		{name: "successful empty", good: `{"models":[]}`},
		{name: "all failed", unavailable: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("ChatGPT-Account-Id") == "chatgpt-one" && tt.good != "" {
					return httpResponse(200, "application/json", tt.good), nil
				}
				return httpResponse(503, "application/json", "secret"), nil
			})}, "https://example.test")
			c := catalog(t, client, testAccount(t, "one"), testAccount(t, "two"))

			snapshot, err := c.Snapshot(t.Context())

			if errors.Is(err, codex.ErrCatalogUnavailable) != tt.unavailable {
				t.Errorf("Snapshot error: %v", err)
			}
			if len(snapshot.Accounts) != 2 || snapshot.Accounts[1].Failure == nil {
				t.Fatalf("missing per-account failures: %+v", snapshot)
			}
			if tt.good != "" && !snapshot.Accounts[0].Loaded {
				t.Error("successful empty catalog not loaded")
			}
			if tt.good == modelJSON && (len(snapshot.Listed) != 1 || snapshot.Listed[0].ID != "m") {
				t.Errorf("wrong union: %+v", snapshot.Listed)
			}
		})
	}
}

func TestCatalogCachesFailuresAndRetainsStaleOnInterruptedRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			n := calls.Add(1)
			if n == 2 {
				response := httpResponse(200, "application/json", "")
				response.Body = io.NopCloser(io.MultiReader(strings.NewReader(`{"models":`), failedReader{}))
				return response, nil
			}
			return httpResponse(200, "application/json", modelJSON), nil
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))
		first, err := c.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		first.Accounts[0].Models[0].ReasoningEfforts[0] = "mutated"
		first.Listed[0].InputModalities[0] = "mutated"
		time.Sleep(5 * time.Minute)

		stale, err := c.Snapshot(t.Context())
		cached, cacheErr := c.Snapshot(t.Context())

		if err != nil || cacheErr != nil {
			t.Fatalf("stale catalog lost: %v %v", err, cacheErr)
		}
		if calls.Load() != 2 || len(stale.Listed) != 1 || stale.Accounts[0].Failure.Category != codex.TransportFailure {
			t.Fatalf("stale refresh: %+v, calls %d", stale, calls.Load())
		}
		if stale.Accounts[0].Models[0].ReasoningEfforts[0] != "low" || cached.Listed[0].InputModalities[0] != "text" {
			t.Error("snapshot mutation changed cache")
		}
		time.Sleep(5 * time.Minute)
		recovered, err := c.Snapshot(t.Context())
		if err != nil || recovered.Accounts[0].Failure != nil || calls.Load() != 3 {
			t.Errorf("refresh did not recover: %+v, %v", recovered, err)
		}
	})
}

func TestCatalogRefreshFailureInvalidatesOnlyPermanentFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		loaded bool
	}{
		{name: "unauthorized", status: 401, loaded: false},
		{name: "forbidden", status: 403, loaded: false},
		{name: "rate limited", status: 429, loaded: true},
		{name: "unavailable", status: 503, loaded: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
					if calls.Add(1) == 1 {
						return httpResponse(200, "application/json", modelJSON), nil
					}
					return httpResponse(tt.status, "application/json", "failure"), nil
				})
				client := testClient(t, &http.Client{Transport: transport}, "https://example.test")
				c := catalog(t, client, testAccount(t, "one"))
				first, err := c.Snapshot(t.Context())
				if err != nil || len(first.Listed) != 1 {
					t.Fatalf("initial catalog = %+v, %v; want a populated catalog", first, err)
				}
				time.Sleep(5 * time.Minute)

				snapshot, err := c.Snapshot(t.Context())

				if tt.loaded && err != nil {
					t.Errorf("transient refresh error = %v, want nil", err)
				}
				if !tt.loaded && !errors.Is(err, codex.ErrCatalogUnavailable) {
					t.Errorf("permanent refresh error = %v, want ErrCatalogUnavailable", err)
				}
				if len(snapshot.Accounts) != 1 {
					t.Fatalf("accounts = %+v, want one account", snapshot.Accounts)
				}
				got := snapshot.Accounts[0]
				if got.Loaded != tt.loaded {
					t.Errorf("Loaded = %v, want %v", got.Loaded, tt.loaded)
				}
				if got.Failure == nil || got.Failure.HTTPStatus != tt.status {
					t.Errorf("failure = %+v, want HTTP status %d", got.Failure, tt.status)
				}
				if tt.loaded {
					if !reflect.DeepEqual(got.Models, first.Accounts[0].Models) ||
						!reflect.DeepEqual(snapshot.Listed, first.Listed) {
						t.Errorf("transient refresh lost last-good models: %+v", snapshot)
					}
				} else if len(got.Models) != 0 || len(snapshot.Listed) != 0 {
					t.Errorf("permanent refresh retained old models: %+v", snapshot)
				}

				// Once an account has loaded, retries follow the standard 5-minute interval
				// rather than rapid initial backoff, even if a subsequent refresh permanently failed.
				time.Sleep(1 * time.Minute)
				_, _ = c.Snapshot(t.Context())
				if calls.Load() != 2 {
					t.Errorf("calls = %d after 1 minute, want 2 (no rapid retry after prior load)", calls.Load())
				}
				time.Sleep(4 * time.Minute)
				_, _ = c.Snapshot(t.Context())
				if calls.Load() != 3 {
					t.Errorf("calls = %d after 5 minutes, want 3 (retried on regular schedule)", calls.Load())
				}
			})
		})
	}
}

func TestCatalogSuccessfulEmptyRefreshReplacesPopulatedCatalog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return httpResponse(200, "application/json", modelJSON), nil
			}
			return httpResponse(200, "application/json", `{"models":[]}`), nil
		})
		client := testClient(t, &http.Client{Transport: transport}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))
		first, err := c.Snapshot(t.Context())
		if err != nil || len(first.Listed) != 1 {
			t.Fatalf("initial catalog = %+v, %v; want a populated catalog", first, err)
		}
		time.Sleep(5 * time.Minute)

		snapshot, err := c.Snapshot(t.Context())

		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Accounts) != 1 {
			t.Fatalf("accounts = %+v, want one account", snapshot.Accounts)
		}
		got := snapshot.Accounts[0]
		if !got.Loaded || got.Failure != nil {
			t.Errorf("empty refresh = %+v, want loaded without failure", got)
		}
		if len(got.Models) != 0 || len(snapshot.Listed) != 0 {
			t.Errorf("empty refresh retained old models: %+v", snapshot)
		}
	})
}

func TestCatalogSharesRefreshWithoutSharingWaiterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			select {
			case <-release:
				return httpResponse(200, "application/json", modelJSON), nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		first := make(chan error, 1)
		second := make(chan error, 1)
		go func() { _, err := c.Snapshot(ctx); first <- err }()
		go func() { _, err := c.Snapshot(t.Context()); second <- err }()
		synctest.Wait()

		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Errorf("waiter cancellation: %v", err)
		}
		close(release)

		if err := <-second; err != nil {
			t.Errorf("other waiter poisoned: %v", err)
		}
		if calls.Load() != 1 {
			t.Errorf("shared refresh sent %d requests", calls.Load())
		}
	})
}

func TestCatalogAttemptTimesOutAfterFiveSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))
		start := time.Now()

		snapshot, err := c.Snapshot(t.Context())

		if !errors.Is(err, codex.ErrCatalogUnavailable) || time.Since(start) != 5*time.Second {
			t.Fatalf("timeout took %v: %v", time.Since(start), err)
		}
		if !errors.Is(snapshot.Accounts[0].Failure, context.DeadlineExceeded) {
			t.Error("deadline cause lost")
		}
		if calls.Load() != 1 {
			t.Errorf("calls = %d, want 1", calls.Load())
		}
	})
}

func TestCatalogFirstLoadFailureRetriesAfterOneSecondWhenHealthy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return httpResponse(500, "application/json", "temporary failure"), nil
			}
			return httpResponse(200, "application/json", modelJSON), nil
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))

		_, err := c.Snapshot(t.Context())
		if !errors.Is(err, codex.ErrCatalogUnavailable) {
			t.Fatalf("first snapshot error = %v, want ErrCatalogUnavailable", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("calls = %d, want 1", calls.Load())
		}

		time.Sleep(1 * time.Second)

		snapshot, err := c.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("second snapshot error = %v, want nil", err)
		}
		if calls.Load() != 2 {
			t.Fatalf("calls = %d, want 2", calls.Load())
		}
		if len(snapshot.Listed) != 1 || snapshot.Listed[0].ID != "m" {
			t.Errorf("snapshot listed = %+v, want model 'm'", snapshot.Listed)
		}
	})
}

func TestCatalogInitialFailureBackoffGrowsWhenProviderStaysDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return httpResponse(500, "application/json", "down"), nil
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))

		_, err := c.Snapshot(t.Context())
		if !errors.Is(err, codex.ErrCatalogUnavailable) || calls.Load() != 1 {
			t.Fatalf("call 1: err=%v, calls=%d", err, calls.Load())
		}

		var callHistory []int
		for step := 1; step <= 16; step++ {
			time.Sleep(500 * time.Millisecond)
			_, _ = c.Snapshot(t.Context())
			callHistory = append(callHistory, int(calls.Load()))
		}
		wantHistory := []int{1, 2, 2, 2, 2, 3, 3, 3, 3, 3, 3, 3, 3, 4, 4, 4}
		if !reflect.DeepEqual(callHistory, wantHistory) {
			t.Errorf("callHistory = %v, want %v", callHistory, wantHistory)
		}
	})
}

func TestCatalogRequestDuringWaitingPeriodAnswersImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))

		start := time.Now()
		_, err := c.Snapshot(t.Context())
		if !errors.Is(err, codex.ErrCatalogUnavailable) || time.Since(start) != 5*time.Second {
			t.Fatalf("first attempt took %v: %v", time.Since(start), err)
		}

		time.Sleep(200 * time.Millisecond)
		subsequentStart := time.Now()
		_, err = c.Snapshot(t.Context())
		if !errors.Is(err, codex.ErrCatalogUnavailable) {
			t.Fatalf("second call error = %v, want ErrCatalogUnavailable", err)
		}
		if time.Since(subsequentStart) != 0 {
			t.Errorf("request during waiting period took %v, want 0", time.Since(subsequentStart))
		}
		if calls.Load() != 1 {
			t.Errorf("request during waiting period triggered call: %d", calls.Load())
		}
	})
}

func TestCatalogRefreshFailureRetainsLoadedModelsAndWaitsFiveMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return httpResponse(200, "application/json", modelJSON), nil
			}
			return httpResponse(500, "application/json", "error"), nil
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))

		first, err := c.Snapshot(t.Context())
		if err != nil || len(first.Listed) != 1 {
			t.Fatalf("initial snapshot = %+v, %v", first, err)
		}

		time.Sleep(5 * time.Minute)
		refreshFail, err := c.Snapshot(t.Context())
		if err != nil || len(refreshFail.Listed) != 1 {
			t.Fatalf("refresh failure lost old models: %+v, %v", refreshFail, err)
		}
		if calls.Load() != 2 {
			t.Fatalf("calls = %d, want 2", calls.Load())
		}

		time.Sleep(4 * time.Minute)
		cached, err := c.Snapshot(t.Context())
		if err != nil || len(cached.Listed) != 1 || calls.Load() != 2 {
			t.Fatalf("premature retry before 5 minutes: calls=%d, err=%v", calls.Load(), err)
		}
	})
}

func TestCatalogMultipleAccountsIsolatesFailingAccountRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var oneCalls, twoCalls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("ChatGPT-Account-Id") == "chatgpt-one" {
				oneCalls.Add(1)
				return httpResponse(200, "application/json", modelJSON), nil
			}
			twoCalls.Add(1)
			return httpResponse(500, "application/json", "fail"), nil
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"), testAccount(t, "two"))

		snapshot, err := c.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("snapshot error = %v, want nil", err)
		}
		if len(snapshot.Listed) != 1 || snapshot.Listed[0].ID != "m" {
			t.Fatalf("listed = %+v, want model 'm'", snapshot.Listed)
		}
		if oneCalls.Load() != 1 || twoCalls.Load() != 1 {
			t.Fatalf("calls: one=%d, two=%d", oneCalls.Load(), twoCalls.Load())
		}

		time.Sleep(1 * time.Second)
		snapshot2, err := c.Snapshot(t.Context())
		if err != nil || len(snapshot2.Listed) != 1 {
			t.Fatalf("snapshot2 error = %v", err)
		}
		if oneCalls.Load() != 1 {
			t.Errorf("account one retried prematurely: %d", oneCalls.Load())
		}
		if twoCalls.Load() != 2 {
			t.Errorf("account two did not retry on schedule: %d", twoCalls.Load())
		}
	})
}

func TestCatalogReconnectClearsWaitAndFailureCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			if r.Header.Get("Authorization") == "Bearer secret-reconnected" {
				return httpResponse(200, "application/json", modelJSON), nil
			}
			return httpResponse(500, "application/json", "error"), nil
		})}, "https://example.test")
		a := testAccount(t, "one")
		c := catalog(t, client, a)

		_, _ = c.Snapshot(t.Context())
		time.Sleep(1 * time.Second)
		_, _ = c.Snapshot(t.Context())
		time.Sleep(2 * time.Second)
		_, _ = c.Snapshot(t.Context())
		if calls.Load() != 3 {
			t.Fatalf("calls = %d, want 3", calls.Load())
		}

		creds := a.Credentials()
		creds.AccessToken = "secret-reconnected"
		reconnected, err := account.New(a.Identity(), creds)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SetAccounts([]account.Account{reconnected}); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		snapshot, err := c.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("snapshot error after reconnect = %v", err)
		}
		if time.Since(start) != 0 {
			t.Errorf("reconnect snapshot delayed by %v", time.Since(start))
		}
		if calls.Load() != 4 {
			t.Errorf("calls = %d, want 4", calls.Load())
		}
		if len(snapshot.Listed) != 1 || snapshot.Listed[0].ID != "m" {
			t.Errorf("listed = %+v, want model 'm'", snapshot.Listed)
		}
	})
}

func TestCatalogDisabledAccountWhileWaitingDoesNotDelayShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return httpResponse(500, "application/json", "error"), nil
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))

		_, _ = c.Snapshot(t.Context())
		if calls.Load() != 1 {
			t.Fatalf("calls = %d, want 1", calls.Load())
		}

		if err := c.SetAccounts(nil); err != nil {
			t.Fatal(err)
		}

		time.Sleep(2 * time.Second)
		_, _ = c.Snapshot(t.Context())
		_, _ = c.Snapshot(t.Context())
		if calls.Load() != 1 {
			t.Errorf("calls = %d, want no further calls after account is removed", calls.Load())
		}

		start := time.Now()
		c.Close()
		if time.Since(start) != 0 {
			t.Errorf("Close was delayed by %v, want 0", time.Since(start))
		}
	})
}

func TestCatalogConcurrentRequestsShareInitialDiscovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			<-release
			return httpResponse(200, "application/json", modelJSON), nil
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))

		errs := make(chan error, 3)
		for range 3 {
			go func() {
				_, err := c.Snapshot(t.Context())
				errs <- err
			}()
		}
		synctest.Wait()
		close(release)

		for range 3 {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent snapshot error = %v", err)
			}
		}
		if calls.Load() != 1 {
			t.Errorf("concurrent calls = %d, want 1", calls.Load())
		}
	})
}

func TestCatalogDeletionCannotBeUndoneByLateRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			<-release
			return httpResponse(200, "application/json", modelJSON), nil
		})}, "https://example.test")
		c := catalog(t, client, testAccount(t, "one"))
		finished := make(chan error, 1)
		go func() { _, err := c.Snapshot(t.Context()); finished <- err }()
		synctest.Wait()

		if err := c.SetAccounts(nil); err != nil {
			t.Fatal(err)
		}
		before, err := c.Snapshot(t.Context())
		if err != nil || len(before.Listed) != 0 || len(before.Accounts) != 0 {
			t.Fatalf("deleted account still visible: %+v, %v", before, err)
		}
		close(release)
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
		after, err := c.Snapshot(t.Context())

		if err != nil || len(after.Listed) != 0 || len(after.Accounts) != 0 {
			t.Errorf("late completion reintroduced deleted account: %+v, %v", after, err)
		}
	})
}

func TestCatalogCredentialRotationRejectsOldCompletionAndKeepsLastGood(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		releaseOld := make(chan struct{})
		var calls atomic.Int32
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			n := calls.Add(1)
			if n == 2 {
				<-releaseOld
				return httpResponse(200, "application/json", `{"models":[{"slug":"late","visibility":"list"}]}`), nil
			}
			if r.Header.Get("Authorization") == "Bearer rotated" {
				return httpResponse(503, "application/json", "temporary"), nil
			}
			return httpResponse(200, "application/json", modelJSON), nil
		})}, "https://example.test")
		a := testAccount(t, "one")
		c := catalog(t, client, a)
		if _, err := c.Snapshot(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Minute)
		oldDone := make(chan error, 1)
		go func() { _, err := c.Snapshot(t.Context()); oldDone <- err }()
		synctest.Wait()
		credentials := a.Credentials()
		credentials.AccessToken = "rotated"
		rotated, err := account.New(a.Identity(), credentials)
		if err != nil {
			t.Fatal(err)
		}

		if err := c.SetAccounts([]account.Account{rotated}); err != nil {
			t.Fatal(err)
		}
		current, err := c.Snapshot(t.Context())
		close(releaseOld)
		if oldErr := <-oldDone; oldErr != nil {
			t.Fatal(oldErr)
		}
		after, afterErr := c.Snapshot(t.Context())

		if err != nil || afterErr != nil || len(current.Listed) != 1 || len(after.Listed) != 1 || after.Listed[0].ID != "m" {
			t.Fatalf("rotation lost/stomped last good: %+v, %v %v", after, err, afterErr)
		}
		if calls.Load() != 3 {
			t.Errorf("rotation request count: %d", calls.Load())
		}
	})
}

func TestCatalogChangedChatGPTAccountClearsLastGoodOnDiscoveryFailure(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("ChatGPT-Account-Id") == "chatgpt-one" {
			return httpResponse(200, "application/json", modelJSON), nil
		}
		return httpResponse(503, "application/json", "temporary"), nil
	})
	client := testClient(t, &http.Client{Transport: transport}, "https://example.test")
	a := testAccount(t, "one")
	c := catalog(t, client, a)
	first, err := c.Snapshot(t.Context())
	if err != nil || len(first.Listed) != 1 {
		t.Fatalf("initial catalog = %+v, %v; want a populated catalog", first, err)
	}
	credentials := a.Credentials()
	credentials.ChatGPTAccountID = "chatgpt-two"
	replacement, err := account.New(a.Identity(), credentials)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.SetAccounts([]account.Account{replacement}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.Snapshot(t.Context())

	if !errors.Is(err, codex.ErrCatalogUnavailable) {
		t.Errorf("replacement discovery error = %v, want ErrCatalogUnavailable", err)
	}
	if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].AccountID != a.Identity().ID {
		t.Fatalf("accounts = %+v, want the same local account ID", snapshot.Accounts)
	}
	got := snapshot.Accounts[0]
	if got.Loaded {
		t.Error("replacement account inherited loaded state")
	}
	if got.Failure == nil || got.Failure.HTTPStatus != 503 {
		t.Errorf("failure = %+v, want HTTP status 503", got.Failure)
	}
	if len(got.Models) != 0 || len(snapshot.Listed) != 0 {
		t.Errorf("replacement account inherited old models: %+v", snapshot)
	}
}

func TestCatalogRenewalRetainsFiveMinuteRetryIntervalOnFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		loaded bool
	}{
		{name: "transient_500", status: 500, loaded: true},
		{name: "permanent_403", status: 403, loaded: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
					if calls.Add(1) == 1 {
						return httpResponse(200, "application/json", modelJSON), nil
					}
					return httpResponse(tt.status, "application/json", "failure"), nil
				})
				client := testClient(t, &http.Client{Transport: transport}, "https://example.test")
				a := testAccount(t, "one")
				c := catalog(t, client, a)

				first, err := c.Snapshot(t.Context())
				if err != nil || len(first.Listed) != 1 {
					t.Fatalf("initial catalog = %+v, %v; want populated catalog", first, err)
				}
				if calls.Load() != 1 {
					t.Fatalf("initial calls = %d, want 1", calls.Load())
				}

				credentials := a.Credentials()
				credentials.AccessToken = "renewed-token"
				renewed, err := account.New(a.Identity(), credentials)
				if err != nil {
					t.Fatal(err)
				}
				if err := c.SetAccounts([]account.Account{renewed}); err != nil {
					t.Fatal(err)
				}

				snapshot, err := c.Snapshot(t.Context())
				if tt.loaded && (err != nil || len(snapshot.Listed) != 1) {
					t.Errorf("transient refresh = %+v, %v; want retained models", snapshot, err)
				}
				if !tt.loaded && (!errors.Is(err, codex.ErrCatalogUnavailable) || len(snapshot.Listed) != 0) {
					t.Errorf("permanent refresh = %+v, %v; want ErrCatalogUnavailable and empty models", snapshot, err)
				}
				if calls.Load() != 2 {
					t.Fatalf("calls after renewal = %d, want 2 (immediate refresh attempt)", calls.Load())
				}

				time.Sleep(1 * time.Minute)
				_, _ = c.Snapshot(t.Context())
				if calls.Load() != 2 {
					t.Errorf("calls after 1 minute = %d, want 2 (renewal must not shorten retry gap)", calls.Load())
				}

				time.Sleep(4 * time.Minute)
				_, _ = c.Snapshot(t.Context())
				if calls.Load() != 3 {
					t.Errorf("calls after 5 minutes = %d, want 3 (retried on 5-minute schedule)", calls.Load())
				}
			})
		})
	}
}

func TestCatalogSeparatesVisibleUnionFromAccountCapabilities(t *testing.T) {
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("ChatGPT-Account-Id") == "chatgpt-one" {
			return httpResponse(200, "application/json", `{"models":[{"slug":"shared","visibility":"list","support_verbosity":true},{"slug":"hidden","visibility":"hide"}]}`), nil
		}
		return httpResponse(200, "application/json", `{"models":[{"slug":"shared","visibility":"list","support_verbosity":false}]}`), nil
	})}, "https://example.test")
	one, two := testAccount(t, "one"), testAccount(t, "two")
	c := catalog(t, client, one, two)

	snapshot, err := c.Snapshot(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Listed) != 1 || snapshot.Listed[0].ID != "shared" || len(snapshot.Accounts[0].Models) != 2 {
		t.Fatalf("wrong visible/full catalogs: %+v", snapshot)
	}
	verbose := request(t, `{"model":"shared","input":"hi","text":{"verbosity":"low"}}`)
	if err := verbose.ValidateModel(snapshot.Accounts[0].Models[0]); err != nil {
		t.Errorf("first account capability: %v", err)
	}
	if err := verbose.ValidateModel(snapshot.Accounts[1].Models[0]); err == nil {
		t.Error("capabilities incorrectly merged across accounts")
	}
	hidden := request(t, `{"model":"hidden","input":"hi"}`)
	if err := hidden.ValidateModel(snapshot.Accounts[0].Models[1]); err != nil {
		t.Errorf("hidden model not usable: %v", err)
	}
	if err := c.SetAccounts([]account.Account{two}); err != nil {
		t.Fatal(err)
	}
	after, err := c.Snapshot(t.Context())
	if err != nil || len(after.Accounts) != 1 || len(after.Accounts[0].Models) != 1 {
		t.Errorf("disabled account contributes: %+v, %v", after, err)
	}
}

func TestCatalogLoadsAccountsAddedWhileSnapshotWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("ChatGPT-Account-Id") == "chatgpt-one" {
				<-release
			}
			return httpResponse(200, "application/json", modelJSON), nil
		})}, "https://example.test")
		one, two := testAccount(t, "one"), testAccount(t, "two")
		c := catalog(t, client, one)
		done := make(chan codex.CatalogSnapshot, 1)
		go func() { result, _ := c.Snapshot(t.Context()); done <- result }()
		synctest.Wait()

		if err := c.SetAccounts([]account.Account{one, two}); err != nil {
			t.Fatal(err)
		}
		close(release)
		snapshot := <-done

		if len(snapshot.Accounts) != 2 || !snapshot.Accounts[1].Loaded {
			t.Errorf("new enabled account missed discovery: %+v", snapshot)
		}
	})
}

func TestCatalogCloseWaitsForCanceledRefreshCleanup(t *testing.T) {
	for _, state := range []string{"active", "removed", "rotated"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				canceled := make(chan struct{})
				releaseCleanup := sync.OnceFunc(func() { close(release) })
				var calls atomic.Int32
				client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					<-r.Context().Done()
					close(canceled)
					<-release
					return nil, r.Context().Err()
				})}, "https://example.test")
				a := testAccount(t, "one")
				c := catalog(t, client, a)
				defer releaseCleanup()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				waiter := make(chan error, 1)
				go func() { _, err := c.Snapshot(ctx); waiter <- err }()
				synctest.Wait()
				cancel()
				select {
				case err := <-waiter:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("waiter cancellation: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("canceled waiter did not return")
				}
				switch state {
				case "removed":
					if err := c.SetAccounts(nil); err != nil {
						t.Fatal(err)
					}
				case "rotated":
					credentials := a.Credentials()
					credentials.AccessToken = "rotated"
					rotated, err := account.New(a.Identity(), credentials)
					if err != nil {
						t.Fatal(err)
					}
					if err := c.SetAccounts([]account.Account{rotated}); err != nil {
						t.Fatal(err)
					}
				}
				closed := make(chan struct{}, 2)
				for range 2 {
					go func() { c.Close(); closed <- struct{}{} }()
				}
				synctest.Wait()

				if len(closed) != 0 {
					t.Error("Close returned before refresh cleanup")
				}
				select {
				case <-canceled:
				default:
					t.Error("Close did not cancel the refresh")
				}
				if _, err := c.Snapshot(t.Context()); !errors.Is(err, codex.ErrCatalogClosed) {
					t.Errorf("Snapshot during Close: %v", err)
				}
				if err := c.SetAccounts([]account.Account{a}); !errors.Is(err, codex.ErrCatalogClosed) {
					t.Errorf("SetAccounts during Close: %v", err)
				}
				releaseCleanup()
				for range 2 {
					select {
					case <-closed:
					case <-time.After(10 * time.Second):
						t.Fatal("Close did not return after refresh cleanup")
					}
				}
				c.Close()
				if _, err := c.Snapshot(t.Context()); !errors.Is(err, codex.ErrCatalogClosed) {
					t.Errorf("Snapshot after Close: %v", err)
				}
				if err := c.SetAccounts([]account.Account{a}); !errors.Is(err, codex.ErrCatalogClosed) {
					t.Errorf("SetAccounts after Close: %v", err)
				}
				if calls.Load() != 1 {
					t.Errorf("closed catalog sent %d requests, want 1", calls.Load())
				}
			})
		})
	}
}

func TestCatalogSnapshotListedAndAccountModelsAreIndependent(t *testing.T) {
	c, err := codex.NewCatalog(func(context.Context, account.Account) ([]codex.Model, error) {
		return []codex.Model{{
			ID:               "m",
			Listed:           true,
			ReasoningEfforts: []string{"low"},
			InputModalities:  []string{"text"},
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)

	if err := c.SetAccounts([]account.Account{testAccount(t, "one")}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := c.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Accounts) != 1 ||
		len(snapshot.Accounts[0].Models) != 1 ||
		len(snapshot.Listed) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}

	accountModel := &snapshot.Accounts[0].Models[0]
	listedModel := &snapshot.Listed[0]

	accountModel.ReasoningEfforts[0] = "changed-account"
	accountModel.InputModalities[0] = "changed-account"

	if listedModel.ReasoningEfforts[0] != "low" ||
		listedModel.InputModalities[0] != "text" {
		t.Error("mutating account models changed the listed union")
	}

	listedModel.ReasoningEfforts[0] = "changed-listed"
	listedModel.InputModalities[0] = "changed-listed"

	if accountModel.ReasoningEfforts[0] != "changed-account" ||
		accountModel.InputModalities[0] != "changed-account" {
		t.Error("mutating the listed union changed account models")
	}

	fresh, err := c.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, model := range []codex.Model{
		fresh.Accounts[0].Models[0],
		fresh.Listed[0],
	} {
		if model.ReasoningEfforts[0] != "low" ||
			model.InputModalities[0] != "text" {
			t.Errorf("snapshot mutation changed cached models: %+v", model)
		}
	}
}
