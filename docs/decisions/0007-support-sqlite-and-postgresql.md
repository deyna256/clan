# ADR 0007: Support SQLite by default and PostgreSQL through environment configuration

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: access-key verification,
credential encryption and budget snapshot storage; application wiring pending.

## Decision

Support both SQLite and PostgreSQL in the initial implementation. Use SQLite by
default. When the operator provides PostgreSQL connection configuration through
environment variables, use PostgreSQL instead. Select one backend at startup.

An explicitly configured PostgreSQL backend must not silently fall back to SQLite
if its configuration is invalid or connection fails. Startup must report the failure
without exposing connection credentials or using a different database.

Both backends must support the same agreed management, account, access-key, limit
and consumption behavior. Switching backend configuration selects another database;
it does not itself copy data between databases.

## Deployment scope

Support one running CLAN instance per installation with either SQLite or PostgreSQL.
That process handles multiple client requests concurrently.
Do not run overlapping gateway processes or containers against the same installation.

Coordinate active-request limits, OAuth refresh and pending budget saves inside that
process. Save each key's budget windows atomically, as defined in
[ADR 0008](0008-enforce-token-budgets-at-admission.md#snapshot-persistence-and-restart). This version
does not need locks, admission blocks or active-request counters shared across processes.
Choosing PostgreSQL does not enable running multiple gateway instances.

Restarts and upgrades may cause downtime. Multiple active gateway instances,
horizontal scaling and overlapping rolling upgrades are deferred; supporting them
later requires a design for coordinating those processes.

## Secret storage

Distinguish credentials that verify incoming requests from credentials presented
to upstream services.

| Secret | Storage contract |
|---|---|
| CLAN access key | Generate a cryptographically random key and return its full value only when created. Store a verification hash, not a recoverable copy of the key |
| Upstream API key or OAuth tokens | Encrypt before saving to the database; CLAN must be able to decrypt them to authenticate with the upstream |
| Encryption key | Keep outside the database, independently of the admin token |
| Admin token | Continue supplying through `CLAN_ADMIN_TOKEN`, as established in [ADR 0006](0006-provide-management-api-without-bundled-ui.md) |

Apply the same secret-storage contract to both database backends and to refreshed
OAuth credentials. Changing the admin token must not affect decryption of stored
upstream credentials. Restoring a database backup containing encrypted credentials
also requires the corresponding encryption key.

Keeping encryption keys separate from encrypted data follows
[OWASP's storage guidance](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html#separation-of-keys-and-data).
Encryption protects stored credentials when the database is exposed without the key;
CLAN still needs the plaintext values when authenticating with an upstream.

## Access-key verification

Generate 32 bytes with `crypto/rand` and encode them as `clan_` followed by
43 canonical unpadded base64url characters. The 48-character key contains
256 random bits and no embedded access-key ID.

Store the SHA-256 digest of the complete canonical key, without salt or pepper.
These are generated random secrets, not human passwords. Use the digest for
indexed lookup; keep `accesskey.ID` independent from the secret and its hash.

`internal/accesskey` provides generation, hash derivation and verification.
Reject malformed keys without trimming or normalization. Compare digests with
`crypto/subtle.ConstantTimeCompare`; this does not guarantee uniform timing for
database lookups or the whole authentication request. A stored digest is never
a bearer credential.

Represent the verification hash as 32 bytes. The storage boundary must reject
other lengths before constructing that value. Algorithm changes require an
explicit compatibility decision, not a configurable algorithm registry.

The generated key is an ordinary string returned separately from its hash.
Callers own one-time delivery and must not log or persist the raw value. The
primitive does not automatically redact strings or promise memory zeroization.
The fixed format uses 32 random bytes because [`rand.Text`](https://pkg.go.dev/crypto/rand#Text)
may increase its output length. Strict base64 decoding still permits CR/LF;
exact length checks also matter. See the [decoder contract](https://pkg.go.dev/encoding/base64#Encoding.Strict)
and [verification tests](../../internal/accesskey/verification_test.go).

## Upstream-credential encryption

`internal/credentialcipher` uses AES-256-GCM with an explicitly supplied 32-byte
key. Construct the cipher with `New`; environment loading, provisioning and payload
serialization belong to callers. No encryption-disabled or plaintext fallback mode.

The library generates nonces through `cipher.NewGCMWithRandomNonce`. Store
`0x01 || nonce[12] || ciphertext || tag[16]` as bytes. Version 1 fixes this layout
and algorithm; reject unsupported versions and malformed records.

Authenticate the exact nonblank account and upstream IDs as associated data:
`0x01 || uint64BE(accountID byte length) || accountID bytes || upstreamID bytes`.
Names are excluded. A copied ciphertext cannot authenticate under a different
identity. When decrypting, use the destination record's identity, not one supplied
with the ciphertext. Moving credentials requires decrypting with the old identity and
re-encrypting with the new one. This does not authenticate all database metadata
or prevent rollback to an old valid ciphertext under the same identity.

Encrypt arbitrary serialized bytes, including empty payloads. Inputs remain
unchanged and outputs are caller-owned. On validation or authentication failure,
return no plaintext and an error without secret contents. Use fresh buffers;
failed AEAD decryption may overwrite its destination. The constructor must not
retain the caller's mutable key slice. No automatic redaction or zeroization claim.

Respect GCM message-size and allocation-length bounds. The random-nonce mode permits
at most 2^32 encryptions per key across all instances and restarts; provisioning
and rotation must respect that lifetime. Do not use a per-object counter as proof
of compliance. Key selection and rotation workflows remain separate work; existing
records and backups require their original key. See the
[random-nonce contract](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce)
and [AEAD buffer rules](https://pkg.go.dev/crypto/cipher#AEAD).

## Context and alternatives

SQLite keeps the default deployment small. PostgreSQL gives operators the requested
alternative. Supporting both requires testing the same behavior on each backend.

## Consequences and validation

Queries and migrations for each database must preserve the same application rules.
Run integration checks against both backends, including saved
configuration, credential updates, budget accounting and schema migration behavior.
SQLite-only tests do not establish PostgreSQL correctness.

Future checks must also verify access-key authentication without storing raw keys,
successful encryption and decryption of upstream credentials, encrypted storage after
OAuth renewal, rejection of corrupted ciphertext or a wrong encryption key, and
independence from admin-token rotation.

## Budget snapshot storage

Use `database/sql` with `pgx/v5/stdlib` for PostgreSQL and `modernc.org/sqlite`
for SQLite. The SQLite driver keeps builds independent of a C toolchain.
Use explicit SQL and embedded migrations through Goose's instance-based provider.

An explicit transaction prevents a cancelled upsert from committing after the
caller has started a newer save. pgx may return before server cleanup finishes;
commit only after the upsert succeeds and has acquired its write lock. A commit
error can still leave the result unknown. See [pgx cancellation](https://pkg.go.dev/github.com/jackc/pgx/v5/pgconn#hdr-Context_Support)
and [Go transactions](https://go.dev/doc/database/execute-transactions).

Handwritten SQL is sufficient for this small contract; query generation adds
little here. Goose supplies migration versioning, and the standard SQL API
already manages connection pools. SQLite uses WAL with `synchronous=FULL` to
avoid NORMAL's power-loss trade-off. See [SQLite durability](https://www.sqlite.org/pragma.html#pragma_synchronous).

Store window openings as UTC RFC3339Nano text to preserve the same instant and
nanosecond precision in both databases. SQL NULL represents an unopened window;
zone names and Go's monotonic clock reading are not persisted.
The supported serialized years are 0 through 9999.

The storage constructor receives an explicit target. Environment loading and the
default SQLite file location belong to application startup. See the
[storage guide](../storage.md) for the concrete contract and test setup.

Environment-variable names, SQLite location, backup/restore, key provisioning,
rotation and recovery also remain open.
