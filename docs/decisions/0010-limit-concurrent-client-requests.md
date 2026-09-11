# ADR 0010: Limit concurrent client requests without an admission queue

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer.
Implementation: [concurrency slots](../../internal/concurrency/limiter.go) and
admission coordinator implemented; request execution integration is pending.

## Decision

For an access key with a concurrency limit, each client request that passes admission
checks holds one slot until it finishes. Admission checks decide whether the request
may proceed. Retries and account fallback keep the same slot without taking another.

Streaming requests retain their slot until processing ends and the upstream stream
is closed. Release the slot exactly once on completion, error or cancellation after
its resources have been cleaned up. If the client disconnects, cancel and clean up
the upstream work before releasing the slot.

Check and take a slot in one atomic operation inside the single gateway process from
[ADR 0007](0007-support-sqlite-and-postgresql.md). If all slots for the key are occupied,
reject the new client request immediately with HTTP 429. Do not queue it for a slot
or dispatch an upstream attempt. If a later admission check rejects a request after
slot acquisition, release the acquired slot as part of cleanup.

## Rationale and alternative

Counting client requests gives the key a stable concurrency limit across retries
and streaming. Immediate rejection avoids an admission queue, queue capacity settings
and queue-wait deadlines. Waiting for a free slot could absorb short bursts, but is
not selected for the initial implementation; the client decides whether to retry.

## Slot contract

Agreed on 2026-09-10: create one limiter for the gateway process with `concurrency.New`.
`TryAcquire(keyID, limit) (Slot, error)` atomically checks and takes one slot for
an `accesskey.ID`. It does not queue requests for a free slot; it may briefly wait
for the mutex. Blank key IDs are invalid; other IDs are preserved and compared
exactly. Separate limiter instances have independent counts.

| Limit | Meaning |
|---|---|
| Positive | Maximum active slots for the key |
| `0` | Reject new acquisitions |
| `Unlimited` (`-1`) | Admit without a configured cap, while still counting slots |
| Below `-1` | Invalid input |

Exhaustion returns `ErrLimitReached`. Invalid input returns a different error.
Both leave counts unchanged and return a zero slot. `Slot.Release` is safe on a
zero slot and releases a successful acquisition at most once, including concurrent
calls through copies of that slot. Release after upstream cleanup, or when a later
admission check rejects the request. Cancellation alone does not release a slot.

Each acquisition uses the supplied configuration snapshot. Raising a limit allows
more acquisitions; lowering it does not cancel active work. With five active slots
and a new limit of two, admission resumes when the count falls below two. Slots
acquired under `Unlimited` also count against a later finite limit.

The admission coordinator receives configuration before acquisition. An admission
already in progress may use an older snapshot after a configuration update.
This primitive does not guarantee that completion of an admin update prevents all
later acquisitions using old values, and it stores no copy of the configured limit.

Delete a key's counter when its last slot is released. Releasing an old slot again
must not affect a new counter for the same key. No background cleanup is needed.

Use a map and a short mutex-protected check and increment. A fixed-capacity channel
or `x/sync/semaphore` would need extra machinery for changing limits. Protect each
slot's release with `sync.OnceFunc`, following the ownership pattern used by Go's
[LimitListener](https://github.com/golang/net/blob/master/netutil/listen.go).

## Validation and integration

Unit tests cover capacity, limit changes, key isolation, and concurrent acquisition
and release through the public contract. Integration tests must cover immediate
429 without upstream dispatch, retries retaining one slot, streams holding slots
through cleanup, and release on terminal and admission-error paths.

Request-rate accounting is defined in [ADR 0015](0015-use-token-bucket-rate-limits.md).
Timeout policies are defined in [ADR 0013](0013-separate-ordinary-and-streaming-timeouts.md).
The admission coordinator retains slots across retries. HTTP status mapping,
configuration loading and release after stream cleanup remain execution integration work.
