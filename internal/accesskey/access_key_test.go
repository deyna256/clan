package accesskey_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/selection"
	"github.com/deyna256/clan/internal/upstream"
)

func TestNewRejectsInvalidIdentity(t *testing.T) {
	for _, tt := range []struct {
		name      string
		identity  accesskey.Identity
		wantField string
	}{
		{name: "missing id", identity: accesskey.Identity{Name: "Agent"}, wantField: "id"},
		{name: "blank id", identity: accesskey.Identity{ID: " \t\n", Name: "Agent"}, wantField: "id"},
		{name: "missing name", identity: accesskey.Identity{ID: "key"}, wantField: "name"},
		{name: "blank name", identity: accesskey.Identity{ID: "key", Name: " \t\n"}, wantField: "name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			permissions := accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true}

			key, err := accesskey.New(tt.identity, true, permissions)

			if err == nil {
				t.Fatal("New() accepted an invalid identity")
			}
			if !strings.Contains(" "+err.Error()+" ", " "+tt.wantField+" ") {
				t.Errorf("error %q does not identify field %q", err, tt.wantField)
			}
			if key.Identity() != (accesskey.Identity{}) {
				t.Error("New() returned a partial identity on failure")
			}
			if key.Allows(accesskey.Model{UpstreamID: "u", Name: "m"}, account.Identity{ID: "a", UpstreamID: "u"}) {
				t.Error("key returned on failure allows access")
			}
		})
	}
}

func TestNewRejectsInvalidPermissions(t *testing.T) {
	for _, tt := range []struct {
		name        string
		permissions accesskey.Permissions
		wantField   string
	}{
		{name: "all and listed upstreams", permissions: accesskey.Permissions{AllUpstreams: true, Upstreams: []upstream.ID{"u"}}, wantField: "upstreams"},
		{name: "all and listed models", permissions: accesskey.Permissions{AllModels: true, Models: []accesskey.Model{{UpstreamID: "u", Name: "m"}}}, wantField: "models"},
		{name: "all and listed accounts", permissions: accesskey.Permissions{AllAccounts: true, Accounts: []account.ID{"a"}}, wantField: "accounts"},
		{name: "empty upstream", permissions: accesskey.Permissions{Upstreams: []upstream.ID{""}}, wantField: "upstreams"},
		{name: "blank upstream", permissions: accesskey.Permissions{Upstreams: []upstream.ID{"u", " \t\n"}}, wantField: "upstreams"},
		{name: "model without upstream", permissions: accesskey.Permissions{Models: []accesskey.Model{{Name: "m"}}}, wantField: "models"},
		{name: "model with blank upstream", permissions: accesskey.Permissions{Models: []accesskey.Model{{UpstreamID: " \t\n", Name: "m"}}}, wantField: "models"},
		{name: "model without name", permissions: accesskey.Permissions{Models: []accesskey.Model{{UpstreamID: "u"}}}, wantField: "models"},
		{name: "model with blank name", permissions: accesskey.Permissions{Models: []accesskey.Model{{UpstreamID: "u", Name: " \t\n"}}}, wantField: "models"},
		{name: "empty account", permissions: accesskey.Permissions{Accounts: []account.ID{""}}, wantField: "accounts"},
		{name: "blank account", permissions: accesskey.Permissions{Accounts: []account.ID{"a", " \t\n"}}, wantField: "accounts"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			identity := accesskey.Identity{ID: "key", Name: "Agent"}

			key, err := accesskey.New(identity, true, tt.permissions)

			if err == nil {
				t.Fatal("New() accepted invalid permissions")
			}
			if !strings.Contains(" "+err.Error()+" ", " "+tt.wantField+" ") {
				t.Errorf("error %q does not identify field %q", err, tt.wantField)
			}
			if key.Identity() != (accesskey.Identity{}) {
				t.Error("New() returned a partial identity on failure")
			}
		})
	}
}

