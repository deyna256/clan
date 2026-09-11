# ADR 0008: Enforce token budgets at admission and charge observed usage

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: usage transitions, budget windows,
snapshot storage and admission coordinator; request execution integration pending.

## Decision

Admission is CLAN's decision that a request or attempt may proceed after passing
all required checks. Before every upstream attempt, check counted token usage
against all configured access-key token budgets. Reject exhausted budgets with HTTP 429. Charge
observed usage without reserving estimated future tokens. This applies to the first
attempt, retries and account fallback.

An admitted attempt may finish and exceed the budget. Record the full overrun;
exhaustion alone does not interrupt a stream or reduce its output limit. Cancellation
and timeouts still apply. RPM and concurrency operate at the client-request level,
as defined in [ADR 0015](0015-use-token-bucket-rate-limits.md) and
[ADR 0010](0010-limit-concurrent-client-requests.md).

## Admission and persistence

Agreed on 2026-09-11: admission happens in the gateway process, before persistence
is confirmed. Coordinate the final checks, RPM acquisition for the first attempt,
and window opening in memory as one admission decision. Rejected attempts do not
open windows; rejected client requests consume no RPM.

If saving an opening fails after admission, continue the admitted attempt and keep
the write pending. Do not refund RPM or turn this into a pre-admission rejection.
New attempts for the affected key with configured token budgets receive HTTP 503
until the pending state is confirmed saved. This includes retries and fallback.
A budget that cannot be checked reliably before admission prevents dispatch.

Admission does not mean the provider received the request or execution succeeded.
It does not require an atomic transaction spanning memory and SQL.

## Fixed budget windows

Use independent fixed 5-hour and 7-day windows. A window opens on the first admitted
upstream attempt or newly charged known usage while no window is active, whichever occurs
first. After expiry, the same rule opens the next window. Later activity does not
extend the end, and renewing one window does not renew the other.

This includes admitted retries and account fallback. Checking eligibility alone
does not open a window; record admission only after all required checks pass.
Use elapsed durations of 5 and 168 hours, not calendar boundaries.

Track both windows even when their limits are absent. Changing, removing or
restoring a limit does not reset the window or its consumption. An absent limit
disables enforcement, zero denies admission, and a positive limit allows admission
while recorded consumption is below it. Negative limits are invalid.

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

## Budget-window transitions

`internal/budget` operates on values. Each window holds its opening time and used
tokens; its end is derived. The zero state has no open windows. `Limits` uses
optional `*int64` caps, where `nil` means absent.

- `Check` validates state and limits without changing either. Expired consumption
  does not count toward eligibility. `ErrExhausted` distinguishes a reached cap
  from invalid input.
- `Admit` records an already accepted attempt and opens expired or unopened
  windows. It does not check permission or limits.
- `Charge` adds a new known increment to both windows, opening them if needed.
  A zero increment leaves state unchanged, including expired windows.

