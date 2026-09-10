# ADR 0003: Separate request execution from protocol adapters

Status: Accepted. Recorded: 2026-09-08.
Decision owner: project maintainer. Implementation: not started.

## Decision

Separate the request path into three responsibilities:

| Module | Responsibility |
|---|---|
| Inbound adapter | Parse and validate the client HTTP request, convert it to common CLAN types, encode the response or stream in the client's protocol |
| Request execution | Check access and which accounts can handle the request, select an account, manage attempts and fallback, record results |
| Provider adapter | Convert an attempt to the provider protocol, make the authenticated call, return a common response, stream events or a classified error |

Request execution owns explicit retries and account switching. Provider adapters report
outcomes; they do not independently select another account or start a new generation
attempt. Retry eligibility is defined in
[ADR 0012](0012-retry-classified-transient-failures.md). OAuth preparation and
renewal timing are defined in [ADR 0005](0005-encapsulate-credential-types-in-account.md).

## Data flow

```text
Client
  -> Inbound adapter
  -> Common request + client identity/access information
  -> Request execution
  -> Attempt request + selected account and model
  -> Provider adapter
  -> External service

Response / stream events / error
  <- Inbound adapter encodes the client's protocol
  <- Request execution observes the attempt outcome
  <- Provider adapter converts the upstream result
```

Keep the original request separate from the account and model chosen for each attempt.
Preparing a fallback must not change the original request. Deliver stream events as
they arrive, without waiting for the complete response.

## Ordinary and streaming execution

Expose separate provider-execution methods for complete responses and streaming,
using shared typed request data.
The ordinary method returns a complete response or an error. The streaming method
returns a stream of events or a startup error. Reading the stream can also fail,
and the caller must receive that error. Opening a stream does not
mean generation has completed successfully.

The alternative was one event stream for both modes, with an aggregator producing
ordinary responses. Separate methods were selected to make the two result contracts
explicit. An adapter may still consume an upstream stream internally when needed to
produce a complete response.

This separation exists in both researched projects: CLIProxyAPI exposes `Execute` and
`ExecuteStream`; Bifrost exposes operation-specific pairs such as `ChatCompletion` and
`ChatCompletionStream`. See the [CLIProxyAPI](../research/cliproxyapi.md) and
[Bifrost](../research/bifrost.md) contract tables for sources.

## Stream consumption: Next and Close

Return a typed stream with `Next` and `Close` methods. `Next` reads the next event
or reports the end of the stream or a read error. `Close` releases its resources.
The context passed when opening
the stream controls its full lifetime; individual `Next` calls do not introduce
separate contexts.

Contract requirements:

- One consumer reads a stream sequentially. The caller receiving it owns cleanup and
  must close it when finished, including on early return or downstream write failure.
- Cancellation must interrupt blocked upstream I/O, not just be checked before a read.
- `Close` is safe to repeat and cleanup must not require draining all remaining events.
- Normal protocol completion must be distinguished from unexpected connection loss.
- Events retain their order and are delivered incrementally. Converting one upstream
  event into several CLAN events may require a small internal buffer.

The interface uses the common `Event` type described below. Its exact Go definition
is still open:

```go
type Stream interface {
    Next() (Event, error)
    Close() error
}
```

| Next result | Meaning |
|---|---|
| Event, nil | One event is available |
| Zero Event, io.EOF | Normal protocol completion; no events remain |
| Zero Event, another error | Terminal failure, including cancellation |

`Next` never returns a meaningful event together with a non-nil error. No further
events follow normal completion or failure. A stream can end normally because a
generation limit was reached; this does not mean the model finished its answer.

The adapter follows the upstream protocol to detect completion. Keep usage and final
metadata that arrive after the text ends. A connection that closes before protocol
completion must return an error, not `io.EOF`. An early `Close` stops reading and
releases resources; it does not record the generation as successfully completed.

