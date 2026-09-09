# ADR 0009: Record request history with separate execution attempts

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: not started.

## Decision

Record the client request separately from its upstream attempts. Preserve its final
outcome alongside failures and account switches. Retain details for one year by
default and compute reports on demand. This defines logical records, not a SQL schema.

```text
Request: succeeded
  Attempt 1, account A: upstream limit
  Attempt 2, account B: response received
```

| Record | Contents |
|---|---|
| Request | ID, start/end times, access-key ID when known, inbound API, requested model, ordinary/streaming mode, final outcome |
| Attempt | Request reference, attempt number, upstream, account ID, actual model, duration, result and reason for retry |
| Attempt usage | Known input/output counts and available cache/reasoning details; unknown usage explicit |
| Error | Category, code and safe description, excluding credentials and request content |

For streams, record time to the first meaningful content event; start/end events alone
do not count. Preserve usage accounting times separately from request start times.

Include gateway rejections. A request rejected before dispatch has no upstream attempt;
do not invent an upstream failure or access-key identity. Do not store message
bodies, model responses or individual stream chunks by default. This decision does
not add an option to record those contents.

## Reporting

Provide a paginated request list, request details with attempts, and consumption/error
summaries for a selected period. Filter by time range, access key, model, account and
outcome. The [management API](0006-provide-management-api-without-bundled-ui.md)
defines the common list contract; exact filters and response schemas belong to this module.

Compute reports with database queries over retained records. No separate daily,
weekly, monthly or yearly summary tables are required initially. Add them only
if measurements show that report queries need them.

Report client-request outcomes (succeeded, failed, rejected), upstream-attempt counts,
known tokens and attempts with unknown usage. Support breakdowns by access key, model,
upstream and account. Count a two-attempt request once as a client request; joins and
aggregation must not multiply it. Derive request usage from attempts instead of
maintaining another independent total. Duplicate accounting updates must not add usage.

Reports are limited by retention, missing provider usage and history-write gaps. Do
not imply that every request was recorded. Keeping less history means reports cover a shorter period. There is no separate
year-long summary after the underlying records are deleted.

## Retention and deletion

Retain completed request/attempt records and their usage for one year by default,
configurable through installation settings. Automatically remove expired completed
records and their details. Do not delete records of active requests just because they are old.

Deleting an account or access key does not erase its history or consumption. Keep its
ID and name snapshot from request time, and indicate deletion, without retaining
credentials. A new same-name entity gets a different ID and does not inherit history.
Define how deletion affects active requests in the account/key modules.

History cleanup must not reset consumption or reopen an active exhausted budget.
Keep admission counters; this decision does not require summing all history on each
request. Charging rules and accounting persistence are owned by
[ADR 0008](0008-enforce-token-budgets-at-admission.md).

## Process termination

After the previous gateway process has stopped, the new process marks its unfinished
history as interrupted, with an unknown final outcome. Preserve attempts and
known usage; an interrupted record does not show whether upstream generation
completed. Do not turn unknown usage into zero or replay generation during recovery.

Do not infer termination from record age or change work active in the current process.
The [single-instance deployment](0007-support-sqlite-and-postgresql.md#deployment-scope)
avoids cross-instance ownership and failure detection.

## History-write failures

Failure to save detailed history does not reject a request that otherwise passes checks
or interrupt its response. Report the failure through logs and metrics without
credentials or message contents. Gaps are possible; recovery of failed writes is not
guaranteed. This does not bypass access checks, budget checks or usage accounting.

## Rationale and consequences

Separate attempts explain failover and preserve usage from unsuccessful upstream calls.
One year of details supports later investigation without another reporting data store.
The cost is storage growth and potentially expensive queries; indexes and cleanup
must handle the amount of data we actually store. On-demand reporting avoids keeping
extra summary tables up to date. History helps explain requests but may not contain
all usage billed by the upstream.

## Validation and open details

Check retries with distinct attempts, rejection without an attempt, interrupted streams,
unknown versus zero usage, safe errors and filters/pagination. Verify request counts
are not multiplied, usage is not duplicated and accounting timestamps are preserved.

Check default/configured retention, expired completed records with associated details,
active records surviving cleanup and unchanged budget counters. Recovery must preserve
known usage without replay or altering the current process's active work. Inject a
history-write failure: execution continues, failure is observable and access/budget
checks remain enforced. Deletion must keep history linked to the original account
or key, not a new one with the same name.

Define schemas, indexes, reporting time buckets, cleanup scheduling, startup sequencing,
historical references and history buffering/retry policy with this module.
