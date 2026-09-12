// Package accesskey holds named client keys and generates and verifies their secrets.
package accesskey

import (
	"errors"
	"strings"
)

// ID identifies a CLAN access key, not its secret value.
type ID string

// Identity contains access-key metadata without its secret or verification hash.
type Identity struct {
	ID   ID
	Name string
}

// AccessKey is an immutable snapshot. Its zero value is disabled.
// Construct it with [New]. It does not verify a presented access-key secret.
type AccessKey struct {
	identity Identity
	enabled  bool
}

// New validates identity, preserving nonblank values. On error, it returns a zero key.
func New(identity Identity, enabled bool) (AccessKey, error) {
	if strings.TrimSpace(string(identity.ID)) == "" {
		return AccessKey{}, errors.New("access key: id is required")
	}
	if strings.TrimSpace(identity.Name) == "" {
		return AccessKey{}, errors.New("access key: name is required")
	}
	return AccessKey{identity: identity, enabled: enabled}, nil
}

// Identity returns the key's metadata.
func (k AccessKey) Identity() Identity {
	return k.identity
}

// Enabled reports the key's status in this snapshot.
func (k AccessKey) Enabled() bool {
	return k.enabled
}
