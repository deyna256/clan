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
	tests := []struct {
		name      string
		identity  accesskey.Identity
		wantField string
	}{
		{name: "missing id", identity: accesskey.Identity{Name: "Agent"}, wantField: "id"},
		{name: "blank id", identity: accesskey.Identity{ID: " \t\n", Name: "Agent"}, wantField: "id"},
		{name: "missing name", identity: accesskey.Identity{ID: "key"}, wantField: "name"},
		{name: "blank name", identity: accesskey.Identity{ID: "key", Name: " \t\n"}, wantField: "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			permissions := unrestrictedPermissions()

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
	tests := []struct {
		name        string
		permissions accesskey.Permissions
		wantField   string
	}{
		{
			name:        "all and listed upstreams",
			permissions: accesskey.Permissions{AllUpstreams: true, Upstreams: []upstream.ID{"u"}},
			wantField:   "upstreams",
		},
		{
			name:        "all and listed models",
			permissions: accesskey.Permissions{AllModels: true, Models: []accesskey.Model{{UpstreamID: "u", Name: "m"}}},
			wantField:   "models",
		},
		{
			name:        "all and listed accounts",
			permissions: accesskey.Permissions{AllAccounts: true, Accounts: []account.ID{"a"}},
			wantField:   "accounts",
		},
		{name: "empty upstream", permissions: accesskey.Permissions{Upstreams: []upstream.ID{""}}, wantField: "upstreams"},
		{name: "blank upstream", permissions: accesskey.Permissions{Upstreams: []upstream.ID{"u", " \t\n"}}, wantField: "upstreams"},
		{
			name:        "model without upstream",
			permissions: accesskey.Permissions{Models: []accesskey.Model{{Name: "m"}}},
			wantField:   "models",
		},
		{
			name:        "model with blank upstream",
			permissions: accesskey.Permissions{Models: []accesskey.Model{{UpstreamID: " \t\n", Name: "m"}}},
			wantField:   "models",
		},
		{
			name:        "model without name",
			permissions: accesskey.Permissions{Models: []accesskey.Model{{UpstreamID: "u"}}},
			wantField:   "models",
		},
		{
			name:        "model with blank name",
			permissions: accesskey.Permissions{Models: []accesskey.Model{{UpstreamID: "u", Name: " \t\n"}}},
			wantField:   "models",
		},
		{name: "empty account", permissions: accesskey.Permissions{Accounts: []account.ID{""}}, wantField: "accounts"},
		{name: "blank account", permissions: accesskey.Permissions{Accounts: []account.ID{"a", " \t\n"}}, wantField: "accounts"},
	}
	for _, tt := range tests {
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
	tests := []struct {
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
		{
			name:        "all dimensions",
			permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true},
			want:        true,
		},
	}
	for _, tt := range tests {
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
	tests := []struct {
		name        string
		permissions accesskey.Permissions
		want        bool
	}{
		{
			name:        "listed upstream",
			permissions: accesskey.Permissions{Upstreams: []upstream.ID{"u"}, AllModels: true, AllAccounts: true},
			want:        true,
		},
		{
			name:        "listed model",
			permissions: accesskey.Permissions{AllUpstreams: true, Models: []accesskey.Model{{UpstreamID: "u", Name: "m"}}, AllAccounts: true},
			want:        true,
		},
		{
			name:        "listed account",
			permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true, Accounts: []account.ID{"a"}},
			want:        true,
		},
		{name: "empty upstreams", permissions: accesskey.Permissions{Upstreams: []upstream.ID{}, AllModels: true, AllAccounts: true}},
		{name: "empty models", permissions: accesskey.Permissions{AllUpstreams: true, Models: []accesskey.Model{}, AllAccounts: true}},
		{name: "empty accounts", permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true, Accounts: []account.ID{}}},
		{
			name:        "upstream star is not all",
			permissions: accesskey.Permissions{Upstreams: []upstream.ID{"*"}, AllModels: true, AllAccounts: true},
		},
		{
			name:        "model star is not all",
			permissions: accesskey.Permissions{AllUpstreams: true, Models: []accesskey.Model{{UpstreamID: "u", Name: "*"}}, AllAccounts: true},
		},
		{
			name:        "account star is not all",
			permissions: accesskey.Permissions{AllUpstreams: true, AllModels: true, Accounts: []account.ID{"*"}},
		},
		{
			name: "all with empty lists",
			permissions: accesskey.Permissions{
				AllUpstreams: true, Upstreams: []upstream.ID{},
				AllModels: true, Models: []accesskey.Model{},
				AllAccounts: true, Accounts: []account.ID{},
			},
			want: true,
		},
	}
	for _, tt := range tests {
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
	firstModel := accesskey.Model{UpstreamID: "first", Name: "shared"}
	secondModel := accesskey.Model{UpstreamID: "second", Name: "other"}
	firstAccount := account.Identity{ID: "a", UpstreamID: "first"}
	secondAccount := account.Identity{ID: "b", UpstreamID: "second"}
	key := newKey(t, true, accesskey.Permissions{
		Upstreams: []upstream.ID{"first", "second"},
		Models:    []accesskey.Model{firstModel, secondModel},
		Accounts:  []account.ID{"a", "b"},
	})
	tests := []struct {
		name      string
		model     accesskey.Model
		candidate account.Identity
		want      bool
	}{
		{
			name:      "first permitted target",
			model:     firstModel,
			candidate: firstAccount,
			want:      true,
		},
		{
			name:      "second permitted target",
			model:     secondModel,
			candidate: secondAccount,
			want:      true,
		},
		{
			name:      "same model name on another upstream",
			model:     accesskey.Model{UpstreamID: "second", Name: "shared"},
			candidate: secondAccount,
		},
		{
			name:      "unlisted model",
			model:     accesskey.Model{UpstreamID: "first", Name: "other"},
			candidate: firstAccount,
		},
		{
			name:      "unlisted account",
			model:     firstModel,
			candidate: account.Identity{ID: "c", UpstreamID: "first"},
		},
		{
			name:      "account belongs to another upstream",
			model:     firstModel,
			candidate: account.Identity{ID: "a", UpstreamID: "second"},
		},
		{
			name:      "model name case differs",
			model:     accesskey.Model{UpstreamID: "first", Name: "Shared"},
			candidate: firstAccount,
		},
		{
			name:      "model name whitespace differs",
			model:     accesskey.Model{UpstreamID: "first", Name: " shared "},
			candidate: firstAccount,
		},
		{
			name:      "account id case differs",
			model:     firstModel,
			candidate: account.Identity{ID: "A", UpstreamID: "first"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := key.Allows(tt.model, tt.candidate)

			if got != tt.want {
				t.Errorf("Allows(%+v, %+v) = %t, want %t", tt.model, tt.candidate, got, tt.want)
			}
		})
	}
}

func TestDisabledAndZeroKeysDenyAccess(t *testing.T) {
	disabled := newKey(t, false, unrestrictedPermissions())
	tests := []struct {
		name string
		key  accesskey.AccessKey
	}{
		{name: "disabled", key: disabled},
		{name: "zero"},
	}
	for _, tt := range tests {
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
	validModel := accesskey.Model{UpstreamID: "u", Name: "m"}
	validAccount := account.Identity{ID: "a", UpstreamID: "u"}
	key := newKey(t, true, unrestrictedPermissions())
	tests := []struct {
		name      string
		model     accesskey.Model
		candidate account.Identity
	}{
		{name: "missing model upstream", model: accesskey.Model{Name: "m"}, candidate: account.Identity{ID: "a"}},
		{
			name:      "blank model upstream",
			model:     accesskey.Model{UpstreamID: " \t\n", Name: "m"},
			candidate: account.Identity{ID: "a", UpstreamID: " \t\n"},
		},
		{name: "missing model name", model: accesskey.Model{UpstreamID: "u"}, candidate: validAccount},
		{
			name:      "blank model name",
			model:     accesskey.Model{UpstreamID: "u", Name: " \t\n"},
			candidate: validAccount,
		},
		{name: "missing account id", model: validModel, candidate: account.Identity{UpstreamID: "u"}},
		{
			name:      "blank account id",
			model:     validModel,
			candidate: account.Identity{ID: " \t\n", UpstreamID: "u"},
		},
		{name: "missing account upstream", model: validModel, candidate: account.Identity{ID: "a"}},
		{
			name:      "wrong account upstream",
			model:     validModel,
			candidate: account.Identity{ID: "a", UpstreamID: "other"},
		},
		{
			name:      "account upstream case differs",
			model:     validModel,
			candidate: account.Identity{ID: "a", UpstreamID: "U"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed := key.Allows(tt.model, tt.candidate)
			filtered := key.Filter(tt.model, []account.Identity{tt.candidate})

			if allowed || len(filtered) != 0 {
				t.Errorf("Allows() = %t, Filter() = %v; want false and no candidates", allowed, filtered)
			}
		})
	}
}

func TestNewPreservesIdentityAndPermissionValues(t *testing.T) {
	identity := accesskey.Identity{ID: " key ", Name: " Agent "}
	permissions := accesskey.Permissions{
		Upstreams: []upstream.ID{" first ", " first "},
		Models:    []accesskey.Model{{UpstreamID: " first ", Name: " model "}, {UpstreamID: " first ", Name: " model "}},
		Accounts:  []account.ID{" a ", " a "},
	}

	key, err := accesskey.New(identity, true, permissions)

	if err != nil {
		t.Fatal(err)
	}
	if key.Identity() != identity {
		t.Errorf("identity = %+v, want %+v", key.Identity(), identity)
	}
	if !key.Allows(accesskey.Model{UpstreamID: " first ", Name: " model "}, account.Identity{ID: " a ", UpstreamID: " first "}) {
		t.Error("key does not allow the supplied values")
	}
	if key.Allows(accesskey.Model{UpstreamID: "first", Name: "model"}, account.Identity{ID: "a", UpstreamID: "first"}) {
		t.Error("key allows normalized values that were not supplied")
	}
}

func TestNewOwnsPermissionLists(t *testing.T) {
	permissions := accesskey.Permissions{
		Upstreams: []upstream.ID{"first"},
		Models:    []accesskey.Model{{UpstreamID: "first", Name: "model"}},
		Accounts:  []account.ID{"a"},
	}
	key := newKey(t, true, permissions)

	permissions.Upstreams[0] = "second"
	permissions.Models[0] = accesskey.Model{UpstreamID: "second", Name: "other"}
	permissions.Accounts[0] = "b"
	original := key.Allows(accesskey.Model{UpstreamID: "first", Name: "model"}, account.Identity{ID: "a", UpstreamID: "first"})
	replacement := key.Allows(accesskey.Model{UpstreamID: "second", Name: "other"}, account.Identity{ID: "b", UpstreamID: "second"})

	if !original || replacement {
		t.Errorf("original allowed = %t, replacement allowed = %t; want true and false", original, replacement)
	}
}

func TestFilterLeavesCandidatesUnchanged(t *testing.T) {
	key := newKey(t, true, accesskey.Permissions{AllUpstreams: true, AllModels: true, Accounts: []account.ID{"a"}})
	model := accesskey.Model{UpstreamID: "u", Name: "m"}
	candidates := []account.Identity{{ID: "c", UpstreamID: "u"}, {ID: "a", UpstreamID: "u"}, {ID: "b", UpstreamID: "other"}}
	wantCandidates := slices.Clone(candidates)

	key.Filter(model, candidates)

	if !slices.Equal(candidates, wantCandidates) {
		t.Errorf("candidates changed: got %v, want %v", candidates, wantCandidates)
	}
}

func TestFilterWithNoCandidates(t *testing.T) {
	key := newKey(t, true, unrestrictedPermissions())
	for _, candidates := range [][]account.Identity{nil, {}} {
		got := key.Filter(accesskey.Model{UpstreamID: "u", Name: "m"}, candidates)

		if got != nil {
			t.Errorf("Filter(%v) = %v, want nil", candidates, got)
		}
	}
}

func TestPermissionsLimitRoundRobinCandidates(t *testing.T) {
	apiKey := account.APIKeyCredentials{Key: "test-key"}
	candidates := []account.Identity{
		newAccount(t, "c", "primary", account.OAuthCredentials{AccessToken: "test-oauth"}).Identity(),
		newAccount(t, "a", "primary", apiKey).Identity(),
		newAccount(t, "d", "other", apiKey).Identity(),
		newAccount(t, "b", "primary", apiKey).Identity(),
	}
	model := accesskey.Model{UpstreamID: "primary", Name: "model"}
	key := newKey(t, true, accesskey.Permissions{
		Upstreams: []upstream.ID{"primary"},
		Models:    []accesskey.Model{model},
		Accounts:  []account.ID{"a", "c", "d"},
	})
	selector := selection.NewRoundRobin()
	scope := selection.Scope{UpstreamID: "primary", Model: "model"}
	var choices []account.ID

	// Act: only the filtered IDs reach the selector.
	allowed := key.Filter(model, candidates)
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

func unrestrictedPermissions() accesskey.Permissions {
	return accesskey.Permissions{AllUpstreams: true, AllModels: true, AllAccounts: true}
}

func newAccount(t *testing.T, id account.ID, upstreamID upstream.ID, credentials account.Credentials) account.Account {
	t.Helper()
	a, err := account.New(account.Identity{ID: id, Name: "Test account", UpstreamID: upstreamID}, credentials)
	if err != nil {
		t.Fatalf("create test account %q: %v", id, err)
	}
	return a
}
