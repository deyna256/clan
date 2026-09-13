# ADR 0007: Use SQLite for the first release

Status: Accepted. Reviewed: 2026-09-13.

## Context

The first release runs as one process or container. It needs persistent account
and key settings, but no token budgets or database request history.

## Decision

Use SQLite as the only database. Persist account records, encrypted OAuth
credentials, access-key hashes and key settings. Keep active requests and
round-robin positions in process memory.

Store verification hashes rather than recoverable client keys. Encrypt OAuth
credentials, including refreshed tokens, before saving them. Keep the encryption
key outside the database and separate from the admin token.
Never include credentials in logs or client errors.

## Alternatives and consequences

SQLite avoids a separate database service. PostgreSQL would add deployment and
test work without serving an initial requirement, so it is deferred.

The [store](../../internal/storage/storage.go) uses `database/sql` with the pure-Go
`modernc.org/sqlite` driver and one connection for small configuration operations.
It initializes schema version 1 in a transaction using `PRAGMA user_version` and
rejects unknown versions or conflicting unversioned databases. Opening a store
authenticates existing credential bundles before allowing writes.

External write locks can delay context cancellation until the five-second SQLite
busy timeout expires. Database location and encryption-key provisioning belong to
application startup.
