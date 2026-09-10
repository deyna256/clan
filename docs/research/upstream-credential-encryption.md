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

## Identity binding and buffers

[ADR 0007](../decisions/0007-support-sqlite-and-postgresql.md#upstream-credential-encryption)
defines the record format and identity binding. The
[implementation](../../internal/credentialcipher/cipher.go) accepts serialized bytes;
credential validation and serialization belong to callers.

Prefixing the account ID with its byte length separates it from the upstream ID
without an ambiguous delimiter. Preserve exact bytes, including non-UTF-8 IDs.
Use the destination record's identity when decrypting, not an identity copied
from the ciphertext. Binding prevents moving a ciphertext to another identity;
it does not prevent replaying an old valid record under the same identity.

Use fresh output buffers: `AEAD.Open` may overwrite its destination even when
authentication fails. Discard any returned bytes on failure.
[AEAD buffer contract](https://pkg.go.dev/crypto/cipher#AEAD).

## Bounds and validation

Check GCM message-size and allocation-length bounds. A separate arbitrary
credential-size setting is unnecessary here; callers own general memory limits.
An in-memory counter cannot enforce the per-key encryption limit across restarts
and backups. Key provisioning and rotation must account for that lifetime.
[Go GCM bounds](https://go.dev/src/crypto/cipher/gcm.go).

[Tests](../../internal/credentialcipher/cipher_test.go) use an independent encrypted
record and check payload round trips, tampering, identity binding and buffer
ownership. They need no injected random source, statistical nonce checks or
huge allocations. SQL, serialization and rotation need their own tests later.
