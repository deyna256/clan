// Package account holds account identity and upstream credentials.
package account

import (
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/deyna256/clan/internal/upstream"
)

// ID identifies an account within the installation.
type ID string

// Identity contains account metadata without credentials.
type Identity struct {
	ID         ID
	Name       string
	UpstreamID upstream.ID
}

// Credentials is an APIKeyCredentials or OAuthCredentials value.
// Credentials contain secrets and must not be logged or sent to clients.
type Credentials interface {
	isCredentials()
}

// APIKeyCredentials authenticates with an upstream API key.
type APIKeyCredentials struct {
	Key string
}

func (APIKeyCredentials) isCredentials() {}

// OAuthCredentials holds token data without checking its freshness.
type OAuthCredentials struct {
	AccessToken  string
	RefreshToken string            // Optional when renewal is handled externally.
	ExpiresAt    time.Time         // Zero means unknown expiry.
	ProviderData map[string]string // Optional provider-specific data; may contain secrets.
}

func (OAuthCredentials) isCredentials() {}

// Account is an immutable snapshot. Construct it with New; its zero value is invalid.
// Pass Identity to code that does not need secrets. Do not log the whole Account.
type Account struct {
	identity    Identity
	credentials Credentials
}

// New validates required fields and copies credentials into an account.
// Pass credentials by value, not pointer. Nonblank fields are kept unchanged.
// It does not check upstream existence, token freshness or provider acceptance.
// Callers must not modify provider data during construction.
func New(identity Identity, credentials Credentials) (Account, error) {
	if strings.TrimSpace(string(identity.ID)) == "" {
		return Account{}, errors.New("account: id is required")
	}
	if strings.TrimSpace(identity.Name) == "" {
		return Account{}, errors.New("account: name is required")
	}
	if strings.TrimSpace(string(identity.UpstreamID)) == "" {
		return Account{}, errors.New("account: upstream id is required")
	}

	switch c := credentials.(type) {
	case APIKeyCredentials:
		if strings.TrimSpace(c.Key) == "" {
			return Account{}, errors.New("account: API key is required")
		}
	case OAuthCredentials:
		if strings.TrimSpace(c.AccessToken) == "" {
			return Account{}, errors.New("account: OAuth access token is required")
		}
		c.ProviderData = maps.Clone(c.ProviderData)
		credentials = c
	default:
		return Account{}, errors.New("account: credentials must be an APIKeyCredentials or OAuthCredentials value")
	}

	return Account{identity: identity, credentials: credentials}, nil
}

// Identity returns account metadata without credentials.
func (a Account) Identity() Identity {
	return a.identity
}

// Credentials returns an independent copy for use by a provider integration.
// It returns nil for a zero Account. Returned data may contain secrets.
func (a Account) Credentials() Credentials {
	if c, ok := a.credentials.(OAuthCredentials); ok {
		c.ProviderData = maps.Clone(c.ProviderData)
		return c
	}
	return a.credentials
}