func TestAllowsRequiresEveryDimension(t *testing.T) {
	for _, tt := range []struct {
		name        string
		permissions accesskey.Permissions
		want        bool
	}{
		{name: "no permissions"},
		{name: "only upstreams", permissions: accesskey.Permissions{AllUpstreams: true}},
		{name: "only models", permissions: accesskey.Permissions{AllModels: true}},
		{name: "only accounts", permissions: accesskey.Permissions{AllAccounts: true}},
		{name: "upstreams and models", permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true}},
		{name: "upstreams and accounts", permissions: accesskey.Permissions{AllUpstreams: true, AllAccounts: true}},
		{name: "models and accounts", permissions: accesskey.Permissions{AllModels: true, AllAccounts: true}},
		{name: "all dimensions", permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			key := newKey(t, true, tt.permissions)
			model := accesskey.Model{UpstreamID: "u", Name: "m"}
			candidate := account.Identity{ID: "a", UpstreamID: "u"}

			got := key.Allows(model, candidate)

			if got != tt.want {
				t.Errorf("Allows() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestAllowsCombinesListsWithUnrestrictedDimensions(t *testing.T) {
	for _, tt := range []struct {
		name        string
		permissions accesskey.Permissions
		want        bool
	}{
		{name: "listed upstream", permissions: accesskey.Permissions{Upstreams: []upstream.ID{"u"}, AllModels: true, AllAccounts: true}, want: true},
		{name: "listed model", permissions: accesskey.Permissions{AllUpstreams: true, Models: []accesskey.Model{{UpstreamID: "u", Name: "m"}}, AllAccounts: true}, want: true},
		{name: "listed account", permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true, Accounts: []account.ID{"a"}}, want: true},
		{name: "empty upstreams", permissions: accesskey.Permissions{Upstreams: []upstream.ID{}, AllModels: true, AllAccounts: true}},
		{name: "empty models", permissions: accesskey.Permissions{AllUpstreams: true, Models: []accesskey.Model{}, AllAccounts: true}},
		{name: "empty accounts", permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true, Accounts: []account.ID{}}},
		{name: "upstream star is not all", permissions: accesskey.Permissions{Upstreams: []upstream.ID{"*"}, AllModels: true, AllAccounts: true}},
		{name: "model star is not all", permissions: accesskey.Permissions{AllUpstreams: true, Models: []accesskey.Model{{UpstreamID: "u", Name: "*"}}, AllAccounts: true}},
		{name: "account star is not all", permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true, Accounts: []account.ID{"*"}}},
		{
			name: "all with empty lists",
			permissions: accesskey.Permissions{
				AllUpstreams: true, Upstreams: []upstream.ID{},
				AllModels: true, Models: []accesskey.Model{},
				AllAccounts: true, Accounts: []account.ID{},
			},
			want: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			key := newKey(t, true, tt.permissions)
			model := accesskey.Model{UpstreamID: "u", Name: "m"}
			candidate := account.Identity{ID: "a", UpstreamID: "u"}

			got := key.Allows(model, candidate)

			if got != tt.want {
				t.Errorf("Allows() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestAllowsMatchesExactTargets(t *testing.T) {
	key := newKey(t, true, accesskey.Permissions{
		Upstreams: []upstream.ID{"first", "second"},
		Models: []accesskey.Model{
			{UpstreamID: "first", Name: "shared"},
			{UpstreamID: "second", Name: "other"},
		},
		Accounts: []account.ID{"a", "b"},
	})
	for _, tt := range []struct {
		name      string
		model     accesskey.Model
		candidate account.Identity
		want      bool
	}{
		{name: "first permitted target", model: accesskey.Model{UpstreamID: "first", Name: "shared"}, candidate: account.Identity{ID: "a", UpstreamID: "first"}, want: true},
		{name: "second permitted target", model: accesskey.Model{UpstreamID: "second", Name: "other"}, candidate: account.Identity{ID: "b", UpstreamID: "second"}, want: true},
		{name: "same model name on another upstream", model: accesskey.Model{UpstreamID: "second", Name: "shared"}, candidate: account.Identity{ID: "b", UpstreamID: "second"}},
		{name: "unlisted model", model: accesskey.Model{UpstreamID: "first", Name: "other"}, candidate: account.Identity{ID: "a", UpstreamID: "first"}},
		{name: "unlisted account", model: accesskey.Model{UpstreamID: "first", Name: "shared"}, candidate: account.Identity{ID: "c", UpstreamID: "first"}},
		{name: "account belongs to another upstream", model: accesskey.Model{UpstreamID: "first", Name: "shared"}, candidate: account.Identity{ID: "a", UpstreamID: "second"}},
		{name: "model name case differs", model: accesskey.Model{UpstreamID: "first", Name: "Shared"}, candidate: account.Identity{ID: "a", UpstreamID: "first"}},
		{name: "account id case differs", model: accesskey.Model{UpstreamID: "first", Name: "shared"}, candidate: account.Identity{ID: "A", UpstreamID: "first"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := key.Allows(tt.model, tt.candidate)

			if got != tt.want {
				t.Errorf("Allows(%+v, %+v) = %t, want %t", tt.model, tt.candidate, got, tt.want)
			}
		})
	}
}

func TestDisabledAndZeroKeysDenyAccess(t *testing.T) {
	disabled := newKey(t, false, accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true})
	for _, tt := range []struct {
		name string
		key  accesskey.AccessKey
	}{
		{name: "disabled", key: disabled},
		{name: "zero"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := accesskey.Model{UpstreamID: "u", Name: "m"}
			candidate := account.Identity{ID: "a", UpstreamID: "u"}

			allowed := tt.key.Allows(model, candidate)
			filtered := tt.key.Filter(model, []account.Identity{candidate})

			if allowed || len(filtered) != 0 {
				t.Errorf("Allows() = %t, Filter() = %v; want false and no candidates", allowed, filtered)
			}
		})
	}
}

func TestUnrestrictedKeyRejectsInvalidTargets(t *testing.T) {
	key := newKey(t, true, accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true})
	for _, tt := range []struct {
		name      string
		model     accesskey.Model
		candidate account.Identity
	}{
		{name: "missing model upstream", model: accesskey.Model{Name: "m"}, candidate: account.Identity{ID: "a"}},
		{name: "blank model upstream", model: accesskey.Model{UpstreamID: " \t\n", Name: "m"}, candidate: account.Identity{ID: "a", UpstreamID: " \t\n"}},
		{name: "missing model name", model: accesskey.Model{UpstreamID: "u"}, candidate: account.Identity{ID: "a", UpstreamID: "u"}},
		{name: "blank model name", model: accesskey.Model{UpstreamID: "u", Name: " \t\n"}, candidate: account.Identity{ID: "a", UpstreamID: "u"}},
		{name: "missing account id", model: accesskey.Model{UpstreamID: "u", Name: "m"}, candidate: account.Identity{UpstreamID: "u"}},
		{name: "blank account id", model: accesskey.Model{UpstreamID: "u", Name: "m"}, candidate: account.Identity{ID: " \t\n", UpstreamID: "u"}},
		{name: "missing account upstream", model: accesskey.Model{UpstreamID: "u", Name: "m"}, candidate: account.Identity{ID: "a"}},
		{name: "wrong account upstream", model: accesskey.Model{UpstreamID: "u", Name: "m"}, candidate: account.Identity{ID: "a", UpstreamID: "other"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allowed := key.Allows(tt.model, tt.candidate)
			filtered := key.Filter(tt.model, []account.Identity{tt.candidate})

			if allowed || len(filtered) != 0 {
				t.Errorf("Allows() = %t, Filter() = %v; want false and no candidates", allowed, filtered)
			}
		})
	}
}

func TestNewOwnsPermissionLists(t *testing.T) {
	identity := accesskey.Identity{ID: " key ", Name: " Agent "}
	permissions := accesskey.Permissions{
		Upstreams: []upstream.ID{" first ", " first "},
		Models:    []accesskey.Model{{UpstreamID: " first ", Name: " model "}},
		Accounts:  []account.ID{" a ", " a "},
	}
	key, err := accesskey.New(identity, true, permissions)
	if err != nil {
		t.Fatal(err)
	}

	permissions.Upstreams[0], permissions.Upstreams[1] = "second", "second"
	permissions.Models[0] = accesskey.Model{UpstreamID: "second", Name: "other"}
	permissions.Accounts[0], permissions.Accounts[1] = "b", "b"
	original := key.Allows(accesskey.Model{UpstreamID: " first ", Name: " model "}, account.Identity{ID: " a ", UpstreamID: " first "})
	replacement := key.Allows(accesskey.Model{UpstreamID: "second", Name: "other"}, account.Identity{ID: "b", UpstreamID: "second"})

	if key.Identity() != identity {
		t.Errorf("identity = %+v, want %+v", key.Identity(), identity)
	}
	if !original || replacement {
		t.Errorf("original allowed = %t, replacement allowed = %t; want true and false", original, replacement)
	}
}

func TestFilterDoesNotShareOrChangeCandidateData(t *testing.T) {
	key := newKey(t, true, accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true})
	model := accesskey.Model{UpstreamID: "u", Name: "m"}
	candidates := []account.Identity{{ID: "c", UpstreamID: "u"}, {ID: "a", UpstreamID: "u"}, {ID: "b", UpstreamID: "other"}}
	wantCandidates := slices.Clone(candidates)

	filtered := key.Filter(model, candidates)
	firstResult := slices.Clone(filtered)
	candidatesAfterCall := slices.Clone(candidates)
	// Reuse both input and output; the next call must use its own candidates.
	for i := range filtered {
		filtered[i] = "replacement"
	}
	candidates[0].ID = "changed"
	next := key.Filter(model, wantCandidates)

	if want := []account.ID{"c", "a"}; !slices.Equal(firstResult, want) {
		t.Errorf("Filter() = %v, want %v", firstResult, want)
	}
	if !slices.Equal(candidatesAfterCall, wantCandidates) {
		t.Errorf("candidates changed: got %v, want %v", candidatesAfterCall, wantCandidates)
	}
	if want := []account.ID{"c", "a"}; !slices.Equal(next, want) {
		t.Errorf("Filter() after reusing data = %v, want %v", next, want)
	}
}

