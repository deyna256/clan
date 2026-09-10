# ADR 0013: Separate ordinary-response and streaming timeout policies

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: not started.

## Decision

Use separate timeout policies for ordinary and streaming client responses within
the shared request execution flow. Select numeric defaults during implementation.

| Limit | Ordinary client response | Streaming client response |
|---|---|---|
| Overall execution duration | Finite, configurable default | Disabled by default; an administrator may enable it |
| Waiting for an ordinary upstream response | Within the remaining overall deadline, without an additional short generation timeout | If used by an adapter, requires an explicit wait policy rather than treating the response as an active stream |
| Upstream stream startup | Applies when the adapter consumes an upstream stream | Separate limit until upstream body data begins |
| Upstream stream inactivity | Applies when the adapter consumes an upstream stream | Sliding limit renewed by incoming upstream data |
| Connection establishment | Separate bounded wait | Separate bounded wait |
| Stalled downstream writes | Bound blocked response writes | Bound blocked response writes without capping the healthy stream's lifetime |

Choose the overall timeout by the client's response mode. When enabled, it starts
when the request passes admission checks and covers OAuth preparation, all attempts,
retry waits and response delivery. Retries and activity do not extend it.

Choose network timeouts based on how the upstream actually responds. An adapter may
read an upstream stream to build an ordinary response for the client; in that case
both the ordinary overall deadline and upstream stream limits apply.

The upstream stream startup timer runs from when the request has been fully sent until
the first body data, including a heartbeat. Headers alone do not finish this wait.
Each later piece of upstream data resets the inactivity timer. This measures connection
activity, not time to the first generated token. It does not prove generation is progressing.
A heartbeat CLAN sends to its client does not renew upstream timers. Neither kind
of heartbeat extends an enabled overall deadline.

Client cancellation stops execution and starts cleanup. A timeout does not allow
resending a request whose outcome is unknown. Once CLAN starts a client response,
including a heartbeat, it cannot silently retry under
[ADR 0012](0012-retry-classified-transient-failures.md). Keep the concurrency slot
through cleanup as defined in [ADR 0010](0010-limit-concurrent-client-requests.md).

## Rationale and alternatives

A shared overall timeout can interrupt a healthy long stream. Removing every timeout
would let silent or stalled requests wait forever. Separate overall, startup,
inactivity and write timeouts handle these cases within the same execution flow.

An ordinary upstream response may stay silent throughout generation. A short header
timeout could stop it before its overall deadline. If the upstream sends nothing,
no timeout can reliably tell whether it is still thinking or has stalled.

Without an overall timeout, an active stream can hold its concurrency slot for a
long time. Client and reverse-proxy limits still apply.

## Validation and open details

Future tests must cover a healthy stream surviving beyond an ordinary request's
deadline, ordinary responses timing out, configured streaming deadlines spanning
retries, startup with headers but no body, upstream heartbeats renewing inactivity
without extending the overall deadline, downstream pings not masking upstream
silence, stalled client writes, cancellation and cleanup, and an ordinary client
response assembled from upstream streaming with both applicable limits enforced.

Define configuration names, override scope, bounded OAuth and request-upload waits,
and the wait policy for a streaming client backed by an ordinary upstream response
with the execution module. Heartbeat emission is not enabled by this decision.
