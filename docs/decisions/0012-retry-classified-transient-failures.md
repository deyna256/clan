# ADR 0012: Retry classified transient failures without replaying unknown outcomes

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: policy functions in `internal/retry`;
provider classification and request-execution integration pending.

## Decision

Allow another upstream attempt before the client response begins if the failure
is classified as temporary and retryable. Do not automatically resend a request
whose outcome is unknown. Apply this to both ordinary responses and streams.

| Outcome | Policy |
|---|---|
| Transient connection failure known to have occurred before sending the request | A new attempt is eligible |
| Explicit temporary upstream rate limit or overload | Retry or account fallback is eligible, respecting the scope of the restriction |
| Server error classified as retryable by the provider adapter | A new attempt is eligible |
| Invalid request, unsupported parameter or excessive context size | Do not retry |
| Authentication failure | Do not repeat with the same credentials; credential recovery and subsequent account handling are separate decisions |
| Timeout or disconnection after sending, with an unknown outcome | Do not automatically retry or switch accounts to replay the request |
| Client cancellation or expired overall execution deadline | Stop execution |

Even a retryable failure may not lead to another attempt: attempt limits, deadlines, account
availability, access restrictions and budgets must also allow it. Attempt and wait
limits are defined below; execution timeouts are defined in
[ADR 0013](0013-separate-ordinary-and-streaming-timeouts.md).

Once the client response has begun, do not silently restart generation. This includes
final response headers or start/end events already sent to a streaming client, even before
any generated text. Close failed upstream resources and report the failure using the
client's response or stream format.

Provider adapters classify outcomes; request execution owns retries and account
selection, as defined in [ADR 0003](0003-separate-request-execution-from-protocols.md).
An HTTP status alone is not enough: distinguish temporary rate limiting from
exhausted quota, and track whether a restriction covers an account, model or provider.

On permitted account fallback, select the next eligible account for the same concrete
model under the configured routing rules. Recheck access, availability and applicable
token budgets on every attempt. A retry keeps the existing concurrency slot, consumes
no additional client RPM unit and does not refund known usage from earlier attempts.

## Attempt and wait limits

Initially allow at most three upstream attempts per client request, including the
first attempt. This is one shared limit across
same-account retries and account fallback, not three attempts per account. Provider
adapters must not introduce independent generation-retry loops.

| Parameter | Initial value |
|---|---|
| Total attempts | 3: first attempt plus at most two retries |
| Jitter before attempt 2 | Random delay from 0 to 500 milliseconds |
| Jitter before attempt 3 | Random delay from 0 to 1 second |
| Total waiting between attempts | At most 5 seconds, including account-availability waits |

The wait allowance is shared across the request and is not reset when switching
accounts. It limits pauses, not time spent executing upstream calls. An overall
execution deadline, when reached, stops execution regardless of remaining attempts
or wait allowance. The request keeps its concurrency slot while waiting, and
cancellation must stop the wait.

## Retry-After

Accept valid Retry-After values as a nonnegative integer number of seconds or an
HTTP-date, following [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html#section-10.2.3).
Do not retry an affected account or scope before its indicated retry time. A new
attempt must satisfy both the ordinary jitter delay and the applicable retry time;
overlapping waits do not need to be added as separate pauses.

Another eligible account may be selected if it is outside the reported restriction's
scope; account switching does not bypass model-wide or provider-wide restrictions.
Its retry still follows the ordinary jitter policy.

If no eligible target can be retried within the remaining wait allowance and overall
deadline, stop retrying and return the failure. Do not shorten an upstream retry
time to fit the local allowance. A missing or malformed header supplies no additional
delay; use the ordinary retry policy. Retry-After does not make an otherwise
non-retryable error eligible.

## Policy interface

`internal/retry` separates Retry-After parsing from evaluation of one candidate.
Provider adapters report whether retry is allowed and the outcome is known.
Inbound adapters report whether the client response has started. Execution selects
the candidate and supplies the effective cooldown for restrictions that apply to it.
This module does not match account, model or provider scopes itself.

Evaluation reads caller-owned attempt count, actual waiting, failure time, current
time, optional deadline, jitter and failure facts. Unknown outcomes, cancellation
or a client response that has started prevent retries. The result is either no retry
or a remaining delay; invalid caller inputs return no retry and an error.

Draw jitter once for each next attempt and anchor it to failure observation. Reuse
the same draw when evaluating other accounts or checking again. Its remaining time
overlaps the applicable cooldown; cleanup can reduce both without consuming the
separate waiting allowance. Do not restart either delay during reevaluation.

At exactly five seconds already waited, only zero additional delay is allowed.
Waiting beyond the allowance or using three attempts means stop. A delay may equal
the remaining allowance, but its target must be strictly before an overall deadline.
A zero deadline means none. The failure and current times are required, with the
failure no later than the current time. Reject nonpositive attempt counts, negative
durations, invalid jitter and contradictory cooldown fields.

Cooldown has three explicit kinds: `NoCooldown`, `RetryAt` and `RetryBlocked`.
Its zero value means no restriction. `RetryAt` uses `Until` as an absolute time,
including the zero date; other kinds require a zero `Until`. Unknown kinds are
invalid. The date itself does not indicate whether a cooldown exists.

Retry-After parsing removes only surrounding HTTP spaces and tabs. Missing or
malformed input yields no cooldown. Numeric values use unsigned ASCII decimal
syntax; validate all digits before handling overflow. Keep ordinary deadlines as
absolute times. A valid number too large for `time.Duration` produces `RetryBlocked`,
marking the affected target unavailable for the current request without an expiring
substitute deadline. Invalid receipt time is a caller error.

Execution owns waiting, cleanup, cancellation/deadline rechecks and access/budget
checks before dispatch. Provider classification and scope matching require tests
there; tests of this policy alone do not prove them. See the
[policy tests](../../internal/retry/retry_test.go).

Absolute retry times preserve delays while cleanup runs: a six-second Retry-After
followed by eight seconds of cleanup needs no further wait. A hundred-second value
after ten seconds of cleanup still exceeds our wait allowance. Clamping either
header to five seconds would lose this distinction.

## Rationale and alternative

If no response arrives, the upstream may still have processed the request. Resending
it could generate another answer and consume more tokens without CLAN knowing.
Retrying these timeouts and disconnects could improve availability, but we have
chosen not to accept that risk.
See [HTTP retry semantics](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.2).

Provider-specific classification remains necessary. For example, Anthropic documents
both rate limiting and spend caps under 429, retry guidance for 500 and overload under
529. See [Claude API errors](https://platform.claude.com/docs/en/api/errors).
A retryable error does not mean the upstream consumed no tokens.

## Validation and open details

Future tests must distinguish pre-send failures from uncertain post-send failures,
temporary limits from exhausted quotas, and provider-classified errors from unknown
statuses. Cover no replay after response commitment or lifecycle events, cancellation,
overall deadline expiry, cleanup before another attempt, eligible account selection,
per-attempt budget checks, one RPM admission and one concurrency slot across retries,
and preservation of all known usage.

Future scheduling tests must verify three attempts total across account switches,
the two jitter ranges, the shared five-second wait limit, Retry-After seconds and
dates, missing/malformed headers, no early retry of an affected scope, and fallback
to an unaffected account. Cover cancellation during waiting, an earlier overall
deadline and upstream execution time not consuming the separate pause allowance.

Timeout policies are defined in [ADR 0013](0013-separate-ordinary-and-streaming-timeouts.md).
Define cooldown state beyond the current request, credential recovery and configuration
options with their modules. Select numeric timeout defaults during implementation.
