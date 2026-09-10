# Fixed token-budget windows

Research for [issue #9](https://github.com/deyna256/clan/issues/9), 2026-09-10.
Reviewed against `main` at `db0189c`.
The contract below was accepted on 2026-09-10 and is recorded in
[ADR 0008](../decisions/0008-enforce-token-budgets-at-admission.md).

## What we can reuse

Bifrost derives recurring budget boundaries from an anchor, or uses calendar
alignment. Its checks and resets share the same expiry rules. CLAN should share
one expiry rule too, but keep its own event-opened windows. Bifrost's reset workers
and distributed accounting are outside this task.
[Window calculation](https://github.com/maximhq/bifrost/blob/e32fe9771d733503febb19a3c2ed1b80f04a9508/framework/configstore/tables/budget.go#L204),
[budget checks](https://github.com/maximhq/bifrost/blob/e32fe9771d733503febb19a3c2ed1b80f04a9508/plugins/governance/store.go#L2115).

LiteLLM has scheduled resets with database and cache coordination. This supports
keeping persistence atomic; it does not justify a scheduler in our value module.
[Reset job](https://github.com/BerriAI/litellm/blob/fbed17d567a62b14b8fc7d9ef13c5cd61a8d1ae0/litellm/proxy/common_utils/reset_budget_job.py#L1450).

For CLAN, a five-hour window opened at 10:00 expires at 15:00. If the next
qualifying event is at 17:00, the next window is 17:00–22:00. We do not create
empty windows during inactivity or sum an exact trailing timestamp history.

## Selected contract

Use `internal/budget` with plain values:

```go
type Limits struct { FiveHours, SevenDays *int64 }
type Window struct {
    OpenedAt time.Time
    Used     int64
}
type State struct { FiveHours, SevenDays Window }

func Check(state State, limits Limits, now time.Time) error
func Admit(previous State, now time.Time) (State, error)
func Charge(previous State, increment int64, now time.Time) (State, error)
```

- `Check` validates and checks eligibility without opening or changing windows.
  Expired usage does not count toward eligibility. Exhaustion has a recognizable
  error; invalid input has a descriptive error without sensitive data.
- `Admit` records an already accepted upstream attempt, including retries and
  fallback; it does not authorize the attempt. It opens only unopened or expired
  windows at `now`.
- `Charge` receives only the new increment from `usage.Advance`. It opens expired
  windows and charges both in full, including overruns. Zero is a no-op after
  validation. Failure returns the entire previous state unchanged.

Keep both windows independent of cap settings. Absent caps
disable enforcement while accounting continues. Changing or restoring a cap uses
existing consumption and never resets it. An absent cap is `nil`, zero denies
new attempts, positive allows while consumption is below it, and negatives are
invalid. These meanings are specific to token budgets.

Use fixed elapsed durations of 5 and 168 hours. Equality with expiry starts the
next window only on a qualifying event. Store the opening and derive the end.
Zero state is unopened; an unopened window cannot contain usage. Reject negative
counts, overflow, zero accounting time and time before a saved opening.

The caller supplies chronological accounting times. Without a last-event field,
this module cannot detect every backwards timestamp inside an active window.
Use standard time comparisons; SQL timestamp encoding belongs to storage.
[Go time semantics](https://pkg.go.dev/time#hdr-Monotonic_Clocks).

If a window expires at 15:00 and a retry is admitted at 15:02, it opens the next
window at 15:02 even before new usage arrives. A rejected attempt opens nothing.

## Implementation and test brief

One executor owns this package and its external `budget_test` tests; the primary
agent owns ADR updates. Use the [development guide](../development.md).

Test first opening, independent expiry, exact expiry, long idle gaps, late charges
and overruns. Feed real `usage.Advance` updates of 100, 150 and 150 across expiry:
only 50 enters the next short window; the duplicate opens nothing. Test eligibility
without mutation, cap changes, invalid state and overflow in the second window
without a partial update to the first. Use explicit times and independent expected
values; no sleeps or concurrency tests for pure functions.

No registry, SQL interfaces, reservations, timers, clock abstraction or background
worker. The later transaction owns serialization and durable deduplication.
Run `just format --check`, `just deps`, `just lint` and `just test`.
