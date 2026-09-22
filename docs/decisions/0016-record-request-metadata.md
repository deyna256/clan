# ADR 0016: Record request metadata synchronously

Status: Accepted. Reviewed: 2026-09-22.

## Context

Administrators need consumption history without processing logs. Reports must
include failed generations and distinguish unknown token usage from zero.

## Decision

Record each `POST /v1/responses` whose key passed initial authentication, including
early rejections. Exclude initially invalid keys, model catalog calls and routing
or method errors.

Store IDs, times, parsed model, outcome, HTTP commitment state and observed token
counts, as defined in the [data contract](../specs/usage-accounting.md#data).
Unknown account, model and counters are NULL. Store no content, IP addresses,
credentials or account/key names. Deleting an account or key keeps its history.

Write synchronously after delivery and cleanup, before releasing an admitted
request's slot. Early rejections use the same final logging and recording without
a slot. Request cancellation does not cancel the write, which has a one-second
context deadline; SQLite lock waits can exceed it. Failed writes produce a safe warning
without changing the response. Shutdown waits for recording before closing
storage. Expired records are deleted hourly, with a default retention of 90 days.

## Consequences

Synchronous writes avoid a queue and its overflow and shutdown rules, but can
delay slot release. A crash or failed insert can lose a record; this is not a
guaranteed billing ledger. Reports do not enforce token budgets.

[WAL and separate report connections](0007-use-sqlite.md) isolate report queries
from the operational connection. Add queues, indexes or pre-aggregation only when
representative tests show a need. See the [API contract](../management-api.md#usage-and-request-history)
and [recording rules](../specs/usage-accounting.md#recording).
