# Usage accounting

Status: Implemented. Issue: #63. Reviewed: 2026-09-22.

## Context

Usage accounting lets an admin inspect generation outcomes and observed token
usage without reading logs. CLAN stores request metadata in SQLite and exposes
summaries and request history through the admin API.

See the [API reference](../management-api.md#usage-and-request-history) for
parameters and response examples, [Running CLAN](../running.md) for configuration
and backups, and [architecture](../architecture.md) for the system overview.

## Scope

- One application instance using a local SQLite database.
- One record per `POST /v1/responses` with a recognized client key, including
  early query, Content-Type, encoding, body, JSON and concurrency rejections.
- `GET /api/usage`, `GET /api/requests` and `GET /api/requests/{id}`.
- Automatic schema migration and deletion of expired records.

Initially unrecognized keys, `GET /v1/models`, route errors and method errors
are excluded. Records contain no request or response content, IP addresses or
credentials. Reports return key and account IDs; names remain available through
`/api/client-keys` and `/api/accounts`.

## Code placement

The implementation uses existing packages and concrete dependencies, following
[the development guide](../development.md).

| Package | Responsibility |
|---|---|
| `internal/storage` | Schema migrations, connection pools and all request-history SQL |
| `internal/gateway` | Request identity, start time and early rejection delivery |
| `internal/execution` | Final log and database record after request cleanup |
| `internal/management` | Admin endpoints, filters and cursor encoding |
| `internal/app` | Retention configuration, worker lifetime and shutdown |

## Connections

The operational pool has one connection for authentication, account and key
changes, request inserts and retention. A separate read-only pool, limited to
four connections, serves reports. A full report pool therefore does not queue
key lookups behind reports.

This separation addresses measured contention: a probe on 900,000 records with
`modernc.org/sqlite` v1.58.0 delayed a key lookup and insert by 2.15 seconds during
a report on one connection. With WAL and a separate read pool, that operation
completed below the probe's millisecond resolution. These are database-operation
measurements, not end-to-end generation latency. A tested covering index halved
summary time but grew the database by 23%; further optimization remains driven
by representative load tests.

After migrations and validation, `Open` enables WAL, verifies the returned mode,
and opens the report pool with `mode=ro`. Both pools use the same busy timeout;
`Close` releases both. Reports use the 30-second management context deadline
and close their rows promptly. Deadlines request cancellation, but cannot impose
a hard limit on SQLite I/O.

[SQLite WAL](https://www.sqlite.org/wal.html) requires a local file system.
Long-running readers can delay checkpoints and grow the WAL file. A backup must
include committed WAL data: use SQLite's backup command or stop CLAN first, as
described in [backup and upgrades](../running.md#backup-and-upgrades).

## Data

Table `requests`:

| Column | Type | Meaning |
|---|---|---|
| `id` | TEXT PRIMARY KEY NOT NULL | Gateway-generated request ID, also used in logs |
| `finished_at` | INTEGER NOT NULL | Completion time, Unix milliseconds |
| `key_id` | TEXT NOT NULL | Initially authenticated client key |
| `account_id` | TEXT NULL | Last selected account, including failed credential preparation; NULL if none was selected |
| `model` | TEXT NULL | Requested model after successful parsing; NULL for earlier failures |
| `result` | TEXT NOT NULL | Safe outcome code shared with the final log |
| `duration_ms` | INTEGER NOT NULL | Time from gateway start through delivery and upstream cleanup, excluding accounting I/O |
| `response_started` | INTEGER NOT NULL | Whether HTTP status and headers were committed: 0 or 1 |
| `input_tokens`, `output_tokens`, `total_tokens` | INTEGER NULL | Observed counters; NULL means unknown |

Durations and known counters must be nonnegative. The gateway generates request
IDs as `req_` plus `crypto/rand` text. A duplicate ID fails without replacing the
existing record.

Indexes cover `(finished_at)`, `(key_id, finished_at)` and
`(account_id, finished_at)`. There are no foreign keys: deleting a key or account
preserves its request history.

## Recording

The gateway retains the initially authenticated key ID, request ID and start
time. Execution checks the key again before admission, so revocation during
upload prevents generation while the rejection remains attributable to the key.
The model is retained only after successful parsing; rejected bodies are not
parsed again for accounting.

Both finalization paths share one function that emits `request finished` and
inserts the record:

- Admitted requests finish through `Stream.Close`, including JSON generations.
  The concurrency slot remains held until the insert returns. Repeated closure
  does not create another log or record.
- Early rejections finish through `gateway.reject`, after error delivery and
  cleanup. They acquire no slot. `Result.Admitted` tells the gateway whether it
  owns rejection delivery and recording.

Completion time, duration and outcome are captured once after cleanup, before
accounting I/O. Writes run outside `Executor.mu`. Accounting uses the last
selected account, kept separately from the active account used for cancellation.
If attempt A fails and B fails during preparation, the record belongs to B.

The insert uses `context.WithTimeout(context.WithoutCancel(ctx), time.Second)`
so request cancellation does not discard the record. This deadline bounds pool
waiting, but SQLite lock waiting can reach the configured five-second busy
timeout. A failed insert logs a safe warning and leaves the client response
unchanged. There is no retry queue. See [Go context](https://pkg.go.dev/context#WithoutCancel)
and [SQLite busy timeout](https://www.sqlite.org/c3ref/busy_timeout.html).

Outcome precedence is cancellation or deadline, then delivery failure, then the
original result. Upload-error delivery may detach its context; accounting still
uses the original request's cancellation state. `response_started` records local
HTTP commitment, not confirmed client receipt, and stays true if a later write
or flush fails.

| Situation | Record |
|---|---|
| Generation completes | `completed` or `incomplete`, with observed counters |
| Client disconnects mid-stream | `canceled`, with any counters already observed |
| Key is revoked mid-stream | `canceled` |
| Gateway stops mid-stream | `canceled`, written before the store closes |
| Retry selects another account | Last selected account |

Shutdown waits for HTTP handlers and active execution before closing the store.
Early rejection recording remains available while handlers drain, even after
execution stops accepting requests.

## Retention

`CLAN_USAGE_RETENTION` accepts a positive Go duration and defaults to `2160h`
(90 days). Invalid, zero and negative values stop startup.

The application starts one hourly worker. Each run deletes records older than
its cutoff in batches of at most 1,000, until none remain. The pinned SQLite
build lacks `DELETE … LIMIT`, so deletion uses a limited `rowid` subquery.
Records exactly at the cutoff are kept. Shutdown cancels and waits for the
worker before closing the store; startup cleanup also stops it if present.

## Migrations

Goose v3.28.0 runs embedded migrations on the operational connection. The Go
baseline is registered per provider with `WithGoMigrations` and
`WithDisableGlobalRegistry(true)`. There is no global registration or distributed
migration lock. See [ADR 0015](../decisions/0015-use-goose-for-migrations.md).

Every open starts with read-only checks, even when no migration is pending:

1. Validate `PRAGMA user_version`, required schema objects and Goose history.
2. Authenticate existing account credentials before making changes.
3. Release preflight connections before Goose uses the single-connection pool.

History is read through `database.NewStore(...).ListMigrations`. Provider methods
such as `GetDBVersion` can initialize the history table, so they are unsuitable
for read-only preflight checks.

| `user_version` | Applied Goose history | Action |
|---|---|---|
| 0 | absent or only 0 | Create the baseline, then apply migration 2 |
| 1 | absent or only 0 | Adopt the legacy schema, then apply migration 2 |
| 1 | 0, 1 | Apply migration 2 |
| 2 | 0, 1, 2 | Open the upgraded database |

Version 0 permits only SQLite internals and a valid Goose table. Version 1
requires `accounts` and `access_keys`, with no object named `requests`. Version 2
also requires `requests`. Missing, future, duplicate, unapplied or inconsistent
history is rejected.

Migration 1 creates the original tables and sets `user_version = 1` in an empty
database, or adopts an already validated legacy version 1 without changing it.
Migration 2 creates `requests` and its indexes and sets `user_version = 2`.
Each migration is transactional, with its schema changes, version marker and
Goose history committed together. After `Up`, CLAN checks the resulting schema
and history before enabling WAL. These migrations do not modify credentials.

Preflight rejection leaves schema, history and journal mode unchanged. A later
migration failure may leave earlier migrations committed; restart resumes from
that state. Goose may also leave an initialized version table after a failed
baseline, which explains the accepted history containing only version 0.

There are no down migrations. CLAN 0.1.x refuses schema version 2; rollback
requires restoring a pre-upgrade backup, not just reverting the binary.

Goose sources: [provider execution](https://github.com/pressly/goose/blob/v3.28.0/provider_run.go),
[database store](https://github.com/pressly/goose/blob/v3.28.0/database/dialects.go)
and [provider options](https://github.com/pressly/goose/blob/v3.28.0/provider_options.go).

## Admin API

All three endpoints require the admin token. Unknown query parameters and
invalid values return `422` in the existing problem format, without reflecting
query values. Public parameters, defaults and JSON examples are in the
[API reference](../management-api.md#usage-and-request-history).

### Filters and summaries

Filters combine with AND and use SQL placeholders. Group names map to fixed SQL
expressions; input never supplies SQL text. Huma parses comma-separated
`group_by` values and rejects unknown or repeated dimensions.

RFC 3339 bounds are normalized to UTC and checked for `from < to`. Both SQL
bounds round up to milliseconds, preserving inclusive `from` and exclusive
`to`. For `[00:00:00.1235, 00:00:00.1245)`, `.124` matches and `.123` does not.
A valid interval shorter than a millisecond may select no records. Bounds must
remain valid JSON timestamps after defaults are applied.

`GET /api/usage` defaults `to` to now, captured once, and `from` to effective
`to` minus 30 days. Summary metrics come from one SQL snapshot:

- Each token counter is summed independently. All-unknown counters are omitted;
  known zero is emitted as 0. Total is never inferred from input and output.
- `unknown_usage` counts each request with any unknown counter once, even when
  its known counters contribute to sums.
- `results` counts requests by outcome code; the API does not classify failures.
- Only selected dimensions are emitted. Unknown selected accounts and models
  are JSON null. Days use UTC dates.
- Results sort by selected dimensions in the fixed order key, account, model,
  day, regardless of their order in `group_by`. No matches returns `items: []`.

### Request pagination

`GET /api/requests` uses descending `(finished_at, id)` order, with the ID breaking
timestamp ties. Missing date bounds stay unbounded. The page limit defaults to
50 and accepts 1–100.

The query fetches `limit + 1` rows. When another page exists, `next_cursor` contains
the last emitted position and a hash of `from`, `to`, `key_id`, `account_id`,
`model` and `result`. The extra row is not the cursor position. The cursor is
omitted on the last page; there is no total count.

Hashing uses normalized UTC bounds and exact filter strings. Equivalent offsets
and fractional notation match; absent bounds remain absent. `limit` is excluded,
so clients may change page size. Storage receives a typed position rather than
the encoded cursor.

Cursors use URL-safe base64. Decoding bounds their size and validates fields,
including rejecting duplicate or unknown JSON fields. Malformed cursors and
changed filters return `422`. The hash is not a signature: a well-formed manual
change is not guaranteed to be detected. Clients should treat the cursor as
opaque. This follows [AIP-158](https://google.aip.dev/158) pagination conventions,
with readable tokens and CLAN's field names and error format.

Pages do not share a database snapshot. Inserts and retention may change later
pages. Record responses omit unknown counters, account and model, while preserving
known zero counters. `GET /api/requests/{id}` returns that same record format or
`404` if the record is absent or expired.

## Verification

Tests follow [AAA and the project test rules](../development.md#tests-exercise-behavior).
SQLite and HTTP integration tests skip under `testing.Short()`.

| Area | Required checks |
|---|---|
| Migrations | Empty and legacy databases; encrypted credentials and keys preserved; reopen; interrupted migration recovery; invalid versions/history and wrong encryption keys rejected without mutation |
| Recording | JSON/SSE completion, early rejections, disconnect, revocation and shutdown; last selected account; accurate timing; one final log and record after cleanup; no records for excluded traffic |
| Delivery | Failure before and after HTTP commitment; original cancellation takes precedence; canceled uploads remain attributable |
| Failed inserts | Response unchanged, safe warning, concurrency slot released; cancellation cannot suppress the final write; pool waits respect the recording deadline |
| Retention | Hourly scheduling, batch limit, cutoff boundary, multiple batches and worker shutdown |
| Summaries | Independent filters, exact groups and UTC days; independent nullable sums, observed zero, unknown usage and empty results |
| Pagination | Timestamp ties, bounds and fractional milliseconds; filter binding, normalized dates, changed page size, malformed cursors, last page and retention between pages |
| API safety | Authentication, validation, JSON/OpenAPI nullability and omission; secrets excluded from records and diagnostics |
| Connections | WAL after upgrade; read-only report pool; reports do not occupy the operational connection; writes proceed while readers hold snapshots |

SQLite lock-wait probes are separate from context-deadline tests: a one-second
context does not guarantee a one-second return. Additional indexes, aggregation
or queues require load measurements before adoption.
