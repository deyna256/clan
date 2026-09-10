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

## Selected contract

Add these operations to `internal/accesskey`, alongside the existing identity
and permission types:

```go
type VerificationHash [32]byte

func Generate() (raw string, hash VerificationHash)
func Hash(raw string) (VerificationHash, bool)
func (hash VerificationHash) Verify(raw string) bool
```

- Generate `clan_` plus 43 unpadded base64url characters: 48 ASCII characters in
  total, carrying 256 random bits. Identity stays in `accesskey.ID`; it is not
  embedded in the secret. No configurable entropy or imported-key issuance.
- Accept only this canonical format. Reject whitespace, padding, wrong case in
  the prefix, invalid alphabet and noncanonical trailing bits. Do not trim input.
  Base64 `Strict` still ignores CR/LF, so enforce the exact length and decoded
  size too. [Go base64 contract](https://pkg.go.dev/encoding/base64#Encoding.Strict).
- Hash the entire canonical string with SHA-256. Invalid input returns a zero
  hash and `false`; the caller must check the boolean. Syntax cannot prove
  randomness; new keys must come from `Generate`.
- Use the digest for a future indexed lookup. Keep it separate from the stable
  access-key ID. A stored digest must never be accepted as a bearer credential.
- Store the 32-byte digest. The storage boundary must reject other byte lengths
  before constructing this type. No textual record parser or algorithm registry.
- `Verify` validates the presented key and compares all 32 digest bytes using
  `subtle.ConstantTimeCompare`. This protects the comparison step, not the timing
  of a whole authentication request or database lookup.
  [Go comparison contract](https://pkg.go.dev/crypto/subtle#ConstantTimeCompare).
- Return the raw key as an ordinary string for explicit delivery. Callers must
  not log it or retain it for later retrieval. There is no automatic redaction
  or memory-zeroization promise. One-time delivery belongs to the management API.

## Implementation and test brief

One executor owns the new source file and external `accesskey_test` tests; the
primary agent owns ADR updates. Follow the [development guide](../development.md).

Test generated format and verification, an independently computed SHA-256 vector,
a different valid key, a changed digest, and a digest presented as a raw key.
Use a compact malformed-input table for length, prefix, alphabet, whitespace,
padding and noncanonical trailing bits. Check that large malformed inputs fail
without panic. Keep expected values independent of the implementation.

No statistical randomness tests, timing benchmarks, injected entropy framework,
secret wrapper, database, HTTP or changes to permission checks. Run
`just format --check`, `just deps`, `just lint` and `just test`.
