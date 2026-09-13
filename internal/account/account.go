// Package account holds account identity and OAuth credentials.
package account

import (
	"errors"
	"strings"
	"time"
)

// ID identifies an account within the installation.
type ID string

// Identity contains account metadata without credentials.
type Identity struct {
	ID   ID
	Name string
}

// OAuthCredentials holds token data without checking its freshness.
// These secrets must not be logged or sent to clients.
type OAuthCredentials struct {
	ChatGPTAccountID string
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time // Zero means unknown expiry.
}

// Account is an immutable snapshot. Construct it with [New]; its zero value is invalid.
// Pass Identity to code that does not need secrets. Do not log the whole Account.
type Account struct {
	identity    Identity
	credentials OAuthCredentials
}

// New validates required fields, preserving nonblank values.
// It does not check token freshness or provider acceptance.
func New(identity Identity, credentials OAuthCredentials) (Account, error) {
	if strings.TrimSpace(string(identity.ID)) == "" {
		return Account{}, errors.New("account: id is required")
	}
	if strings.TrimSpace(identity.Name) == "" {
		return Account{}, errors.New("account: name is required")
	}
	if err := ValidateCredentials(credentials); err != nil {
		return Account{}, err
	}
	return Account{identity: identity, credentials: credentials}, nil
}

// ValidateCredentials checks the structural fields required for a stored Codex
// account without checking freshness or provider acceptance.
func ValidateCredentials(credentials OAuthCredentials) error {
	if strings.TrimSpace(credentials.ChatGPTAccountID) == "" {
		return errors.New("account: ChatGPT account ID is required")
	}
	if strings.TrimSpace(credentials.AccessToken) == "" {
		return errors.New("account: OAuth access token is required")
	}
	return nil
}

// Identity returns the account's metadata.
func (a Account) Identity() Identity {
	return a.identity
}

// Credentials returns a copy of the stored secrets.
func (a Account) Credentials() OAuthCredentials {
	return a.credentials
}
