# Retry eligibility and delay policy

Research for [issue #10](https://github.com/deyna256/clan/issues/10), 2026-09-10.
Baseline: `main` at `2ccc394`. The contract was accepted on 2026-09-10.
[ADR 0012](../decisions/0012-retry-classified-transient-failures.md) defines the
existing retry rules; [ADR 0013](../decisions/0013-separate-ordinary-and-streaming-timeouts.md)
defines execution deadlines.

## Evidence

Retry safety depends on what happened to the request, not just the HTTP status.
Retry-After is either decimal nonnegative seconds from response receipt or an
HTTP-date. Missing or malformed values do not create a delay under CLAN's ADR.
[HTTP retry rules](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.2),
[Retry-After](https://www.rfc-editor.org/rfc/rfc9110.html#section-10.2.3).

Go provides HTTP-date parsing, integer overflow reporting and bounded random
draws. No retry library or scheduler is needed for a pure policy.
[http.ParseTime](https://pkg.go.dev/net/http#ParseTime),
[strconv.ParseUint](https://pkg.go.dev/strconv#ParseUint),
[rand.Int64N](https://pkg.go.dev/math/rand/v2#Int64N).

CLIProxyAPI separates retry rounds and credential/model cooldowns. Its tests
allow different retry counts per credential. CLAN instead shares three total
attempts across the request; copying that retry loop would change our policy.
[Pinned retry tests](https://github.com/router-for-me/CLIProxyAPI/blob/09a29bd345bc44c473abe7fd07859e32df2ea543/sdk/cliproxy/auth/conductor_retry_round_test.go#L102),
[cooldown handling](https://github.com/router-for-me/CLIProxyAPI/blob/09a29bd345bc44c473abe7fd07859e32df2ea543/sdk/cliproxy/auth/conductor_cooldown.go#L1569).

## Selected contract

Use `internal/retry` with two pure functions:

```go
type Cooldown struct {
    Until           time.Time
    Unrepresentable bool
}

type Input struct {
    Attempts           int
    Waited             time.Duration
    Jitter             time.Duration
    FailureAt          time.Time
    Now                time.Time
    Deadline           time.Time
    Retryable          bool
    OutcomeUnknown     bool
    Committed          bool
    Cancelled          bool
    ApplicableCooldown Cooldown
}

type Decision struct {
    Retry bool
    Delay time.Duration
}

func ParseRetryAfter(value string, receivedAt time.Time) (Cooldown, error)
func Evaluate(input Input) (Decision, error)
```

`Evaluate` considers one candidate. The executor selects it and supplies the
effective cooldown for applicable account, model or provider restrictions.
An unaffected account receives no cooldown. Scope matching is an executor
responsibility, not a guarantee of this value function.

Provider adapters supply failure facts. Unknown outcomes override retryability;
response commitment or cancellation prevents retry. This function does not
classify HTTP responses or recover credentials.

Draw jitter once for the next attempt, anchored to failure observation, and reuse
it across candidates and later evaluations. Expose the agreed 500 ms and 1 s
maxima; the executor draws a duration and the policy validates it. Delay is the
maximum remaining jitter and cooldown, or zero when both have elapsed.

Actual waiting shares the five-second allowance. Cleanup time can reduce a pending
delay without consuming that allowance. At exactly five seconds already waited,
only a zero-delay retry can qualify. A delay may equal the remaining allowance,
but its target must be strictly before any overall deadline. Zero deadline means
none. Three attempts used or waiting beyond the allowance means stop.

Reject invalid caller inputs: nonpositive attempt count, negative durations,
out-of-range jitter, zero/inverted observation times, or a cooldown with both
`Until` and `Unrepresentable`. The zero cooldown means no restriction. Invalid
input returns no retry and an error. `FailureAt` and `Now` are required, with
`FailureAt <= Now`. Execution rechecks cancellation, deadline,
access and budgets after waiting and owns upstream cleanup.

## Retry-After boundaries

Trim HTTP optional whitespace (space and tab), then accept only unsigned ASCII decimal digits
or an HTTP-date. Parse all three HTTP date layouts. Past dates add no remaining
delay. Malformed text returns the zero cooldown without an error; an invalid
caller-supplied receipt time is an error.

Represent ordinary delays as absolute times. Validate the complete numeric syntax
before handling overflow. A valid number too large to represent as a duration
sets `Unrepresentable`, blocking the affected target for this request. The same
applies to a future deadline equal to the zero-time sentinel used for absence.

Do not shorten a delay to five seconds: a 100-second header followed by ten seconds
of cleanup must still block retry. A six-second header followed by eight seconds
of cleanup can legitimately need no wait. The overflow marker never expires during
reevaluation; it does not claim to retain an exact provider deadline.

## Review and implementation brief

Test eligibility vetoes, attempt counts, jitter bounds, overlapping delays,
reevaluation after cleanup, wait/deadline equality, malformed/overflowing headers,
HTTP date layouts and unchanged input. Use explicit times and draws; no sleeps
or statistical assertions. Provider classification and scope-matching tests belong
to their future callers; naming identical boolean inputs after different HTTP
errors does not test those behaviors.

No account registry, retry loop, HTTP calls, clock interface or OAuth recovery.
Follow the [development guide](../development.md) and run all four `just` checks.
