# ADR 0015: Limit request rates with a token bucket

Status: Accepted. Recorded: 2026-09-10.
Decision owner: project maintainer. Implementation: limiter in `internal/ratelimit`;
admission integration pending.
Supersedes [ADR 0011](0011-use-sliding-window-rpm.md).

## Decision

Use a token bucket per access key, implemented with
[`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate).
This is an additional Go module, not part of the standard library.

Use `Limiter.Allow` directly and accept the library's floating-point and time
rounding. CLAN does not add a separate balance check or guarantee nanosecond-exact
admission. At extreme configured rates, rounding can also affect burst enforcement.

For a positive limit, N RPM replenishes N / 60 request permits per second.
Bucket capacity (`burst`) bounds the accumulated permits.
Each permit allows one client request; it is unrelated to LLM token consumption.

| RPM | Meaning |
|---|---|
| Positive | Apply the configured refill rate and burst capacity |
| `0` | Reject new requests, even if permits remain |
| `Unlimited` (`-1`) | Disable request-rate limiting |
| Below `-1` | Invalid configuration |

These values follow the concurrency limit convention in
[ADR 0010](0010-limit-concurrent-client-requests.md#slot-contract).
Unlimited RPM does not bypass access checks, concurrency limits or token budgets.

Expose burst as a separate, optional access-key setting. For a positive RPM limit,
an omitted burst defaults to RPM. An explicit burst sets the capacity independently
of the refill rate: 60 RPM with burst 10 allows up to 10 accumulated permits and
replenishes one permit per second.

An explicit burst must be a positive integer; zero and negative values are invalid,
including when RPM is zero or Unlimited. Burst may be lower or higher than RPM.
With zero or Unlimited RPM, retain the configured burst but do not apply it.
An omitted burst defaults to RPM only when RPM is positive.

This controls sustained request rates and allows bursts. It does not guarantee
at most N requests in every trailing 60-second window. For example, a full bucket
with 60 RPM and burst 60 can admit 60 requests immediately and another 30 after
30 seconds.

Create each bucket with a full balance of burst permits, including after a process
restart. A restart restores the request-rate allowance; it does not reset saved
LLM token consumption. Positive-limit updates and idle cleanup must not recreate
a depleted bucket to grant extra requests.

- Consume one permit when a client request passes admission, regardless of its
  eventual outcome. Failure or cancellation after admission does not refund it.
- Retries and account fallback consume no additional request permits.
- Requests rejected by access, concurrency or token-budget checks consume no permit.
- When no permit is available, reject immediately with HTTP 429. Do not queue or
  dispatch upstream work. Use the library's immediate admission operations rather
  than waiting for capacity.

Coordinate rate admission with the other checks. A concurrency-safe bucket alone
does not make the combined admission process atomic. Request execution owns that
coordination; see [ADR 0010](0010-limit-concurrent-client-requests.md) and
[ADR 0008](0008-enforce-token-budgets-at-admission.md).

Keep bucket state in the single gateway process. Do not persist it or introduce
Redis.

## Changes to positive limits

When changing from one positive RPM to another, refill up to the change time at
the old rate, then apply the new rate. Keep the available balance, capped at the
new burst capacity. Increasing burst does not immediately add permits; decreasing
it discards any balance above the new capacity. Do not recreate a full bucket.

An omitted burst follows the new RPM. An explicitly configured burst stays unchanged
unless it is also updated. Already admitted requests continue running.

For example, a balance of 3 remains 3 when burst increases from 10 to 20. Reducing
burst to 2 leaves a balance of 2. These rules also apply when only burst changes
and RPM remains positive.

## Configuration and lifetime

Setting RPM to zero or Unlimited discards the previous bucket. Returning to a
positive RPM creates a full bucket. This is an explicit administrative reset;
CLAN does not preserve the old balance across it. The configuration owner retains
any explicit burst setting while rate limiting is disabled.

Use a concrete limiter with separate `Configure`, `TryAcquire` and `Remove`
operations. `Configure` replaces a key's full configuration, rather than applying
a management API patch. The configuration owner supplies any retained settings.
Acquisitions use the last applied configuration; they cannot overwrite it with a
request's older snapshot. Configuration changes and acquisitions share one lock.
After a successful update returns, new acquisitions use the updated configuration.

Validate nonblank access-key IDs when configuring them; preserve IDs exactly.
Acquisitions for any unconfigured ID return `ErrNotConfigured`. Reject invalid
settings without changing state. Positive RPM and explicit burst must fit both Go's `int`
and the exact integer range through 2^53 used by the library's floating-point
balance. Sample time while holding the lock; use Go's monotonic clock for elapsed
time and `testing/synctest` for tests, without a public clock interface.

Keep state until its owner removes the access key. Removal is idempotent, ignores
unknown IDs and returns no error. Reconfiguring a removed key creates fresh state.
Do not evict idle keys: that would reset their allowance on reconfiguration.
No periodic cleanup or background worker is needed.

## Rationale and alternatives

The previous exact sliding window required retaining admission timestamps.
A token bucket keeps constant-size state per key, and the Go library supplies
refill calculations and synchronization. CLAN still owns key lookup, configuration
changes, inactive-state cleanup and coordination with other admission checks.

Exact sliding windows remain appropriate when a strict trailing-window quota is
required. Fixed windows use little state but permit double bursts around reset
boundaries. CLAN now chooses sustained rate control with an explicit burst capacity.
[Envoy also uses token buckets for local rate limiting](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/local_rate_limit_filter).

## Validation

Tests must cover burst exhaustion, refill before and at the next
available permit, the capacity ceiling after idle time, simultaneous admissions,
key isolation, omitted burst defaulting to RPM, explicit burst overrides, full
initial and restart balances, and configuration changes. Use controlled time
without real waits and assert allowed request counts, not only absence of races.
Keep boundary checks outside subnanosecond rounding effects.
Also test zero RPM rejection with permits remaining, Unlimited RPM, burst below
and above RPM, and burst validation when RPM is zero or Unlimited. Invalid RPM
or burst values must leave existing state unchanged.
For positive-limit updates, test old-rate refill before the change, new-rate refill
after it, preserved fractional balances, burst increases without extra permits,
burst decreases discarding excess, and omitted versus explicit burst settings.

Integration tests must cover other admission failures consuming no permit, retries
counted once, and no refund after an admitted request fails or is cancelled.

Also test reset through zero and Unlimited, configuration ordering, invalid
updates preserving the old state, removal and reconfiguration. HTTP handling and
coordination with other admission checks belong to request execution.
