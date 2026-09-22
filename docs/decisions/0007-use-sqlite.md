# ADR 0007: Use SQLite for the first release

Status: Accepted. Reviewed: 2026-09-22.

## Context

CLAN runs as one process or container. It needs persistent account and key
settings and request metadata for usage reports, but no token budgets.

## Decision

Use SQLite as the only database. Persist account records, encrypted OAuth
credentials, access-key hashes, key settings and
[request metadata](0016-record-request-metadata.md). Keep active requests and
round-robin positions in process memory.

An account record contains its CLAN ID, name, enabled flag and an encrypted
credential bundle: ChatGPT account ID, access token, refresh token and access-token
expiry. The ChatGPT ID identifies the provider account or workspace; it is separate
from the CLAN ID and is not a unique identifier for a person.

A client-key record contains its ID, name, verification hash, enabled flag and
concurrency limit. Pending OAuth sessions stay in memory. Do not store email,
plan details or the raw ID token.

Store verification hashes rather than recoverable client keys. Encrypt OAuth
credentials, including refreshed tokens, before saving them. Keep the encryption
key outside the database and separate from the admin token.
Never include credentials in logs or client errors.

OAuth completion replaces credentials only if the account is still enabled and
the stored credentials match the snapshot that started the operation. Use one
conditional update so deletion or newer credentials cannot be undone by a late
result. The encrypted record serves as its revision; no schema field is needed.
Re-enabling an account re-encrypts the same credentials with a fresh nonce so
results from before disablement cannot become valid again.

## Alternatives and consequences

SQLite avoids a separate database service. PostgreSQL would add deployment and
test work without serving an initial requirement, so it is deferred.

The [store](../../internal/storage/storage.go) uses `database/sql` with the pure-Go
`modernc.org/sqlite` driver. WAL mode and one operational connection serve
authentication, configuration writes, accounting and retention. A separate
read-only pool of up to four connections serves reports so their queries do not
occupy the operational connection. They still share CPU and disk.

[Goose migrations](0015-use-goose-for-migrations.md) upgrade the schema after
compatibility and credential checks. WAL and the report pool open only after
migration and validation succeed.

WAL requires a local file system and changes
[backup requirements](../running.md#backup-and-upgrades): copying only the main
database file from a running gateway is insufficient.

External write locks can delay context cancellation until the five-second SQLite
busy timeout expires. Database location and encryption-key provisioning belong to
application startup.

Provider token rotation and SQLite persistence cannot form one transaction.
A process crash between them may require a new sign-in.
