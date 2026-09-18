package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deyna256/clan/internal/accesskey"
)

// A stored limit of -1 means unlimited; nonnegative values are caps.
const unlimitedConcurrencyLimit = -1

// CreateAccessKey stores a key's identity, status, verification hash and limit.
func (s *Store) CreateAccessKey(
	ctx context.Context,
	key accesskey.AccessKey,
	hash accesskey.VerificationHash,
	concurrencyLimit int,
) error {
	identity := key.Identity()
	_, err := accesskey.New(identity, key.Enabled())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if concurrencyLimit < unlimitedConcurrencyLimit {
		return fmt.Errorf("%w: concurrency limit is invalid", ErrInvalid)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO access_keys
		 (id, name, verification_hash, enabled, concurrency_limit)
		 VALUES (?, ?, ?, ?, ?)`,
		string(identity.ID),
		identity.Name,
		hash[:],
		key.Enabled(),
		concurrencyLimit,
	)
	if err != nil {
		return classifyWriteError("creating access key", err)
	}
	return nil
}

// ListAccessKeys returns all keys in ascending local-ID order.
func (s *Store) ListAccessKeys(ctx context.Context) ([]AccessKeyRecord, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, verification_hash, enabled, concurrency_limit
		 FROM access_keys ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: listing access keys: %w", err)
	}
	defer rows.Close() // Iteration reports errors through rows.Err; early exits already return an error.

	var records []AccessKeyRecord
	for rows.Next() {
		record, err := scanAccessKey(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterating access keys: %w", err)
	}
	return records, nil
}

// FindAccessKeyByHash finds a key by its stored verification hash.
func (s *Store) FindAccessKeyByHash(
	ctx context.Context,
	hash accesskey.VerificationHash,
) (AccessKeyRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, name, verification_hash, enabled, concurrency_limit
		 FROM access_keys WHERE verification_hash = ?`,
		hash[:],
	)
	record, err := scanAccessKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessKeyRecord{}, ErrNotFound
	}
	if err != nil {
		return AccessKeyRecord{}, fmt.Errorf("storage: finding access key: %w", err)
	}
	return record, nil
}

// RevokeAccessKey disables a key while preserving its identity and hash.
func (s *Store) RevokeAccessKey(ctx context.Context, id accesskey.ID) error {
	if err := validateAccessKeyID(id); err != nil {
		return err
	}
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE access_keys SET enabled = 0 WHERE id = ?`,
		string(id),
	)
	if err != nil {
		return fmt.Errorf("storage: revoking access key: %w", err)
	}
	return requireAffected(result)
}

// UpdateAccessKeyConcurrency changes only a key's concurrency limit.
func (s *Store) UpdateAccessKeyConcurrency(
	ctx context.Context,
	id accesskey.ID,
	concurrencyLimit int,
) error {
	if err := validateAccessKeyID(id); err != nil {
		return err
	}
	if concurrencyLimit < unlimitedConcurrencyLimit {
		return fmt.Errorf("%w: concurrency limit is invalid", ErrInvalid)
	}
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE access_keys SET concurrency_limit = ? WHERE id = ?`,
		concurrencyLimit,
		string(id),
	)
	if err != nil {
		return fmt.Errorf("storage: updating access-key concurrency: %w", err)
	}
	return requireAffected(result)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccessKey(row rowScanner) (AccessKeyRecord, error) {
	var id string
	var name string
	var hash []byte
	var enabled int64
	var limit int
	if err := row.Scan(&id, &name, &hash, &enabled, &limit); err != nil {
		return AccessKeyRecord{}, fmt.Errorf("storage: scanning access key: %w", err)
	}
	return decodeAccessKey(id, name, hash, enabled, limit)
}

func decodeAccessKey(
	id string,
	name string,
	storedHash []byte,
	enabled int64,
	limit int,
) (AccessKeyRecord, error) {
	if len(storedHash) != len(accesskey.VerificationHash{}) {
		return AccessKeyRecord{}, ErrCorrupt
	}
	if enabled != 0 && enabled != 1 {
		return AccessKeyRecord{}, ErrCorrupt
	}
	if limit < unlimitedConcurrencyLimit {
		return AccessKeyRecord{}, ErrCorrupt
	}
	var hash accesskey.VerificationHash
	copy(hash[:], storedHash)
	key, err := accesskey.New(accesskey.Identity{ID: accesskey.ID(id), Name: name}, enabled == 1)
	if err != nil {
		return AccessKeyRecord{}, ErrCorrupt
	}
	return AccessKeyRecord{
		Key:              key,
		VerificationHash: hash,
		ConcurrencyLimit: limit,
	}, nil
}
