// Package credentialcipher encrypts serialized OAuth credentials for storage.
package credentialcipher

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"math"
	"strings"

	"github.com/deyna256/clan/internal/account"
)

const (
	recordVersion  = 1
	recordOverhead = 1 + 12 + 16
	maxPlaintext   = ((1 << 32) - 2) * aes.BlockSize
)

// Cipher authenticates credentials against their exact account ID.
// Construct it with [New]. Callers must limit encryption to 2^32 messages per key
// across all cipher instances, restarts and restores; this object keeps no counter.
type Cipher struct {
	aead cipher.AEAD
}

// New constructs an AES-256-GCM cipher from exactly 32 key bytes.
// It does not retain the supplied slice. Provisioning must supply random key
// material independently of the admin token; key length cannot prove entropy.
func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("credential cipher: key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns version 1 followed by a random nonce, ciphertext and tag.
// The account ID must be nonblank. Empty and arbitrary binary plaintext are supported.
// Inputs remain unchanged; the returned record is caller-owned. Errors return nil.
func (c *Cipher) Encrypt(accountID account.ID, plaintext []byte) ([]byte, error) {
	if uint64(len(plaintext)) > maxPlaintext || len(plaintext) > math.MaxInt-recordOverhead {
		return nil, errors.New("credential cipher: plaintext is too large")
	}
	aad, err := associatedData(accountID)
	if err != nil {
		return nil, err
	}
	return c.aead.Seal([]byte{recordVersion}, nil, plaintext, aad), nil
}

// Decrypt authenticates a version 1 record under the exact supplied account ID.
// Inputs remain unchanged; successful plaintext is caller-owned. Every validation
// or authentication failure returns nil plaintext and a secret-free error.
func (c *Cipher) Decrypt(accountID account.ID, record []byte) ([]byte, error) {
	if len(record) < recordOverhead || record[0] != recordVersion || uint64(len(record)-recordOverhead) > maxPlaintext {
		return nil, errors.New("credential cipher: invalid encrypted record")
	}
	aad, err := associatedData(accountID)
	if err != nil {
		return nil, err
	}
	plaintext, err := c.aead.Open(nil, nil, record[1:], aad)
	if err != nil {
		return nil, errors.New("credential cipher: authentication failed")
	}
	return plaintext, nil
}

func associatedData(accountID account.ID) ([]byte, error) {
	if strings.TrimSpace(string(accountID)) == "" {
		return nil, errors.New("credential cipher: account ID must be nonblank")
	}
	return []byte(accountID), nil
}
