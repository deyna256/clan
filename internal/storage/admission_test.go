package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/admission"
	"github.com/deyna256/clan/internal/budget"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/ratelimit"
	"github.com/deyna256/clan/internal/storage"
	"github.com/deyna256/clan/internal/usage"
)

func TestAdmissionRestoresSavedAttemptConsumption(t *testing.T) {
	for _, backend := range []string{storage.SQLite, storage.PostgreSQL} {
		t.Run(backend, func(t *testing.T) {
			config, _ := database(t, backend)
			store := openStore(t, config)
			opening := time.Now().Add(-time.Hour)
			saved := budget.State{
				FiveHours: budget.Window{OpenedAt: opening, Used: 10},
				SevenDays: budget.Window{OpenedAt: opening, Used: 20},
			}
			if err := store.Save(t.Context(), "client", saved); err != nil {
				t.Fatal(err)
			}
			coordinator := newCoordinator(t, store)
			policy := admissionPolicy(t)
			policy.Budgets.SevenDays = new(int64(40))
			model := accesskey.Model{UpstreamID: "upstream", Name: "model"}
			candidate := account.Identity{ID: "account", UpstreamID: model.UpstreamID}

			request, first, err := coordinator.Start(t.Context(), policy, model, candidate)
			if err != nil {
				t.Fatal(err)
			}
			defer request.Release()
			if _, err := first.Observe(usage.Snapshot{Total: usage.Counter{Known: true, Tokens: 5}}); err != nil {
				t.Fatal(err)
			}
			second, err := request.NextAttempt(t.Context(), policy, candidate)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := second.Observe(usage.Snapshot{Total: usage.Counter{Known: true, Tokens: 15}}); err != nil {
					t.Fatal(err)
				}
			}
			request.Release()
			if err := coordinator.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened := openStore(t, config)
			assertSnapshot(t, reopened, "client", budget.State{
				FiveHours: budget.Window{OpenedAt: opening, Used: 30},
				SevenDays: budget.Window{OpenedAt: opening, Used: 40},
			})
			restarted := newCoordinator(t, reopened)
			rejected, _, err := restarted.Start(t.Context(), policy, model, candidate)
			defer rejected.Release()
			if !errors.Is(err, budget.ErrExhausted) {
				t.Fatalf("admission after restart = %v, want exhausted saved budget", err)
			}
		})
	}
}

func TestAdmissionRecoversAfterDatabaseRejectsSnapshot(t *testing.T) {
	for _, backend := range []string{storage.SQLite, storage.PostgreSQL} {
		t.Run(backend, func(t *testing.T) {
			config, raw := database(t, backend)
			store := openStore(t, config)
			if _, err := raw.ExecContext(t.Context(), `ALTER TABLE budget_snapshots
				ADD COLUMN reject_update INTEGER NOT NULL DEFAULT 0 CHECK (seven_days_used <> 999)`); err != nil {
				t.Fatal(err)
			}
			coordinator := newCoordinator(t, store)
			policy := admissionPolicy(t)
			policy.Budgets.SevenDays = new(int64(2000))
			model := accesskey.Model{UpstreamID: "upstream", Name: "model"}
			candidate := account.Identity{ID: "account", UpstreamID: model.UpstreamID}
			request, attempt, err := coordinator.Start(t.Context(), policy, model, candidate)
			if err != nil {
				t.Fatal(err)
			}
			defer request.Release()
			if err := coordinator.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			opening, _, err := store.Load(t.Context(), "client")
			if err != nil {
				t.Fatal(err)
			}

			if _, err := attempt.Observe(usage.Snapshot{Total: usage.Counter{Known: true, Tokens: 999}}); err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Flush(t.Context()); err == nil {
				t.Fatal("Flush succeeded despite database constraint")
			}
			if _, err := request.NextAttempt(t.Context(), policy, candidate); !errors.Is(err, admission.ErrUnavailable) {
				t.Fatalf("retry after failed save = %v, want ErrUnavailable", err)
			}
			assertSnapshot(t, store, "client", opening)

			if _, err := attempt.Observe(usage.Snapshot{Total: usage.Counter{Known: true, Tokens: 1000}}); err != nil {
				t.Fatalf("usage from admitted work after failed save: %v", err)
			}
			if err := coordinator.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := request.NextAttempt(t.Context(), policy, candidate); err != nil {
				t.Fatalf("retry after successful save: %v", err)
			}
			assertSnapshot(t, store, "client", budget.State{
				FiveHours: budget.Window{OpenedAt: opening.FiveHours.OpenedAt, Used: 1000},
				SevenDays: budget.Window{OpenedAt: opening.SevenDays.OpenedAt, Used: 1000},
			})
		})
	}
}

func newCoordinator(t *testing.T, store *storage.Store) *admission.Coordinator {
	t.Helper()
	rpm := ratelimit.New()
	if err := rpm.Configure("client", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
		t.Fatal(err)
	}
	coordinator, err := admission.New(store, rpm)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func admissionPolicy(t *testing.T) admission.Policy {
	t.Helper()
	key, err := accesskey.New(accesskey.Identity{ID: "client", Name: "client"}, true,
		accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	return admission.Policy{Key: key, Concurrency: concurrency.Unlimited}
}
