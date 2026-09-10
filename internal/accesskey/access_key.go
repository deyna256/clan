// Package accesskey checks access to upstreams, models and accounts.
package accesskey

import (
	"errors"
	"slices"
	"strings"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/upstream"
)

// ID identifies a CLAN access key, not its secret value.
type ID string

// Identity contains access-key metadata without its secret or verification hash.
type Identity struct {
	ID   ID
	Name string
}

// Model identifies a concrete model within one configured upstream.
type Model struct {
	UpstreamID upstream.ID
	Name       string
}

func (m Model) valid() bool {
	return strings.TrimSpace(string(m.UpstreamID)) != "" && strings.TrimSpace(m.Name) != ""
}

// Permissions must allow all three dimensions for access to be granted.
// Empty lists deny access; each All setting explicitly removes that restriction.
// An All setting cannot be combined with a nonempty list in the same dimension.
type Permissions struct {
	AllUpstreams bool
	Upstreams    []upstream.ID
	AllModels    bool
	Models       []Model
	AllAccounts  bool
	Accounts     []account.ID
}

func (p Permissions) validate() error {
	if p.AllUpstreams && len(p.Upstreams) > 0 {
		return errors.New("access key: upstreams cannot combine all with a list")
	}
	if p.AllModels && len(p.Models) > 0 {
		return errors.New("access key: models cannot combine all with a list")
	}
	if p.AllAccounts && len(p.Accounts) > 0 {
		return errors.New("access key: accounts cannot combine all with a list")
	}
	for _, id := range p.Upstreams {
		if strings.TrimSpace(string(id)) == "" {
			return errors.New("access key: upstreams must contain nonblank IDs")
		}
	}
	for _, model := range p.Models {
		if !model.valid() {
			return errors.New("access key: models must contain a nonblank upstream ID and name")
		}
	}
	for _, id := range p.Accounts {
		if strings.TrimSpace(string(id)) == "" {
			return errors.New("access key: accounts must contain nonblank IDs")
		}
	}
	return nil
}

// AccessKey is an immutable permission snapshot. Its zero value denies all access.
// Construct it with New. It does not verify a presented access-key secret.
type AccessKey struct {
	identity    Identity
	enabled     bool
	permissions Permissions
}

// New validates identity and permissions and copies their lists. Nonblank values
// are preserved; resource existence is not checked. Callers must not modify the
// lists during construction. Errors return a zero key without input values.
func New(identity Identity, enabled bool, permissions Permissions) (AccessKey, error) {
	if strings.TrimSpace(string(identity.ID)) == "" {
		return AccessKey{}, errors.New("access key: id is required")
	}
	if strings.TrimSpace(identity.Name) == "" {
		return AccessKey{}, errors.New("access key: name is required")
	}
	if err := permissions.validate(); err != nil {
		return AccessKey{}, err
	}
	permissions.Upstreams = slices.Clone(permissions.Upstreams)
	permissions.Models = slices.Clone(permissions.Models)
	permissions.Accounts = slices.Clone(permissions.Accounts)
	return AccessKey{identity: identity, enabled: enabled, permissions: permissions}, nil
}

// Identity returns access-key metadata without secrets.
func (k AccessKey) Identity() Identity {
	return k.identity
}

// Allows checks permissions for a model and an account from trusted inventory.
// It does not check model support, account availability or token budgets.
func (k AccessKey) Allows(model Model, candidate account.Identity) bool {
	if !k.enabled || !model.valid() || strings.TrimSpace(string(candidate.ID)) == "" {
		return false
	}
	if candidate.UpstreamID != model.UpstreamID {
		return false
	}
	p := k.permissions
	return (p.AllUpstreams || slices.Contains(p.Upstreams, model.UpstreamID)) &&
		(p.AllModels || slices.Contains(p.Models, model)) &&
		(p.AllAccounts || slices.Contains(p.Accounts, candidate.ID))
}

// Filter returns permitted IDs in candidate order, or nil when none are allowed.
// It neither changes nor retains candidates; callers must not modify them during
// the call. Candidate identities must come from trusted inventory, not client input.
func (k AccessKey) Filter(model Model, candidates []account.Identity) []account.ID {
	var allowed []account.ID
	for _, candidate := range candidates {
		if k.Allows(model, candidate) {
			allowed = append(allowed, candidate.ID)
		}
	}
	return allowed
}
