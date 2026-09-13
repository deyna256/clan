# ADR 0003: Separate request execution from protocols

Status: Accepted. Reviewed: 2026-09-13.

## Context

Account selection, concurrency, cancellation and retry rules must have one owner.
Putting them in HTTP handlers or individual integrations would duplicate policy.

## Decision

HTTP handles the client protocol. Request execution owns the request lifecycle.
The Codex integration handles one upstream generation attempt. See the
[module responsibilities](../architecture.md#responsibilities).

### Requests and results

Use separate ordinary and streaming methods with the same validated Responses
input. Pass the selected account separately from request content.

An ordinary result carries Responses JSON and observed usage. A stream carries
Responses events and the small metadata execution needs. Preserve known usage
on success, incomplete generation and failure; unknown usage is not zero.

### Failure details

An attempt failure exposes a category, the upstream HTTP status when received,
whether a repeat is known to be safe, and retry timing when known. Categories
distinguish invalid requests, account problems, provider limits, transport failures
and invalid provider responses. A 5xx status alone does not prove a safe repeat.

Keep observed usage on the attempt result or stream, not a second copy on the
error. Preserve cancellation so `errors.Is(err, context.Canceled)` works. Error
messages and logs use safe descriptions, not raw provider error bodies.

The integration reports these facts. Execution decides retries and temporary
account exclusion; the integration does neither internally. Reuse the existing
`internal/retry` parser for `Retry-After`.

### Accounts, slots and retries

- Select eligible Codex accounts with round-robin for the requested model.
  Keep positions in memory and temporarily exclude accounts with exhausted
  provider limits. Return a clear error when none are eligible.
- Check the client key and enforce its concurrency limit. Reject requests without
  queuing when all slots are occupied.
- Keep one slot across attempts. Close attempt resources before retrying, and
  release the slot only after request cleanup.
- Retry only after a failure known to permit a safe repeat and before the client
  response starts. Do not replay a request whose upstream outcome is unknown.
- Revoking a key blocks new requests and cancels its active requests.
  Client cancellation also stops upstream I/O and leads to cleanup.

Retry limits, delays, timeout settings and account cooldown details belong to
the execution and integration implementation. This decision does not select
their numeric values or configuration options.

### Stream consumption: Next and Close

Use `Next` to read Responses events sequentially and `Close` to release resources.
Do not introduce a separate item/part/delta event hierarchy.

- The caller owns cleanup, including early return and downstream write failure.
- Cancellation interrupts blocked I/O. `Close` is safe to repeat and does not
  require draining the stream.
- Distinguish protocol completion from transport EOF. A connection closing early
  is a failure; reaching a generation limit is an incomplete outcome.
- Preserve event order and known usage, including data received at the end.
- A read returns an event or an end/error. Known usage remains available when
  failure ends the stream. Exact Go result types will follow these requirements.

### Final response assembly

Keep complete items from `response.output_item.done` in output order. Use the
terminal response's `output` when present and non-empty; otherwise fill it from
those items. Do not build a second accumulator for text and argument deltas.

Both methods use this final response: ordinary calls return its JSON, and
streaming calls include it in the terminal event. Preserve the terminal status
and observed usage. Collected items alone never turn an interrupted stream into
a successful response.

This follows [CLIProxyAPI's completed-item approach](https://github.com/router-for-me/CLIProxyAPI/blob/ac02da6c05e18f465aa7e3ed5b0a65a2f060917d/internal/runtime/executor/codex_executor_execute.go).

## Consequences

Protocol changes stay in HTTP and integration code. Execution can use existing
selection and concurrency primitives.
Tests must verify retries before response start, no replay of unknown outcomes,
key revocation, stream interruption and cleanup before slot release.
