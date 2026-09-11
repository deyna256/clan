# Budget snapshot storage and admission

Research for [#21](https://github.com/deyna256/clan/issues/21), 2026-09-11.
CLAN baseline: `7c03022`. Accepted behavior is recorded in
[ADR 0008](../decisions/0008-enforce-token-budgets-at-admission.md).
Snapshot persistence is accepted. The selected SQL tools are recorded in ADR 0007.

## Main finding

Admission is the decision that a request passed CLAN's checks and may proceed.
It is not confirmation that the provider received it, the response started, or
the request succeeded. Rejected requests consume no RPM; admitted requests keep
their RPM charge even if execution later fails.

The SQL transaction is only half the contract. `ratelimit.TryAcquire` consumes a
permit in memory, while budget-window opening needs persistence. Neither ordering
alone makes these operations atomic:

| Order | Failure to resolve |
|---|---|
| Consume RPM, then save opening | Saving fails after the permit was consumed |
| Save opening, then consume RPM | RPM rejects after a window was opened |

A lost commit acknowledgement also leaves the caller unsure whether the write
was saved. Adding a mutex or using serializable SQL does not resolve that uncertainty.
Go requires transaction results to be treated as unconfirmed after a commit error.
[Go transactions](https://go.dev/doc/database/execute-transactions).

Accepted on 2026-09-11: logical admission precedes persistence confirmation. Failed
window writes preserve that admission and block later budgeted attempts until saved.
A crash can lose unsaved openings as well as usage. See the
[admission contract](../decisions/0008-enforce-token-budgets-at-admission.md#admission-and-persistence).

Requiring durable opening before dispatch would need tentative RPM ownership and
ambiguous-commit recovery. That alternative adds states and may require changing
the limiter. `rate.Reservation.CancelAt` is not a general exact rollback operation.
[Reservation contract](https://pkg.go.dev/golang.org/x/time/rate#Reservation.CancelAt).

## Selected storage model

Use in-memory accounting with periodic budget snapshots, following Bifrost's local
store. The current contract is in
[ADR 0008](../decisions/0008-enforce-token-budgets-at-admission.md#snapshot-persistence-and-restart).
`usage.Advance` and `budget` remain the counting rules. Store absolute window counts
and opening times together; retrying a save does not add consumption again.

One writer orders saves and preserves changes received during a write. Keep the
latest unsaved state rather than every intermediate event. Attempt history is
separate and cannot reconstruct exact budgets when records are missing.

| Alternative | Why CLAN does not need it now |
|---|---|
| Per-key or per-attempt event sequences | Require replay and sequence-lifetime rules; the single process restores snapshots instead |
| A durable receipt for every event | Adds records and retention rules to support event replay |
| Atomic attempt accounting and budget writes | Couples durable attempt state to budgeting; active usage is counted in memory and history is separate |

Periodic snapshots can lose changes since the last successful save on a crash,
even when the database is healthy. CLAN accepts this. Failed saves still block
new attempts for affected budgeted keys until pending state is confirmed saved.
Ordinary delay between successful saves does not block admission.

## SQL and tools

| Choice | Selection and reason |
|---|---|
| SQL API | `database/sql` with explicit queries; it already owns connection pooling |
| PostgreSQL | `pgx/v5/stdlib`, which implements the standard SQL interface |
| SQLite | `modernc.org/sqlite` for builds without a C toolchain; `mattn/go-sqlite3` was the cgo alternative |
| Queries | Handwritten SQL for the small load/save contract; sqlc is unnecessary here |
| Migrations | Embedded SQL through Goose's instance-based provider; avoid building a migration engine |

Dependency versions are pinned in `go.mod`. See the [storage guide](../storage.md)
for the implemented API and database settings.
Sources: [connection pooling](https://go.dev/doc/database/manage-connections),
[pgx](https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib),
[modernc](https://pkg.go.dev/modernc.org/sqlite),
[mattn](https://github.com/mattn/go-sqlite3#installation),
[sqlc](https://github.com/sqlc-dev/sqlc),
[Goose provider](https://pressly.github.io/goose/documentation/provider/).

Keep saves short and provider I/O outside SQL transactions. Wrap the upsert in an
explicit transaction: pgx can return on cancellation before server cleanup finishes.
Without a separate commit, an interrupted statement could still save an old snapshot
after returning. Only commit after a successful upsert; that write acquired its
lock before Commit, so later conflicting writes wait for its outcome.
[pgx cancellation](https://pkg.go.dev/github.com/jackc/pgx/v5/pgconn#hdr-Context_Support).

Snapshot replacement needs no SQL read-modify-write calculation or application
lock protocol. Both databases serialize conflicting writes; the coordinator
still owns the order of Save calls.
[PostgreSQL locks](https://www.postgresql.org/docs/current/explicit-locking.html),
[SQLite isolation](https://www.sqlite.org/isolation.html).

SQLite uses one pooled connection, WAL and `synchronous=FULL`. WAL does not
add concurrent writers; FULL avoids the power-loss durability trade-off of NORMAL.
Apply connection settings to every relevant connection. Define time precision and
encoding before schema design so a round trip cannot silently move an expiry boundary.
[SQLite durability](https://www.sqlite.org/pragma.html#pragma_synchronous).

## Competitor lessons

The linked commits matched each repository's `main` when checked on 2026-09-11.
These findings concern the inspected paths, not every deployment or external plugin.

**Bifrost:** `PreLLMHook` evaluates limits before forwarding. Its local budget check
compares in-memory consumption with the cap; usage is charged after/during execution.
A worker resets and flushes counters to SQL, logging persistence failures. There
is no successful SQL commit required for every admission in this path.
[Governance](https://github.com/maximhq/bifrost/blob/e32fe9771d733503febb19a3c2ed1b80f04a9508/plugins/governance/main.go),
[store](https://github.com/maximhq/bifrost/blob/e32fe9771d733503febb19a3c2ed1b80f04a9508/plugins/governance/store.go),
[tracker](https://github.com/maximhq/bifrost/blob/e32fe9771d733503febb19a3c2ed1b80f04a9508/plugins/governance/tracker.go).

Its terminal-billing duplicate set is process-local, not durable deduplication.
Request counts in the inspected tracker are charged for successful completions;
CLAN charges RPM at admission regardless of the eventual result. Reuse the separation
of admission and accounting, not these different counting and recovery guarantees.

**CLIProxyAPI:** configured client-key authentication checks a key set. Upstream
quota/cooldown handling belongs to account selection, while `usage.Manager` queues
records for plugins. `Publish` does not return a persistence acknowledgement, and
the manager discards records downstream when no plugin is registered. These paths
do not implement CLAN-style durable token budgets per client access key.
[Authentication](https://github.com/router-for-me/CLIProxyAPI/blob/09a29bd345bc44c473abe7fd07859e32df2ea543/internal/access/config_access/provider.go),
[usage delivery](https://github.com/router-for-me/CLIProxyAPI/blob/09a29bd345bc44c473abe7fd07859e32df2ea543/sdk/cliproxy/usage/manager.go).

**LiteLLM:** common checks are followed by estimated-cost reservation, enabled unless
explicitly disabled. It reconciles reserved and actual cost after execution. Spend
counters use the cache/Redis path; SQL spend updates run separately, with a writer
that restores uncommitted buffered updates for retry. This is not one atomic
transaction spanning admission, Redis and SQL.
[Admission caller](https://github.com/BerriAI/litellm/blob/fbed17d567a62b14b8fc7d9ef13c5cd61a8d1ae0/litellm/proxy/auth/user_api_key_auth.py),
[reservations](https://github.com/BerriAI/litellm/blob/fbed17d567a62b14b8fc7d9ef13c5cd61a8d1ae0/litellm/proxy/spend_tracking/budget_reservation.py),
[SQL writer](https://github.com/BerriAI/litellm/blob/fbed17d567a62b14b8fc7d9ef13c5cd61a8d1ae0/litellm/proxy/db/db_spend_update_writer.py).

The default is not an unconditional hard ceiling: the code may shrink an estimate
to the remaining budget. `fail_closed_budget_enforcement` rejects an estimate that
does not fit and failed reservations instead. It also strengthens spend verification;
the documented database checks may be briefly cached. Do not copy reservation and
reconciliation into CLAN's deliberately reservation-free policy.
[Budget settings](https://docs.litellm.ai/docs/proxy/users#budget-reservation).

**Implication for CLAN:** these projects separate permission to proceed from durable
accounting, but offer different failure guarantees. For our single process, a shared
admission decision plus atomic, repeatable accounting writes avoids coordinating SQL
with distributed reservations. The accepted pending-window policy is CLAN's choice,
not a guarantee borrowed from a competitor.

## Checks that should drive implementation

Run the same storage contract suite on file-backed SQLite and a real PostgreSQL
instance. A PostgreSQL service can join the existing CI job; keep `just test` as
the full entry point. Missing integration prerequisites must fail the full run,
not silently produce a green result. Unit runs use `testing.Short()`.

Storage checks cover snapshot round trips and reopen, atomic replacement of both
windows, repeated saves after expiry, invalid state without partial writes and
unchanged key settings. Simulate a successful real commit whose acknowledgement
is hidden, then save the same snapshot again.

Coordinator checks cover cumulative 100/150/150 across windows, concurrent updates,
changes during a save, no stale overwrite, failed-save recovery and restoring only
the saved state after restart. These checks replace durable event-replay tests.

No database experiment or driver benchmark was run in this research. Next decisions:
save scheduling and shutdown behavior in #22.
