package execution_test

import (
	"errors"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/execution"
)

func TestCheckKeyAuthenticatesWithoutDiscoveryOrSlotAcquisition(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, completed), nil
	})
	f := newFixtureWithCatalog(t, transport, transport)
	if err := f.executor.SetConcurrency(t.Context(), "key", 0); err != nil {
		t.Fatal(err)
	}

	if _, err := f.executor.CheckKey(t.Context(), f.key); err != nil {
		t.Fatalf("valid key with no available slots = %v", err)
	}
	if _, err := f.executor.Stream(t.Context(), f.key, execution.RequestInfo{ID: "zero"}, f.request); !errors.Is(err, concurrency.ErrLimitReached) {
		t.Fatalf("generation with no available slots = %v, want limit", err)
	}
	if _, err := f.executor.CheckKey(t.Context(), "invalid"); !errors.Is(err, execution.ErrUnauthorized) {
		t.Fatalf("invalid key = %v, want unauthorized", err)
	}
	if err := f.executor.RevokeKey(t.Context(), "key"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executor.CheckKey(t.Context(), f.key); !errors.Is(err, execution.ErrUnauthorized) {
		t.Fatalf("revoked key = %v, want unauthorized", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want none", calls.Load())
	}
	f.executor.Close()
	if _, err := f.executor.CheckKey(t.Context(), f.key); !errors.Is(err, execution.ErrClosed) {
		t.Fatalf("key check after shutdown = %v, want closed", err)
	}
}

func TestAdministrationAndClientsShareCatalog(t *testing.T) {
	var discoveries atomic.Int32
	f := newFixtureWithCatalog(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, completed), nil
	}), roundTripFunc(func(*http.Request) (*http.Response, error) {
		discoveries.Add(1)
		return response(200, `{"models":[{"slug":"model","visibility":"list"},{"slug":"hidden","visibility":"hide"}]}`), nil
	}))

	admin, err := f.executor.AvailableModels(t.Context())
	if err != nil || len(admin) != 1 || admin[0].ID != "model" {
		t.Fatalf("admin catalog = %v, %v; want visible model", admin, err)
	}
	admin[0].ID = "changed-by-caller"
	client, err := f.executor.Models(t.Context(), f.key)

	if err != nil || len(client) != 1 || client[0].ID != "model" {
		t.Fatalf("client catalog = %v, %v; want unchanged visible model", client, err)
	}
	if got := discoveries.Load(); got != 1 {
		t.Fatalf("catalog fetches = %d, want one shared fetch", got)
	}
	f.executor.Close()
	if _, err := f.executor.AvailableModels(t.Context()); !errors.Is(err, execution.ErrClosed) {
		t.Fatalf("admin catalog after shutdown = %v, want closed", err)
	}
}

func TestCatalogRefreshesOAuthBeforeDiscoveryAndGeneration(t *testing.T) {
	var calls []string
	f := newFixtureWithCatalog(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/oauth/token" {
			calls = append(calls, "refresh")
			resp := response(200, `{"access_token":"rotated","refresh_token":"new-refresh","expires_in":3600}`)
			resp.Header.Set("Content-Type", "application/json")
			return resp, nil
		}
		calls = append(calls, "generate:"+r.Header.Get("Authorization"))
		return response(200, completed), nil
	}), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, "catalog:"+r.Header.Get("Authorization"))
		return response(200, `{"models":[{"slug":"model","visibility":"list"}]}`), nil
	}))
	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	credentials := record.Account.Credentials()
	credentials.RefreshToken = "old-refresh"
	credentials.ExpiresAt = time.Now().Add(4 * time.Minute)
	if err := f.store.ReplaceAccountCredentialsIfUnchanged(t.Context(), record, credentials); err != nil {
		t.Fatal(err)
	}

	result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "fresh"}, f.request)
	defer result.Close()

	if err != nil {
		t.Fatal(err)
	}
	want := []string{"refresh", "catalog:Bearer rotated", "generate:Bearer rotated"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestSelectionUsesAccountCapabilitiesAndAllowsHiddenModel(t *testing.T) {
	var selected string
	f := newFixtureWithCatalog(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		selected = r.Header.Get("ChatGPT-Account-Id")
		return response(200, completed), nil
	}), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("ChatGPT-Account-Id") == "one" {
			return response(200, `{"models":[{"slug":"model","visibility":"hide","supported_reasoning_levels":[{"effort":"low"}]}]}`), nil
		}
		return response(200, `{"models":[{"slug":"model","visibility":"hide","supported_reasoning_levels":[{"effort":"high"}]}]}`), nil
	}))
	f.addAccount(t, "two")
	request, err := codex.ParseRequest([]byte(`{"model":"model","input":"hello","reasoning":{"effort":"high"}}`))
	if err != nil {
		t.Fatal(err)
	}

	models, err := f.executor.Models(t.Context(), f.key)
	if err != nil || len(models) != 0 {
		t.Fatalf("listed models = %v, error = %v", models, err)
	}
	result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "capability"}, request)
	defer result.Close()

	if err != nil {
		t.Fatal(err)
	}
	if selected != "two" {
		t.Fatalf("selected %q, want account supporting high effort", selected)
	}
}

