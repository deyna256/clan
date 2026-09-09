# ADR 0011: Count admitted requests in an exact sliding RPM window

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: not started.

## Decision

For an access key with an RPM limit of N, allow at most N client requests in any
60-second window. Record when each request passes admission checks (the checks that
allow it to proceed).
At time t, timestamps at or before t minus 60 seconds have expired. Check the
remaining admissions and record a new admission atomically within the single process.

- Count a client request once when it passes admission, regardless of its final
  outcome. Failure or cancellation after admission does not refund its RPM unit.
- Internal retries and account fallback do not consume additional RPM units.
- Requests rejected before admission do not consume RPM, including rejection by
  access checks, concurrency limits or token budgets.
- If the RPM allowance is exhausted, immediately return HTTP 429 without queuing
  or dispatching an upstream attempt.

Coordinate RPM admission with the other admission checks so concurrent callers
cannot exceed the limit or charge rejected requests. Concurrency slots remain
governed by [ADR 0010](0010-limit-concurrent-client-requests.md).

Store admission timestamps in process memory. Restart starts an empty RPM history;
the exact window guarantee applies while that process is running. Saved
token-budget consumption is not reset. No Redis or persistent RPM
state is required for the initial single-instance deployment.

## Rationale and alternatives

The sliding window avoids admitting N requests just before a calendar minute ends
and another N just after it. It can still admit all N together if other limits allow;
it does not space them evenly. Memory use grows with the number of admissions kept
in the window.

A fixed window uses a counter but allows bursts across its boundary. A token bucket
refills an allowance over time and permits configurable bursts; it does not guarantee
at most N admissions in every trailing 60 seconds. Neither is selected initially.
See the [algorithm comparison](https://redis.io/tutorials/howtos/ratelimiting/) and
the Go [token-bucket implementation](https://pkg.go.dev/golang.org/x/time/rate).

## Validation and open details

Future tests must cover the Nth admission and N+1 rejection, expiration immediately
before and exactly at 60 seconds, simultaneous admissions, isolation between keys,
rejections by other checks consuming no RPM, retries counted once, and no refund
after an admitted request fails or is cancelled. Restart must clear RPM history
without clearing saved token budgets.

Concrete limit values, changing limits during operation and the admission-check
implementation will be defined with the admission and configuration modules.
