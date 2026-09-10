# Fixed token-budget windows

Research for [issue #9](https://github.com/deyna256/clan/issues/9), 2026-09-10.
Reviewed against `main` at `db0189c`.
The accepted contract is recorded in
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

## Why plain state transitions are enough

[ADR 0008](../decisions/0008-enforce-token-budgets-at-admission.md#budget-window-transitions)
defines `Check`, `Admit` and `Charge`. The [implementation](../../internal/budget/budget.go)
keeps opening times and usage; it derives window ends when called. No reset timer
or background worker is needed.

The caller supplies accounting times in order. Without a last-event field, the
module cannot detect every backwards timestamp within an active window. SQL time
encoding and synchronization belong to storage.
[Go time semantics](https://pkg.go.dev/time#hdr-Monotonic_Clocks).

If a window expires at 15:00 and a retry is admitted at 15:02, the new window opens
at 15:02 even before usage arrives. A rejected attempt opens nothing.

## Validation

[Tests](../../internal/budget/budget_test.go) cover independent window expiry,
late usage, overruns, invalid state and overflow without partial updates.
Cumulative usage of 100, 150 and 150 across expiry charges only 50 to the new window;
the duplicate neither charges nor opens a window.

These pure functions need no concurrent tests. The future accounting transaction
must prove that concurrent writes and retries cannot lose or duplicate charges.
