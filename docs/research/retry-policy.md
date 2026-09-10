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

## Why CLAN keeps absolute retry times

HashiCorp returns a delay and a parsing-success flag. Its retry loop calculates
that delay after response cleanup. CLAN reevaluates candidates and lets cleanup
reduce the remaining delay, so it keeps an absolute retry time.
[HashiCorp implementation](https://github.com/hashicorp/go-retryablehttp/blob/main/client.go).

A 100-second header followed by ten seconds of cleanup must still block retry
within our five-second wait allowance. A six-second header followed by eight
seconds of cleanup needs no further wait. Clamping both headers to five seconds
would lose this distinction.

The current [policy contract](../decisions/0012-retry-classified-transient-failures.md#policy-interface)
uses explicit cooldown kinds. `RetryAt` carries a date; `NoCooldown` means no
restriction; `RetryBlocked` prevents retry for that candidate during the request.
A date equal to `time.Time{}` is therefore distinct from an absent cooldown.

Validate all numeric digits before checking overflow. A valid value too large
for `time.Duration` blocks the affected candidate for this request. It must not
become a short delay or disappear as if the header were malformed.

## Validation

[Policy tests](../../internal/retry/retry_test.go) cover delay boundaries,
reevaluation, parsing and the three cooldown states with fixed times and jitter.
Provider classification and scope matching need tests in their future callers;
policy tests alone cannot verify them.
