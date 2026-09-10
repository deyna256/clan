# Access-key generation and verification

Research for [issue #11](https://github.com/deyna256/clan/issues/11), 2026-09-10.
Reviewed against `main` at `db0189c`. The contract was accepted on 2026-09-10.
[ADR 0007](../decisions/0007-support-sqlite-and-postgresql.md) requires hash-only
storage for access keys.

## Evidence and choice

LiteLLM generates random tokens and hashes the complete token with unsalted
SHA-256. It handles human passwords separately with salted scrypt.
[Generation](https://github.com/BerriAI/litellm/blob/fbed17d567a62b14b8fc7d9ef13c5cd61a8d1ae0/litellm/proxy/management_endpoints/key_management_endpoints.py#L4142),
[hashing](https://github.com/BerriAI/litellm/blob/fbed17d567a62b14b8fc7d9ef13c5cd61a8d1ae0/litellm/proxy/utils.py#L6577).

Bifrost's virtual-key model includes both a value and an indexed hash, plus
encryption and rotation fields. That model is broader than our hash-only scope.
Its configuration-drift hash is not evidence of how it verifies credentials.
[Stored model](https://github.com/maximhq/bifrost/blob/e32fe9771d733503febb19a3c2ed1b80f04a9508/framework/configstore/tables/virtualkey.go#L244).

For CLAN, use SHA-256 over independently generated 256-bit secrets. Guessing
that random key space is infeasible; password hashing addresses a different
problem: low-entropy human choices. This is our threat-model assessment, not a
recommendation to hash passwords with SHA-256. A salt or separately managed
pepper does not justify extra storage, lookup and rotation rules for this task.
Reassess if we introduce user-chosen secrets or new storage threats.
[OWASP password guidance](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html).

Go's standard library covers generation, encoding, hashing and comparison.
`rand.Text` is safe but allows its output length to grow. Use 32 random bytes for
a fixed format. On our Go baseline, `rand.Read` fills the buffer and returns no
error; an entropy-source failure terminates the process.
[Go crypto/rand](https://pkg.go.dev/crypto/rand).

## Format and validation

[ADR 0007](../decisions/0007-support-sqlite-and-postgresql.md#access-key-verification)
defines the key format and storage rules. The [implementation](../../internal/accesskey/verification.go)
provides generation, hash derivation and verification.

Base64 `Strict` decoding still ignores CR/LF. Checking the exact encoded and
decoded lengths excludes those characters; strict decoding also rejects unused
trailing bits. [Go base64 contract](https://pkg.go.dev/encoding/base64#Encoding.Strict).

`subtle.ConstantTimeCompare` protects the digest comparison. It does not make a
database lookup or a whole authentication request take constant time.
[Go comparison contract](https://pkg.go.dev/crypto/subtle#ConstantTimeCompare).

## Validation

[Tests](../../internal/accesskey/verification_test.go) check the generated format,
an independent SHA-256 vector, mismatched keys and malformed input. Statistical
randomness tests and injected entropy sources would add little to this wrapper
around `crypto/rand`. One-time key delivery and database lookup need tests when
those operations are implemented.