func TestFilterWithNoCandidates(t *testing.T) {
	key := newKey(t, true, accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true})
	for _, candidates := range [][]account.Identity{nil, {}} {
		got := key.Filter(accesskey.Model{UpstreamID: "u", Name: "m"}, candidates)

		if got != nil {
			t.Errorf("Filter(%v) = %v, want nil", candidates, got)
		}
	}
}

func TestPermissionsLimitRoundRobinCandidates(t *testing.T) {
	// Arrange: real accounts differ in credentials, IDs and upstreams.
	var candidates []account.Identity
	for _, input := range []struct {
		id          account.ID
		upstreamID  upstream.ID
		credentials account.Credentials
	}{
		{id: "c", upstreamID: "primary", credentials: account.OAuthCredentials{AccessToken: "test-oauth"}},
		{id: "a", upstreamID: "primary", credentials: account.APIKeyCredentials{Key: "test-key-a"}},
		{id: "d", upstreamID: "other", credentials: account.APIKeyCredentials{Key: "test-key-d"}},
		{id: "b", upstreamID: "primary", credentials: account.APIKeyCredentials{Key: "test-key-b"}},
	} {
		a, err := account.New(account.Identity{ID: input.id, Name: "Test account", UpstreamID: input.upstreamID}, input.credentials)
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, a.Identity())
	}
	key, err := accesskey.New(
		accesskey.Identity{ID: "key-1", Name: "Agent"}, true,
		accesskey.Permissions{
			Upstreams: []upstream.ID{"primary"},
			Models:    []accesskey.Model{{UpstreamID: "primary", Name: "model"}},
			Accounts:  []account.ID{"a", "c", "d"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "primary", Model: "model"}
	var choices []account.ID

	// Act: only the filtered IDs reach the selector.
	allowed := key.Filter(accesskey.Model{UpstreamID: "primary", Name: "model"}, candidates)
	for range 4 {
		id, ok := selector.Select(scope, allowed)
		if !ok {
			t.Fatal("selection unexpectedly found no candidate")
		}
		choices = append(choices, id)
	}

	// Assert: filtering preserves input order; selection rotates by ID.
	if want := []account.ID{"c", "a"}; !slices.Equal(allowed, want) {
		t.Errorf("allowed = %v, want %v", allowed, want)
	}
	if want := []account.ID{"a", "c", "a", "c"}; !slices.Equal(choices, want) {
		t.Errorf("choices = %v, want %v", choices, want)
	}
}

func newKey(t *testing.T, enabled bool, permissions accesskey.Permissions) accesskey.AccessKey {
	t.Helper()
	key, err := accesskey.New(accesskey.Identity{ID: "key", Name: "Agent"}, enabled, permissions)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