We also considered returning events through a channel. `Next`/`Close` makes cleanup
the caller's responsibility and allows sequential reads without a producer goroutine.
An adapter can still use goroutines internally if its provider needs them.
This follows the reasoning in Go's guidance on
[synchronous functions](https://go.dev/wiki/CodeReviewComments#synchronous-functions);
it is not a claim that channel-based interfaces are unidiomatic or slower.

## Common event contents

Use **response → items → parts** as the common event model. Check it with adapter
test data before finalizing Go types. Items represent assistant
messages, reasoning or tool calls. Parts distinguish content within an item, including
text, refusal, reasoning text and reasoning summaries. Tool argument deltas address
the call item directly; a tool call need not have a content part.

Each event has a kind, a target where needed, and data for that kind:

| Event | Contents |
|---|---|
| `ResponseStarted` | Response identity and model |
| `ItemStarted` | Stable item address, item type, role or tool-call identity/name as applicable |
| `PartStarted` | Parent item, part address and content type |
| `Delta` | Target and a typed incremental change: text or a tool-argument string fragment |
| `PartEnded` | Completion of a particular part |
| `ItemEnded` | Completion of an item and final metadata, including reasoning data that CLAN preserves without interpreting |
| `UsageUpdated` | Snapshot of currently known token counts |
| `ResponseEnded` | Generation status and completion reason |

An address distinguishes items and their parts without relying on which item was
most recently opened. If multiple choices are supported, it must also distinguish
the choice; this decision does not itself add multiple-choice support. Tool-call IDs
are preserved separately from local addresses for subsequent tool results.

### Conversion rules

- An item/part starts before its deltas and ends after them. Different tool calls may
  have interleaved deltas; each retains its own address and argument sequence.
- Adapters create start/end events where the upstream protocol has no explicit
  equivalent. Late tool identity/name fields may require buffering before emitting
  `ItemStarted`; do not invent an ID or name and silently change it later.
- Argument fragments are strings, not independently valid JSON objects. A generation
  cut short by a token limit may leave incomplete arguments; preserve the incomplete
  outcome instead of reporting a successful call.
- A final upstream snapshot does not append previously emitted text or arguments
  again. Data provided only in the final snapshot must still be represented.
- Keep reasoning text, summaries and opaque data (data CLAN does not interpret)
  separate. Preserve the type and source of opaque data; one provider's signature is not automatically
  valid for another provider. `ItemEnded` may deliver data received after text ended.
  An inbound adapter may therefore need to delay closing its reasoning block.
- Usage updates carry normalized cumulative snapshots, not provider patches.
  Adapters merge partial fields; accounting charges only new observed consumption.
  See the [usage contract](0008-enforce-token-budgets-at-admission.md#cumulative-usage-contract).
  Unknown counters remain distinguishable from zero; provider adapters normalize
  token counts to CLAN's accounting rules, including cache-related counters.
- On normal protocol completion, close remaining items/parts and deliver all known
  usage before `ResponseEnded`, then return `io.EOF`. `ResponseEnded` can describe
  a generation limit; it does not imply that every tool argument is complete.
- Failure returns an error from `Next`, without adding `ResponseEnded` or `io.EOF`.
  Adapters handle connection keepalives separately from content and usage events.

For example, a tool-call response can produce:

```text
ResponseStarted
  ItemStarted(tool_call, item=0, call_id=call_1, name=weather)
    Delta(item=0, arguments='{"city":')
    Delta(item=0, arguments='"Moscow"}')
  ItemEnded(item=0)
  UsageUpdated(...)
ResponseEnded(reason=tool_calls)
io.EOF
```

The source comparison supports this design; compatibility still needs testing.
See the event findings for [Bifrost](../research/bifrost.md#содержимое-событий-стрима)
and [CLIProxyAPI](../research/cliproxyapi.md#содержимое-событий-стрима).
These event types do not promise support for every upstream event or operation.

## Context and alternatives

The supported client APIs need the same access, routing and retry behavior. Keeping these
rules in individual HTTP handlers would duplicate policy. Letting each provider
adapter own retries would make the total attempt limit harder to enforce.

This separates protocol conversion from request execution rules.
It builds on [ADR 0002](0002-use-common-request-format.md), without copying an
upstream project's full interface or plugin system.

## Consequences

Interfaces should expose supported operations explicitly and stay small. Their
contracts must define errors, stream ordering and termination, cancellation, resource
cleanup and the point after which retries are forbidden. Detect unsupported behavior
before sending the upstream request where possible.

These are responsibility boundaries within the
[single gateway process](0007-support-sqlite-and-postgresql.md#deployment-scope),
not separate services or a required package hierarchy. Core execution uses common
types; HTTP frameworks and provider-specific parsing stay in adapters.

## Validation and open details

| Area | Required checks |
|---|---|
| Execution | Success, restricted access, retryable failures, interrupted streams; restrictions survive fallback; adapters do not hide extra attempts |
| Stream lifetime | Early/repeated close, cancellation during blocked I/O, truncation, trailing metadata, exclusive event/error results, EOF only on normal completion, no events after termination |
| Event conversion | Text; interleaved calls with fragmented arguments and late identity; reasoning with late signatures or no visible text; trailing usage; repeated snapshots; generation limits and abrupt truncation |
| Client encoding | Protocol ordering and preservation of IDs, content, completion reasons and usage for every supported client protocol |

Define concrete Go types, execution signatures, completion-reason mapping,
feature-specific data, error categories and package layout with the adapters and
execution module. Account lifecycle and storage details belong to their own modules.