Transitions validate inputs and return the previous state unchanged on failure.
Reject negative counts, overflow, usage without an opening, a zero accounting time,
or a time before either saved opening. Callers supply chronological accounting
times; the state does not retain every event time or correct clock changes.
The accounting coordinator owns synchronization. Window ends are computed when
needed; no reset worker is required. See the [window tests](../../internal/budget/budget_test.go)
and [Go time semantics](https://pkg.go.dev/time#hdr-Monotonic_Clocks).

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

## Cumulative usage contract

Adapters emit full normalized snapshots of the currently known counters,
not raw provider patches. Each counter records whether its value is known;
unknown differs from an observed zero. Input includes cache reads and writes,
which are separate subsets. Output includes reasoning. Preserve these details
without adding them to their parent counters again.

Use the explicit total when known. Otherwise, count known input and output;
where a parent is unknown, count its known subsets. A known total or both known
parents make the total token count known. This does not mean the stream has
finished. Missing categories remain unknown.

For each attempt, keep its latest snapshot and the amount already charged.
A pure transition returns the next state and only the positive difference above
that amount. Repeated snapshots and late breakdowns add no charge. For example,
100, 150, 150 charges 100, 50, 0.

Reject negative values, arithmetic overflow, decreasing or lost known breakdowns,
and inconsistent totals or subsets. Unknown counters must have a zero payload.
On error, return the previous state unchanged and no increment; the caller owns
error handling. Validate supplied previous state as well as the new snapshot.

An outdated total may become unknown when partial counters advance. For example,
known total 100 with input 80 can become input 90 with unknown total and output.
Keep the already charged 100 and add zero. Later input 90 and output 20 establish
110 and add 10. A new complete total below the amount already charged is invalid;
a partial lower bound below it is allowed. Adapters must retain still-valid totals
and known breakdowns when merging provider patches, and discard or recalculate
stale totals rather than present them as current measurements.

The transition does not save data. Keep attempt usage state in memory while the
attempt can report usage. Apply its next state and budget charge together under
the coordinator's synchronization; a failed transition changes neither.
Persist attempt details through the separate history mechanism in
[ADR 0009](0009-record-request-and-attempt-history.md).

## Accounting failures

| Failure | New upstream attempts | Already admitted work |
|---|---|---|
| Initial budget-state load fails | HTTP 503 for that key, even without token caps; retry loading on later requests | Other initialized keys follow their normal checks |
| An applicable configured budget cannot be checked reliably | HTTP 503, including retry/fallback; do not use an unverified stale counter | Continue |
| Saving a budget snapshot fails | HTTP 503 for the affected budgeted key until pending state is confirmed saved | Continue |
| Optional detailed-history write fails | Follow normal admission checks | Continue; see ADR 0009 |

Restore budget state before the first admission for every key, including keys
without token caps. A failed read is not an empty state: existing window openings
and counts must be known before adding usage. A successful read with no saved row
starts with unopened windows. This refinement was accepted on 2026-09-11.

After restoration, keys without configured token budgets skip cap enforcement and
are not blocked by save failures. Access and other applicable limit checks still
apply. Unknown provider usage is not a failed write of known usage and does not
itself block admission.

Keep current budget state in memory and retry saving it. Normal delay between
periodic saves is not a failure and does not block admission. After a failed save,
reconnecting alone does not unblock admission: confirm pending state was saved,
then apply normal checks. Report failures through logs and metrics without
credentials or request content.

## Accounting order

Process accounting changes in acceptance order for each access key. This includes
window openings and usage from all its attempts. Different keys need no shared
logical order; provider requests for one key may still execute concurrently.

Apply transitions at their accounting acceptance time. Saving later must not
recalculate charges or window openings using the save time. Already admitted
attempts continue reporting usage while saving is pending.

## Coordinator contract

`internal/admission` owns admission and live accounting in one process. It reuses
the permission, concurrency, RPM, usage and budget primitives. Provider execution,
account selection, HTTP errors and history remain outside this package.

- `Start` checks trusted model/account access, restores the key's saved state and
  admits the first attempt. It returns a request lease and a separate attempt handle.
- `NextAttempt` uses fresh permissions and budget caps while preserving the original
  key, model, slot and RPM charge. The executor owns sequential attempt execution.
- `Observe` applies a cumulative usage snapshot and its budget increment together,
  or changes neither. Attempt handles retain their own usage; copies share state.
- `Release` ends the lease after upstream cleanup. Repeated release is safe. It
  prevents new attempts but does not discard usage from an existing attempt.

Access and token/concurrency limits are immutable inputs for each call; do not
change cap pointers during a call. RPM uses its latest applied configuration,
including an explicit Unlimited entry. Admission never reapplies an older RPM
configuration. These settings do not form an atomic configuration transaction.

Load once per key before admission, without holding the state lock during I/O.
Waiting callers can cancel; a failed load permits a later retry. Under the short
state lock, check cancellation and budgets, prepare window opening, and take a
provisional slot. Acquire RPM last, then publish the prepared opening without
another fallible check. A rejection releases the provisional slot. Cancellation
after admission does not refund RPM or release the request before cleanup.

Sample accounting time under the state lock. Keep loaded keys for the process
lifetime and attempt state with its handle. No event queue or attempt registry
is required. `Observe` has no request context: cancellation must not discard
received consumption.

## Snapshot persistence and restart

Agreed on 2026-09-11: check and update budgets in memory, then periodically save
their current state. This replaces the earlier event-sequence and durable replay
design. The database stores both windows' opening times and absolute token counts
atomically per access key. Save only budget state, without overwriting key settings.

Saving the same snapshot again succeeds without adding consumption. This also
handles a lost commit acknowledgement. One writer owns save order: an older
snapshot must never overwrite a newer one. Changes accepted during a save remain
pending for a later save; completing the older save must not clear them.

Pending work is the latest state to save, not a queue of every accounting event.
No event sequence, gap check, durable receipt or event replay is required.
Detailed history is independent; saving attempt state and budget state together
is not required. History gaps and cleanup cannot alter budget counters.

After restart, restore saved windows before using them for admission. Do not replay
old attempt usage or reconstruct budgets from history. A crash may lose consumption
and openings since the last successful save, even with a healthy database.
Use normal admission checks without an administrator-only recovery block. An
exhausted saved budget or an unreadable initial snapshot still prevents admission.
Mark lost data where detectable; complete recovery is not guaranteed.

`Flush(ctx)` saves a finite set of pending keys through one writer. Waiting for
the writer is cancellable. Capture each key's latest state immediately before its
save, outside the I/O itself. Keys not reached before cancellation remain pending.
Each save has a five-second timeout; a failed key does not prevent trying the others
unless the flush context is cancelled.

A successful retry clears the save-failure block even when newer changes arrived
during that save. Those changes remain pending normally. Waiting for a completely
clean state could keep a continuously active key blocked indefinitely.

Use a five-second initial interval for `Run`; failed saves retry on the next tick.
This is a cadence, not a bound on persistence lag. The application supplies the
interval and error reporter and owns the worker goroutine. On shutdown, stop new
admission, drain request cleanup, cancel and join the worker, then perform a final
flush with a separate bounded context before closing storage. Cancelling the worker
does not discard pending state or perform an unbounded final save.

## Rationale and alternatives

Checking accounted usage avoids estimating generation length and managing token
reservations. It permits overruns from one long attempt or concurrent admitted work,
and the window charged depends on when usage becomes known. A concurrency limit reduces
active work but cannot guarantee a strict token limit.

Reserving an estimated token count, or a maximum that execution cannot exceed, could
reduce overruns. Both need rules to compare reserved tokens with actual usage and
adjust the counters. They are deferred. In-memory checks and periodic snapshots
follow [Bifrost's local tracker](https://github.com/maximhq/bifrost/blob/0145f674ec5f61b87f05c69940a8e1cbaa086dbc/plugins/governance/tracker.go#L80-L229).
CLAN keeps its own token-window and
save-failure policies; it does not adopt monetary accounting or cluster coordination.

Requiring a saved opening before dispatch would couple admission to each SQL write
and require recovery when a commit result is unknown. Consuming RPM before that
write can charge a rejected request; consuming it afterwards can leave a window
opened for an RPM rejection. Local admission followed by snapshots avoids that
cross-system transaction. The library's [reservation cancellation](https://pkg.go.dev/golang.org/x/time/rate#Reservation.CancelAt)
does not guarantee an exact refund after other reservations.

## Validation and open details

| Area | Required checks |
|---|---|
| Admission | Exhaustion in either window; per-attempt checks; concurrent admissions and preserved overruns; admitted streams continue |
| Normalization | Inclusive totals, separate cache categories, reasoning, known totals without breakdowns, partial/unknown versus zero |
| Attempts | Charge failures and cancellation; retries neither refund nor double-charge; request aggregation does not add a charge |
| Boundaries | Before/at/after expiry; late usage opening a window; cumulative snapshots across windows; duplicates after reset; concurrent opening |
| Failures | Unreliable checks reject before dispatch; failed saves preserve admitted work and RPM but block later budgeted attempts; normal save delay does not block |
| Persistence | Atomic window snapshots in both databases; repeated saves and lost commit acknowledgement; no stale overwrite or lost changes during saving; key settings and history remain independent |
| Restart | Saved counters remain authoritative; exhaustion/check failures still block; no manual-recovery block or claimed reconstruction of unsaved usage |

SQL tools and time encoding are defined in
[ADR 0007](0007-support-sqlite-and-postgresql.md#budget-snapshot-storage).
