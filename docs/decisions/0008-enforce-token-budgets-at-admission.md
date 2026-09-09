# ADR 0008: Enforce token budgets at admission and charge observed usage

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: not started.

## Decision

Admission means checking whether a request or attempt may proceed. Before every
upstream attempt, check token usage already counted against all
configured access-key token budgets. Reject exhausted budgets with HTTP 429. Charge
observed usage without reserving estimated future tokens. This applies to the first
attempt, retries and account fallback.

An admitted attempt may finish and exceed the budget. Record the full overrun;
exhaustion alone does not interrupt a stream or reduce its output limit. Cancellation
and timeouts still apply. RPM and concurrency operate at the client-request level,
as defined in [ADR 0011](0011-use-sliding-window-rpm.md) and
[ADR 0010](0010-limit-concurrent-client-requests.md).

## Fixed budget windows

Use independent fixed 5-hour and 7-day windows. A window opens on the first admitted
request or newly charged known usage while no window is active, whichever occurs
first. After expiry, the same rule opens the next window. Later activity does not
extend the end, and renewing one window does not renew the other.

Charge each new known increment to both windows active when it is accepted for
accounting, regardless of when the attempt passed admission. The time history is saved
does not select the window. At exactly the expiry boundary, a new increment belongs
to the successor. Late usage can open and exhaust a new window before any new request.

For cumulative stream usage of 100 before expiry and 150 after it, charge 100 to the
old window and only 50 to the successor. If usage is first reported after expiry,
charge it there in full. Duplicate updates must neither recharge nor move consumption
between windows. Admission and charging must agree on a single successor window.

These are CLAN limits, not rolling or calendar windows and not automatically aligned
with provider quotas. The number of allowed tokens is configured separately.

## Token accounting unit

Count input and output tokens equally, including cache reads/writes and reasoning.
Normalize provider counters so every token counts once: a cache or reasoning detail
already included in a total must not be added again. Retain available breakdowns.

Input of 1,000 tokens including 700 cached tokens and output of 200 including 50
reasoning tokens consume 1,200 budget tokens. A known total is chargeable without a
complete category breakdown. Accounting is token-based; cost tracking is deferred.

Charge known usage from every attempt, including failed and cancelled attempts.
Retrying does not refund earlier consumption. Keep usage linked to its attempt;
forming request totals must not charge it again. A failed attempt using 500 tokens
followed by a successful one using 1,000 consumes 1,500 in total.

## Missing or partial usage

Count only known usage; do not estimate the missing portion. Preserve
known counters and mark incomplete attempt usage and affected summaries. Unknown
usage is distinct from observed zero. A missing breakdown does not make a known
total incomplete.

Missing usage alone does not block later requests. Other access and limit checks
still apply. Totals may be lower than actual usage and must show when data is missing.

## Accounting failures

| Failure | New upstream attempts | Already admitted work |
|---|---|---|
| An applicable configured budget cannot be checked reliably | HTTP 503, including retry/fallback; do not use an unverified stale counter | Continue |
| Known usage cannot be saved | HTTP 503 for the affected budgeted key while unsaved charges remain pending | Continue |
| Optional detailed-history write fails | Follow normal admission checks | Continue; see ADR 0009 |

Keys without configured token budgets do not require budget checks, but still undergo
access and other applicable limit checks. Unknown provider usage is not a failed
write of known usage and does not trigger the pending-charge block.

Keep known unsaved charges in process memory and retry saving without double
charging. Reconnecting to the database alone does not unblock admission: confirm
the pending charges were saved, then apply normal checks. Report accounting failures through
logs and metrics without credentials or request content.

## Atomic persistence and process failure

Save known usage increments, affected budget counters and duplicate-processing
protection in one transaction in the primary database. Concurrent writes must not
lose increments. Retrying after a lost commit acknowledgement must not double-charge
or move an existing charge into a new window. Optional detailed-history writes are
independent; history retention cannot reset active budget counters.

Do not require an unfinished-accounting record before each upstream call. Normal
budget checks and window opening still apply. No separate durable journal or external
queue is selected. Pending charges and temporary blocks belong to the single process
specified in [ADR 0007](0007-support-sqlite-and-postgresql.md).

A crash may lose unsaved usage and pending charges. After restart, use saved counters
and normal admission checks. Do not block requests until an administrator intervenes
just because usage may have been lost. An exhausted saved budget or a failed budget
check still blocks admission. Mark missing data where detectable; not every lost
update can be recovered. An interrupted history record alone does not block new
attempts or allow resending an upstream request. See the
[recovery research](../research/usage-accounting-recovery.md).

## Rationale and alternatives

Checking accounted usage avoids estimating generation length and managing token
reservations. It permits overruns from one long attempt or concurrent admitted work,
and the window charged depends on when usage becomes known. A concurrency limit reduces
active work but cannot guarantee a strict token limit.

Reserving an estimated token count, or a maximum that execution cannot exceed, could
reduce overruns. Both need rules to compare reserved tokens with actual usage and
adjust the counters. They are deferred. The selected policy
uses Bifrost's admission approach, without adopting its monetary accounting or storage
architecture; see the [comparison](../research/token-budget-enforcement.md).

## Validation and open details

| Area | Required checks |
|---|---|
| Admission | Exhaustion in either window; per-attempt checks; concurrent admissions and preserved overruns; admitted streams continue |
| Normalization | Inclusive totals, separate cache categories, reasoning, known totals without breakdowns, partial/unknown versus zero |
| Attempts | Charge failures and cancellation; retries neither refund nor double-charge; request aggregation does not add a charge |
| Boundaries | Before/at/after expiry; late usage opening a window; cumulative snapshots across windows; duplicates after reset; concurrent opening |
| Failures | 503 without dispatch for initial/retry attempts; admitted work continues; no bypass via fallback or restored connectivity with pending charges |
| Persistence | Atomic counters and deduplication in both databases; lost commit acknowledgement; independence from optional history and retention |
| Restart | Saved counters remain authoritative; exhaustion/check failures still block; no manual-recovery block or claimed reconstruction of unsaved usage |

Define write timing, retry scheduling, pending-charge bounds and shutdown handling
with the accounting module. Delayed writes must preserve the rule that accounting
time determines the budget window. The exact storage mechanism is still open.
