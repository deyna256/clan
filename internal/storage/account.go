package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/deyna256/clan/internal/account"
)

// CreateAccount stores an account snapshot and its initial enabled state.
func (s *Store) CreateAccount(ctx context.Context, value account.Account, enabled bool) error {
	identity, credentials, err := validateAccount(value)
	if err != nil {
		return err
	}
	encrypted, err := s.encryptCredentials(identity.ID, credentials)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO accounts (id, name, enabled, credentials)
		 VALUES (?, ?, ?, ?)`,
		string(identity.ID),
		identity.Name,
		enabled,
		encrypted,
	)
	if err != nil {
		return classifyWriteError("creating account", err)
	}
	return nil
}

// GetAccount returns one account, decrypting and authenticating its credentials.
func (s *Store) GetAccount(ctx context.Context, id account.ID) (AccountRecord, error) {
	if err := validateAccountID(id); err != nil {
		return AccountRecord{}, err
	}

	var name string
	var enabled int64
	var encrypted []byte
	err := s.db.QueryRowContext(
		ctx,
		`SELECT name, enabled, credentials FROM accounts WHERE id = ?`,
		string(id),
	).Scan(&name, &enabled, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountRecord{}, ErrNotFound
	}
	if err != nil {
		return AccountRecord{}, fmt.Errorf("storage: reading account: %w", err)
	}
	return s.decodeAccount(id, name, enabled, encrypted)
}

// ListAccounts returns all accounts in ascending local-ID order.
func (s *Store) ListAccounts(ctx context.Context) ([]AccountRecord, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, enabled, credentials FROM accounts ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: listing accounts: %w", err)
	}
	defer rows.Close() // Iteration reports errors through rows.Err; early exits already return an error.

	var records []AccountRecord
	for rows.Next() {
		var id string
		var name string
		var enabled int64
		var encrypted []byte
		if err := rows.Scan(&id, &name, &enabled, &encrypted); err != nil {
			return nil, fmt.Errorf("storage: scanning account: %w", err)
		}
		record, err := s.decodeAccount(account.ID(id), name, enabled, encrypted)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterating accounts: %w", err)
	}
	return records, nil
}

// ReplaceAccountCredentials atomically replaces credentials without changing
// the account's name or enabled state.
func (s *Store) ReplaceAccountCredentials(
	ctx context.Context,
	id account.ID,
	credentials account.OAuthCredentials,
) error {
	if err := validateAccountID(id); err != nil {
		return err
	}
	if err := account.ValidateCredentials(credentials); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	encrypted, err := s.encryptCredentials(id, credentials)
	if err != nil {
		return err
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE accounts SET credentials = ? WHERE id = ?`,
		encrypted,
		string(id),
	)
	if err != nil {
		return fmt.Errorf("storage: replacing account credentials: %w", err)
	}
	return requireAffected(result)
}

// DisableAccount persists the account's disabled state.
func (s *Store) DisableAccount(ctx context.Context, id account.ID) error {
	if err := validateAccountID(id); err != nil {
		return err
	}
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE accounts SET enabled = 0 WHERE id = ?`,
		string(id),
	)
	if err != nil {
		return fmt.Errorf("storage: disabling account: %w", err)
	}
	return requireAffected(result)
}

// DeleteAccount permanently removes an account and its encrypted credentials.
func (s *Store) DeleteAccount(ctx context.Context, id account.ID) error {
	if err := validateAccountID(id); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("storage: deleting account: %w", err)
	}
	return requireAffected(result)
}

func (s *Store) encryptCredentials(id account.ID, credentials account.OAuthCredentials) ([]byte, error) {
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return nil, fmt.Errorf("storage: encoding account credentials: %w", ErrInvalid)
	}
	defer clear(encoded)
	encrypted, err := s.cipher.Encrypt(id, encoded)
	if err != nil {
		return nil, fmt.Errorf("storage: encrypting account credentials: %w", ErrInvalid)
	}
	return encrypted, nil
}

func (s *Store) decodeAccount(
	id account.ID,
	name string,
	enabled int64,
	encrypted []byte,
) (AccountRecord, error) {
	if enabled != 0 && enabled != 1 {
		return AccountRecord{}, ErrCorrupt
	}
	plaintext, err := s.cipher.Decrypt(id, encrypted)
	if err != nil {
		return AccountRecord{}, ErrCorrupt
	}
	defer clear(plaintext)

	var credentials account.OAuthCredentials
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return AccountRecord{}, ErrCorrupt
	}
	value, err := account.New(account.Identity{ID: id, Name: name}, credentials)
	if err != nil {
		return AccountRecord{}, ErrCorrupt
	}
	return AccountRecord{Account: value, Enabled: enabled == 1}, nil
}

func validateAccount(value account.Account) (account.Identity, account.OAuthCredentials, error) {
	identity := value.Identity()
	credentials := value.Credentials()
	if _, err := account.New(identity, credentials); err != nil {
		return account.Identity{}, account.OAuthCredentials{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return identity, credentials, nil
}
