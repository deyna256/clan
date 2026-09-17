# Usage accounting — design

Status: Accepted, not implemented. Issue: #63. Date: 2026-09-17.

## Context

CLAN reports token usage only in the `request finished` log line
(`internal/execution/log.go`). An admin cannot see who used how much without
reading logs. This change stores one metadata record per request in SQLite and
reports it through the admin API. It also adds the first schema migration.

## Scope

In scope:

- a `requests` table and the migration that adds it to existing databases;
- WAL mode and a read-only connection pool, so reports do not block requests;
- writing a record when a request finishes;
- deleting records older than a retention period;
- `GET /api/usage`, `GET /api/requests` and `GET /api/requests/{id}`;
- tests and documentation for the above.

Out of scope:

- request or response content, IP addresses and credentials in records;
- key and account names in usage responses; clients read them from
  `/api/client-keys` and `/api/accounts`;
- down migrations; rollback means restoring a backup of the database file.

## Code placement

Follows `docs/development.md` ("Packages and responsibility", "Interfaces follow
their consumers", "Goroutine lifetime"). No new packages or interfaces.

| Package | Adds |
|---|---|
| `internal/storage` | WAL and the read pool; embedded migrations; `*Store` methods to insert a record, delete records before a time, summarize usage, list records after a position and read one record. SQL stays here |
| `internal/execution` | the insert next to the `request finished` log line, without holding `Executor.mu`; `Executor` keeps using the concrete `*storage.Store` |
| `internal/management` | the three endpoints; cursor encoding and filter hash. Storage receives a typed `(finished_at, id)` position, not the cursor string |
| `internal/app` | `CLAN_USAGE_RETENTION`; the retention goroutine, owned by the application, stopped and awaited in `shutdown` and `closeStartup` before the store closes |

## Connections

Today the store uses one connection (`SetMaxOpenConns(1)`, ADR 0007), and every
`/v1` request looks up its key through it. A usage report on that connection makes
generation wait.

Measured with `modernc.org/sqlite` v1.58.0 on 900,000 records (about 10,000
requests a day for 90 days, 120–155 MiB):

| Operation | Time |
|---|---|
| 30-day summary by key | 1.9 s |
| 30-day summary by key, model and result | 2.2–2.3 s |
| 90-day summary by day and account | 6.0 s |
| 30-day summary for one key | 0.08 s |
| one list page | 1 ms |
| one insert | 0.22 ms |
| key lookup and insert during a report, one connection | **2.15 s** |
| the same, WAL with a separate read pool | **0 ms** |

A covering index only halved the summary time and grew the file by 23%.

Design, following PocketBase, which uses the same driver (WAL, one writer
connection, a separate pool for reads):

- `Open` sets `PRAGMA journal_mode=WAL` on the writer after migrations succeed,
  so a database that CLAN rejects is not changed. The pragma cannot run inside
  a transaction, and the mode is stored in the database file.
- The read pool is opened after that.
- The writer stays at one connection and serves key lookups, account operations,
  record inserts and retention.
- A second `*sql.DB` opened with `mode=ro` and the same busy timeout serves
  `/api/usage` and `/api/requests`, limited to four connections
  (`SetMaxOpenConns(4)`), which is enough for admin use.
  A write through it fails with `attempt to write a readonly database`.
- `Close` closes both.

Probe results: WAL persisted after reopening; an open read transaction did not
block a write; the reader saw committed rows.

Consequences, from the [SQLite WAL documentation](https://www.sqlite.org/wal.html):

- all processes must be on the same host; network file systems are not supported;
- `clan.db-wal` and `clan.db-shm` appear next to the database. Copying only
  `clan.db` from a running gateway is not a valid backup. Use
  `sqlite3 clan.db ".backup backup.db"` or stop CLAN first;
- a reader that never stops would prevent checkpoints and grow the WAL file.
  Reports are short and bounded by the 30-second management timeout.

Pre-aggregated tables (used by LiteLLM and new-api) are not needed for admin
reports at this volume. They belong with per-key limits, which need a period
total on every request; they would be updated in the same transaction as the
record.

## Data

Table `requests`:

| Column | Type | Meaning |
|---|---|---|
| `id` | TEXT PRIMARY KEY NOT NULL | request ID, the same as `request_id` in logs |
| `finished_at` | INTEGER NOT NULL | completion time, Unix milliseconds |
| `key_id` | TEXT NOT NULL | client key |
| `account_id` | TEXT NULL | account of the last attempt; NULL when no account was selected |
| `model` | TEXT NOT NULL | requested model |
| `result` | TEXT NOT NULL | outcome code from `logResult`, such as `completed`, `incomplete`, `canceled`, `concurrency_limit` or `provider_limit` |
| `duration_ms` | INTEGER NOT NULL | request duration |
| `response_started` | INTEGER NOT NULL CHECK (0 or 1) | the HTTP response was committed |
| `input_tokens`, `output_tokens`, `total_tokens` | INTEGER NULL | NULL means unknown, not zero |

Indexes: `(finished_at)`, `(key_id, finished_at)`, `(account_id, finished_at)`.

The gateway generates request IDs as `req_` plus `crypto/rand` text
(`internal/gateway/gateway.go`), so `id` is unique without client input.

The table has no foreign keys. Deleting a key or an account keeps its records.

## Recording

`Executor.logResult` runs once per admitted or rejected request:

- `admit` calls it when admission fails (`internal/execution/execution.go`);
- otherwise `Stream.Close` calls `Executor.finish`, which calls it. `Generate` is
  built on `Stream`, so JSON and SSE requests share this path.

The record is written next to the log line.

- Recorded: every request whose key was resolved, including rejections such as
  `concurrency_limit`.
- Not recorded: requests rejected before the key was resolved (`unauthorized`,
  `closed`, a failed key lookup). They have no key ID, and recording invalid keys
  would let anyone fill the database.
- The write is synchronous and happens before the key slot is released.
- `finish` cancels the request context before `logResult`. The write uses
  `context.WithoutCancel(ctx)` with a 1-second timeout, so a busy database holds
  the slot for at most one second.
- `requestState.accountID` is protected by `Executor.mu`. `finish` reads it under
  that lock, releases the lock and then writes. The lock is never held during I/O.
- A failed write does not change the client response. It is logged at WARN
  without request content.

Stream outcomes:

| Situation | Record |
|---|---|
| Stream completes | `completed` or `incomplete`, tokens from the observed usage |
| Client disconnects mid-stream | `canceled`, `response_started = 1`, tokens as observed so far, usually NULL |
| Key revoked mid-stream | `canceled` |
| Gateway stops mid-stream | `canceled`, written before the store closes |
| Retry moves to another account | `account_id` of the last attempt |

Token columns store `usage.Snapshot` as it is: an unknown counter is NULL.

Shutdown order: `app.shutdown` closes the store only after `server.Shutdown`
(which waits for handlers) and `Executor.Close` (which waits for active requests)
have returned (`internal/app/app.go`). Records of finishing requests are written
first.

## Retention

- `CLAN_USAGE_RETENTION` is a Go duration parsed with `time.ParseDuration`.
  Default: `2160h` (90 days). Zero, negative and invalid values stop startup.
- A background job runs every hour and deletes records older than the retention
  period, 1,000 rows per statement, until none are left. `modernc.org/sqlite`
  does not support `DELETE … LIMIT`, so each statement is
  `DELETE FROM requests WHERE rowid IN (SELECT rowid FROM requests WHERE finished_at < ? LIMIT 1000)`.
- The application owns the job and stops it before the store closes, both in
  `shutdown` and when startup fails. The loop uses `time.NewTicker`, returns on
  context cancellation, and is awaited with `sync.WaitGroup.Go`.

## Migrations

Use `github.com/pressly/goose/v3` v3.28.0 with `goose.NewProvider`,
`goose.DialectSQLite3`, the existing `*sql.DB` and migrations embedded with
`embed`. Each migration runs in its own transaction.

Probe results with `modernc.org/sqlite` v1.58.0 and `SetMaxOpenConns(1)`:

- a 0.1.x database with data was upgraded and kept its data;
- a repeated run applied nothing;
- an empty database got the full schema;
- goose creates `goose_db_version` before migration 1 runs;
- `PRAGMA user_version` set inside a migration is committed with it, and rolled
  back when the migration fails.

Migrations:

1. Go migration, version 1, based on `PRAGMA user_version`:
   - `1`, a database from CLAN 0.1.x: check that `accounts` and `access_keys`
     exist; create nothing.
   - `0`: fail if any table other than `goose_db_version` and `sqlite_*` exists
     (today's `schemaConflict` rule, which must now skip the goose table); create
     `accounts` and `access_keys` as `createSchema` does today; set
     `user_version = 1`.
   - any other value: fail.
2. `00002_requests.sql`: create `requests` and its indexes; set
   `PRAGMA user_version = 2`.

Each migration sets `user_version` to its own number. CLAN 0.1.x checks this value
and refuses an upgraded database with `storage: unsupported schema version 2`.
Without it, 0.1.x would open the database, because its two tables still exist.

`initialize`, `createSchema`, `checkSchemaTables` and `schemaVersion` in
`internal/storage/storage.go` are replaced by the provider. `Open` still verifies
account credentials after migrating.

## Admin API

The endpoints are under `/api`, require the admin token and use the existing
`register` helper (`internal/management/management.go`). As in the existing
lists, unknown query parameters and invalid values return `422` in the existing
problem format (Huma `RejectUnknownQueryParameters`). The new query names are
added to the safe error locations in `internal/management/errors.go`.

`group_by` is a Huma `[]string` query field. Huma reads comma-separated values by
default, and `enum` and `uniqueItems` tags reject unknown and repeated values.
Each value maps to a fixed SQL expression from an allowlist; request values are
never inserted into SQL text. All filters use placeholders.

### `GET /api/usage`

| Parameter | Meaning |
|---|---|
| `from`, `to` | RFC 3339. Default: the last 30 days. `from` is inclusive, `to` is exclusive. `from` must be before `to` |
| `group_by` | comma-separated list of `key`, `account`, `model`, `day`. Default: `key`. Repeated values are invalid |
| `key_id`, `account_id`, `model` | optional filters, combined with AND |

Example for `group_by=key,model`:

```json
{
  "from": "2026-09-01T00:00:00Z",
  "to": "2026-10-01T00:00:00Z",
  "items": [{
    "key_id": "k1",
    "model": "gpt-test",
    "requests": 412,
    "results": {"completed": 398, "canceled": 9, "provider_limit": 5},
    "input_tokens": 1830211,
    "output_tokens": 90412,
    "total_tokens": 1920623,
    "unknown_usage": 11
  }]
}
```

- An item has the group fields selected by `group_by` (`key_id`, `account_id`,
  `model`, `day`) and the metric fields. `day` is a UTC date, `YYYY-MM-DD`.
  `account_id` is `null` for requests without an account.
- `results` counts requests per result code. The server does not decide which
  codes are failures.
- Token fields sum known values only. `unknown_usage` counts requests where any
  token counter is NULL.
- Items are ordered by the group fields.

### `GET /api/requests`

| Parameter | Meaning |
|---|---|
| `from`, `to` | optional RFC 3339 range, same rules as above |
| `key_id`, `account_id`, `model`, `result` | optional filters, combined with AND |
| `limit` | 1–100, default 50 |
| `cursor` | value of `next_cursor` from the previous page |

```json
{
  "items": [{
    "id": "req_…",
    "finished_at": "2026-09-17T10:00:00.123Z",
    "key_id": "k1",
    "account_id": "a1",
    "model": "gpt-test",
    "result": "completed",
    "duration_ms": 5120,
    "response_started": true,
    "input_tokens": 4211,
    "output_tokens": 312,
    "total_tokens": 4523
  }],
  "next_cursor": "…"
}
```

- Order: `finished_at DESC, id DESC`. The second key keeps records with the same
  time from being skipped or repeated across pages.
- Pagination follows the rules of Google AIP-158 with CLAN names (`limit`,
  `cursor`, `next_cursor` instead of `page_size`, `page_token`,
  `next_page_token`):
  - `next_cursor` is omitted on the last page (AIP-158: empty);
  - the cursor is URL-safe base64 and documented as opaque;
  - a cursor used with different filters returns `422`, as are damaged cursors
    (AIP-158: `INVALID_ARGUMENT`).
- The cursor holds the last `(finished_at, id)` and a hash of the filters. It is
  not encrypted: AIP-158 asks for tokens that users cannot parse, but this API is
  for the admin only, and a readable cursor exposes nothing new.
- There is no `total`; AIP-158 makes it optional, and counting a 90-day table on
  every page is slow. The existing offset lists keep their format.
- Unknown token counters and a NULL `account_id` are omitted, as in the log line.

### `GET /api/requests/{id}`

Returns one record in the item format above, or `404` when it does not exist or
retention deleted it.

## Tests

AAA layout. Tests that use SQLite or HTTP skip under `testing.Short()`.

Migrations:

- empty database: schema created, `user_version = 2`;
- 0.1.x database with an account and a key: upgraded, data kept,
  `user_version = 2`;
- second open: nothing applied;
- empty database with an unrelated table: error;
- `user_version = 99`: error.

Recording:

- JSON request: `completed`, account and tokens stored;
- completed stream: the same, `response_started = 1`;
- client disconnects mid-stream: `canceled`, unknown tokens stored as NULL, not 0;
- key revoked mid-stream: `canceled` record;
- retry on another account: `account_id` of the last attempt;
- `concurrency_limit` rejection: record without `account_id`;
- invalid key: no record;
- failing write: client response unchanged, WARN logged. The failure comes from a
  trigger that aborts inserts into `requests`, created through a second
  connection to the same temporary file; no test interface is added;
- gateway stops mid-stream: record present after shutdown.

Retention, with `testing/synctest`: old records deleted, fresh records kept, more
than 1,000 old records deleted in one run.

`/api/usage`:

- each `group_by` value and a combination;
- filters;
- `from` inclusive, `to` exclusive;
- day boundaries in UTC;
- `unknown_usage` and sums of known tokens only;
- invalid range, unknown and repeated `group_by` values: `422`.

`/api/requests`:

- walking all pages over records with equal `finished_at` returns each record once;
- cursor with changed filters: `422`;
- damaged cursor: `422`;
- last page: no `next_cursor`.

`/api/requests/{id}`: existing record returns 200; unknown ID returns 404.

Secrets: a marker string in credentials and request content does not appear in
records or responses, like the marker tests in `internal/app/config_test.go`.

Configuration: `CLAN_USAGE_RETENTION` default; invalid, zero and negative values
fail startup.

Connections:

- after `Open`, `PRAGMA journal_mode` returns `wal`, also for a 0.1.x database;
- a rejected database (unrelated table, unknown version) keeps its journal mode;
- while a read transaction is open on the read pool, a generation request still
  finishes and its record is written;
- the read pool rejects writes.

## Documentation

Updated in the same change:

- `docs/management-api.md`: the three endpoints, the cursor list format and how it
  differs from the offset lists (section "Lists"), `unknown_usage`.
- `docs/architecture.md`: recording, retention, migrations and the two connection
  pools.
- `docs/running.md`: `CLAN_USAGE_RETENTION` in the settings table; the database
  must be on a local file system; back up with `sqlite3 ".backup"` or with CLAN
  stopped, not by copying `clan.db` alone; back up before upgrading; CLAN 0.1.x
  cannot open an upgraded database.
- `docs/decisions/0007-use-sqlite.md`: request history is now stored; WAL with one
  writer connection and a read pool; migrations through goose.
- `README.md`: mention usage records in "Why CLAN". The "First release" section
  describes 0.1.0 and stays unchanged.
- New ADRs, following the format of `docs/decisions/0001-use-go.md`:
  - `docs/decisions/0015-use-goose-for-migrations.md`;
  - `docs/decisions/0016-record-request-metadata.md`: what is stored, what is not,
    and why the write is synchronous.
