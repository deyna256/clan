# Authenticated upstream-credential encryption

Research for [issue #12](https://github.com/deyna256/clan/issues/12), 2026-09-10.
Baseline: `main` at `2ccc394`, Go 1.27. The contract was accepted on 2026-09-10.
[ADR 0007](../decisions/0007-support-sqlite-and-postgresql.md) requires encrypted
upstream credentials and an external encryption key independent of the admin token.

## Evidence and choice

Use AES-256-GCM from the standard library. OWASP recommends authenticated modes
such as GCM and keeping encryption keys separate from encrypted data.
[OWASP storage guidance](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html).

Go's `NewGCMWithRandomNonce` generates and prepends a 12-byte nonce. The library
also supplies the 16-byte authentication tag. It removes manual nonce handling
from CLAN. The same key must encrypt no more than 2^32 messages over its lifetime.
[Go cipher documentation](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce).

Bifrost uses AES-256-GCM with a manually generated nonce and a base64 record. Its
helper also has global configuration, no associated data, and plaintext passthrough
when encryption is disabled. Those behaviors do not fit our explicit-key boundary.
[Pinned helper](https://github.com/maximhq/bifrost/blob/e32fe9771d733503febb19a3c2ed1b80f04a9508/framework/encrypt/encrypt.go#L71).

## Selected contract

Use `internal/credentialcipher` with a concrete cipher constructed from an
explicitly supplied 32-byte key:

```go
func New(key []byte) (*Cipher, error)
func (c *Cipher) Encrypt(accountID account.ID, upstreamID upstream.ID, plaintext []byte) ([]byte, error)
func (c *Cipher) Decrypt(accountID account.ID, upstreamID upstream.ID, record []byte) ([]byte, error)
```

The constructor validates key length and retains cipher state, not the caller's
key slice. Later mutation of that slice does not change the cipher. Provisioning
must supply random key material; length validation cannot prove entropy. No
password derivation or admin-token dependency. Construction through `New` is required.

Accept arbitrary serialized bytes, including empty payloads. Credential validity
and API-key/OAuth serialization belong outside this module. Inputs remain unchanged;
returned buffers belong to the caller. No text encoding or JSON requirement.

Record format: `0x01 || nonce[12] || ciphertext || tag[16]`, adding 29 bytes.
Version 1 fixes the algorithm and layout. Reject unknown versions and truncated
records; never treat unrecognized data as plaintext. Nonces remain library-owned.

Require exact, nonblank account and upstream IDs as authenticated associated data:

```text
AAD = 0x01 || uint64BE(accountID byte length) || accountID bytes || upstreamID bytes
```

The length separates the two IDs without delimiter ambiguity. Preserve exact
bytes, including non-UTF-8 strings permitted by current identity types. Exclude
display names so renaming does not require re-encryption. Callers supply the
destination record's identity; do not trust an identity copied from the ciphertext.

A ciphertext copied to another identity must fail authentication. Moving credentials
to another account or upstream requires decrypting under the old identity and
re-encrypting under the new one. This does not protect all database metadata or
prevent rollback of old valid ciphertext under the same identity.

On validation or authentication failure, return nil output and an error containing
no keys, plaintext or ciphertext. Wrong-key and tampering errors need no distinct
cryptographic details. Use fresh destinations: `AEAD.Open` may overwrite its
destination even on failure. Discard any returned bytes when it fails.
[AEAD buffer contract](https://pkg.go.dev/crypto/cipher#AEAD).

## Bounds and tests

Check the GCM message-size bound and allocation-length arithmetic before encryption;
do not impose an arbitrary credential-size setting. General memory limits belong
to callers. The per-key 2^32-message bound spans all instances, restarts and backups;
an in-memory counter would not enforce it. Key provisioning and rotation must
respect that lifetime bound. No key ring or rotation workflow in this issue.
[Go GCM bounds](https://go.dev/src/crypto/cipher/gcm.go).

Test representative API-key and full OAuth bytes, binary/empty payloads, an
independent fixed encrypted record, wrong keys/identities, ambiguous ID pairs,
malformed versions/lengths and tampered nonce/ciphertext/tag. Verify nil output
on failure, unchanged input buffers and independence from the original key slice.
No injected random source, statistical nonce tests or enormous test allocations.

SQL, serialization, environment configuration
and rotation remain separate work. Follow the [development guide](../development.md)
and run all four `just` checks.
