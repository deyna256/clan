# ADR 0010: Limit concurrent client requests without an admission queue

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: not started.

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

## Validation and open details

Future tests must cover simultaneous admissions at the limit, isolation between
keys, immediate 429 without upstream dispatch, retries retaining one slot, streams
holding slots through cleanup, and release on all terminal and admission-error paths
without leaks or double release.

RPM accounting is defined in [ADR 0011](0011-use-sliding-window-rpm.md), and timeout
policies in [ADR 0013](0013-separate-ordinary-and-streaming-timeouts.md). Concrete
concurrency values and what happens when a key's limit changes during active requests remain
details for the admission and configuration modules.
