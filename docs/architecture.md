# Architecture

CLAN is one gateway process with shared account inventory, access rules and limits.
The gateway is not yet runnable. The foundation packages below are implemented;
HTTP handling, provider adapters and application startup are still pending.

## Request path

The [request-flow diagram](../README.md#request-flow) shows the planned runtime flow:
an inbound adapter converts the client protocol, request execution manages attempts,
and a provider adapter calls the upstream service. Results return along that path.
These responsibilities do not require separate services.

Execution selects an eligible account for the requested upstream and model, then
asks admission whether the attempt may proceed. Selection alone grants no access
or quota. Execution also owns retries, cancellation and provider cleanup.
Protocol parsing stays in the adapters; shared request and event types are still
to be implemented. See [ADR 0003](decisions/0003-separate-request-execution-from-protocols.md).

## Foundation packages

All packages are under `internal/`. The links point to their public Go contracts.

| Package | Responsibility |
|---|---|
| [account](../internal/account/account.go) | Immutable account identity and API-key/OAuth credentials |
| [upstream](../internal/upstream/upstream.go) | Shared upstream ID type; configuration is pending |
| [accesskey](../internal/accesskey/access_key.go) | Immutable permissions and candidate filtering; [secret generation and verification](../internal/accesskey/verification.go) |
| [selection](../internal/selection/round_robin.go) | Round-robin positions per upstream and model |
| [concurrency](../internal/concurrency/limiter.go) | Active client-request slots per access key |
| [ratelimit](../internal/ratelimit/limiter.go) | Request permits and token-bucket configuration per access key |
| [usage](../internal/usage/usage.go) | Cumulative attempt usage and newly chargeable tokens |
| [budget](../internal/budget/budget.go) | Pure transitions for fixed 5-hour and 7-day token windows |
| [admission](../internal/admission/admission.go) | Combined admission checks, live accounting and ordered snapshot saves |
| [storage](../internal/storage/storage.go) | Budget snapshots and migrations in SQLite or PostgreSQL |
| [credentialcipher](../internal/credentialcipher/cipher.go) | Encryption of serialized upstream credentials |
| [retry](../internal/retry/retry.go) | Retry eligibility and remaining delay; execution owns waiting |

## Admission and accounting

The implemented coordinator combines the existing permission and limit primitives:

```text
Start -> access -> restore budget -> budget check -> slot -> RPM -> admit
NextAttempt -> fresh access and budget checks; keep the request's slot and RPM
Observe -> cumulative usage delta -> update both budget windows in memory
Flush -> save pending budget snapshots through SnapshotStore
Release -> release the request's slot after provider cleanup
```

These arrows show operation order, not package imports. The coordinator defines
the small `SnapshotStore` interface it needs; the SQL store satisfies it without
depending on admission. Application startup will connect the two.

The coordinator holds a short lock across related state changes and performs
database I/O outside it. Admitted attempts may exceed token budgets. Periodic saves
can lose unsaved usage on a crash; failed saves block later budgeted attempts for
the affected key. See [ADR 0008](decisions/0008-enforce-token-budgets-at-admission.md)
for failure, cancellation and shutdown rules, and [storage setup](storage.md)
for the database contract and tests.

## Remaining integration work

The entry point is empty. Management HTTP routes, shared protocol types, provider
adapters, OAuth renewal, model discovery and request history remain to be built.
The web panel belongs in a separate repository.

Resolve the remaining [management contract](decisions/0006-provide-management-api-without-bundled-ui.md#remaining-contract-decisions)
and each module's open behavior before implementing it. This includes account
availability, live configuration changes, conversation continuity, client errors,
and startup/shutdown wiring. Numeric timeout defaults remain open.
The [decision log](../README.md#decision-log) records agreed behavior; accepted
designs are not a claim that the corresponding feature is implemented.