func TestModelsRequireValidKeyWithoutUsingGenerationCapacity(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, completed), nil
	}))
	stream := f.open(t, f.key)

	models, err := f.executor.Models(t.Context(), f.key)
	if err != nil || len(models) != 1 || models[0].ID != "model" {
		t.Fatalf("models while generation slot occupied = %v, %v", models, err)
	}
	if _, err := f.executor.Models(t.Context(), "invalid-key"); !errors.Is(err, execution.ErrUnauthorized) {
		t.Fatalf("invalid key received catalog: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.executor.RevokeKey(t.Context(), "key"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executor.Models(t.Context(), f.key); !errors.Is(err, execution.ErrUnauthorized) {
		t.Fatalf("revoked key received catalog: %v", err)
	}
}

func TestRevokedKeyCannotReceiveInFlightCatalog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		calls := 0
		f := newFixtureWithCatalog(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, completed), nil
		}), roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			close(entered)
			<-release
			return response(200, `{"models":[{"slug":"model","visibility":"list"}]}`), nil
		}))
		if _, err := f.executor.Models(t.Context(), "invalid-key"); !errors.Is(err, execution.ErrUnauthorized) {
			t.Fatalf("invalid key = %v", err)
		}
		if calls != 0 {
			t.Fatal("invalid key initiated model discovery")
		}
		done := make(chan error, 1)
		go func() { _, err := f.executor.Models(t.Context(), f.key); done <- err }()
		<-entered

		if err := f.executor.RevokeKey(t.Context(), "key"); err != nil {
			t.Fatal(err)
		}
		unblock()

		if err := <-done; !errors.Is(err, execution.ErrUnauthorized) {
			t.Fatalf("key revoked during discovery received catalog: %v", err)
		}
	})
}

func TestReconnectedIdentityCannotUseOldCatalog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		generationCalls := 0
		f := newFixtureWithCatalog(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			generationCalls++
			return response(200, completed), nil
		}), roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("ChatGPT-Account-Id") == "one" {
				close(entered)
				<-release
				return response(200, `{"models":[{"slug":"model","visibility":"list"}]}`), nil
			}
			return response(200, `{"models":[{"slug":"replacement-model","visibility":"list"}]}`), nil
		}))
		finished := make(chan error, 1)
		go func() {
			result, err := f.executor.Generate(t.Context(), f.key, execution.RequestInfo{ID: "overlap"}, f.request)
			finished <- errors.Join(err, result.Close())
		}()
		<-entered
		record, err := f.store.GetAccount(t.Context(), "one")
		if err != nil {
			t.Fatal(err)
		}
		credentials := record.Account.Credentials()
		credentials.ChatGPTAccountID = "replacement"
		if err := f.store.ReplaceAccountCredentialsIfUnchanged(t.Context(), record, credentials); err != nil {
			t.Fatal(err)
		}

		unblock()
		if err := <-finished; err == nil {
			t.Fatal("old catalog authorized replacement identity")
		}
		models, err := f.executor.Models(t.Context(), f.key)

		if err != nil || len(models) != 1 || models[0].ID != "replacement-model" {
			t.Fatalf("models = %v, error = %v", models, err)
		}
		if generationCalls != 0 {
			t.Fatalf("sent %d requests under mismatched capabilities", generationCalls)
		}
	})
}

func TestCloseCancelsAndJoinsCatalogWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		f := newFixtureWithCatalog(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, completed), nil
		}), roundTripFunc(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			<-release
			return nil, r.Context().Err()
		}))
		modelsDone := make(chan error, 1)
		go func() { _, err := f.executor.Models(t.Context(), f.key); modelsDone <- err }()
		<-entered

		closed := make(chan struct{})
		go func() { f.executor.Close(); close(closed) }()
		<-canceled
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close returned before catalog cleanup")
		default:
		}
		unblock()
		<-closed

		if err := <-modelsDone; !errors.Is(err, codex.ErrCatalogClosed) {
			t.Fatalf("catalog result = %v", err)
		}
		if _, err := f.executor.Models(t.Context(), f.key); !errors.Is(err, execution.ErrClosed) {
			t.Fatalf("closed executor = %v", err)
		}
	})
}
