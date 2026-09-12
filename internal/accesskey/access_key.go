// Package accesskey generates and verifies keys and checks their permissions.
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

// Permissions restrict upstreams and models by exact value.
// Both restrictions must allow access. Empty lists deny access.
// Each All flag removes its restriction and cannot accompany a nonempty list.
type Permissions struct {
	AllUpstreams bool
	Upstreams    []upstream.ID
	AllModels    bool
	Models       []Model
}

func (p Permissions) validate() error {
	if p.AllUpstreams && len(p.Upstreams) > 0 {
		return errors.New("access key: upstreams cannot combine all with a list")
	}
	if p.AllModels && len(p.Models) > 0 {
		return errors.New("access key: models cannot combine all with a list")
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
	return nil
}

// AccessKey is an immutable permission snapshot. Its zero value denies all access.
// Construct it with [New]. It does not verify a presented access-key secret.
type AccessKey struct {
	identity    Identity
	enabled     bool
	permissions Permissions
}

// New validates identity and permissions, preserving nonblank values and copying
// permission lists. It does not check resource existence. Lists must not change
// during the call. On error, New returns a zero key.
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
	return AccessKey{identity: identity, enabled: enabled, permissions: permissions}, nil
}

// Identity returns the key's metadata.
func (k AccessKey) Identity() Identity {
	return k.identity
}

// Allows reports whether the key permits a model and an account from trusted inventory.
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
		(p.AllModels || slices.Contains(p.Models, model))
}

// Filter applies [AccessKey.Allows], preserving candidate order. It returns nil
// if none are allowed. Candidates must not change during the call; the slice is
// neither modified nor retained.
func (k AccessKey) Filter(model Model, candidates []account.Identity) []account.ID {
	var allowed []account.ID
	for _, candidate := range candidates {
		if k.Allows(model, candidate) {
			allowed = append(allowed, candidate.ID)
		}
	}
	return allowed
}
