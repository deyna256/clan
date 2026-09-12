# ADR 0003: Separate request execution from protocol adapters

Status: Accepted. Recorded: 2026-09-08.
Decision owner: project maintainer. Implementation: the
[OpenAI provider adapter](../openai-adapter.md); inbound adapters and centralized
execution are not implemented.

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
returns a stream of events or a startup error. Later read errors must also reach
the caller. Opening a stream does not mean generation has succeeded.

The alternative was one event stream for both modes, with an aggregator producing
ordinary responses. Separate methods were selected to make the two result contracts
explicit. An adapter may still consume an upstream stream internally when needed to
produce a complete response.

This separation exists in both researched projects: CLIProxyAPI exposes `Execute` and
`ExecuteStream`; Bifrost exposes operation-specific pairs such as `ChatCompletion` and
`ChatCompletionStream`.

## Stream consumption: Next and Close

Return a typed stream with `Next` and `Close` methods. `Next` reads the next event
or reports the end of the stream or a read error. `Close` releases its resources.
The context passed when opening the stream controls its full lifetime;
individual `Next` calls do not take separate contexts.

Contract requirements:

- One consumer reads a stream sequentially. The caller receiving it owns cleanup and
  must close it when finished, including on early return or downstream write failure.
- Cancellation must interrupt blocked upstream I/O, not just be checked before a read.
- `Close` is safe to repeat and cleanup must not require draining all remaining events.
- Normal protocol completion must be distinguished from unexpected connection loss.
- Events retain their order and are delivered incrementally. Converting one upstream
  event into several CLAN events may require a small internal buffer.

The stream uses the common `Event` type described below. Consumers can declare the
interface they need:

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
| `CodeDelta` | Tool-call item address and a generated-code fragment |
| `ToolInputDelta` | Custom-tool item address and a freeform input fragment |
| `MCPProgress` | MCP item address and hosted-tool status; failure here does not end generation |
| `ShellCommandDelta` | Shell-call item and command index, with a command string fragment |
| `ShellOutputDelta` | Shell-result item and command index, with stdout/stderr fragments |
| `ShellOutputEnded` | A command's output chunks and outcomes as a snapshot |
| `PatchDiffDelta` | Patch-call item address and a diff suffix recovered from an item snapshot |
| `ImagePreview` | Image-call item address, preview index, complete base64 preview and image metadata; never a final result |
| `AnnotationAdded` | Part address, annotation index and typed citation |
| `LogprobsDelta` | Part address and newly observed token probabilities |
| `PartEnded` | Completion of a particular part |
| `ItemEnded` | Completion of an item and final metadata, including reasoning data that CLAN preserves without interpreting |
| `UsageUpdated` | Snapshot of currently known token counts |
| `ResponseEnded` | Generation status and completion reason |

An address distinguishes items and their parts without relying on which item was
most recently opened. If multiple choices are supported, it must also distinguish
the choice; this decision does not itself add multiple-choice support. Tool-call IDs
are preserved separately from local addresses for subsequent tool results.

Shell output events keep their command indices. The terminal item carries the
provider's flat output list, which has no command indices. Keep both representations;
do not infer one output chunk per command. Snapshots do not append bytes already
delivered by deltas.

Tool-search arguments and loaded catalogs are atomic snapshots. `ItemStarted`
carries identity; `ItemEnded` carries their final data. A completed client search
needs a call ID so the client can submit its result. Hosted search stays at the
provider; CLAN does not perform discovery or execute the loaded tools.

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

Compatibility still needs testing.
These event types do not promise support for every upstream event or operation.

### Recoverable protocol differences

Ignore additional response fields, known service events and exact repeats without
warnings. An unknown stream event is skipped with a warning, not an immediate
failure. Completion still requires a recognized terminal response and supported
final output. This favors availability but may omit a new optional extension.

For ordered SSE streams, use supplied sequence numbers to recognize a repeated
last event. Conflicting or regressing sequence numbers are protocol errors;
identical text in different events must still be delivered.

Use final snapshots to recover missing text or arguments when they extend the
published prefix. If a final text aggregate contradicts text already delivered,
keep the delivered text and warn instead of replacing it or failing the request.
Conflicting tool arguments or identity, unrecoverable required content and
unsupported final items remain errors. Never invent a completed tool call.
Provider failures and missing protocol completion remain failures.

Keep reliable observed usage when optional usage data is missing or invalid;
do not turn unknown consumption into zero or reject an otherwise usable answer
solely because optional accounting data could not be read.

Use structured `log/slog` warnings for unexpected recoverable anomalies, with a
stable reason, request/attempt identity, upstream, event type and action. Exclude
payloads, arguments, credentials and opaque reasoning. Coalesce repeats within
a request. Log terminal failures once at their owning boundary; routine protocol
translation should not produce warnings.

## Stored provider resources

The initial scope includes stored responses and conversations. Management of
Files, Containers and Vector Stores is deferred, including uploads and artifact
downloads. Their references may appear in generation data, but the gateway must
authorize referenced resources before dispatch.

Resources created through CLAN belong to the access-key identity that created them.
CLAN keeps their upstream/account binding; content stays at the provider. On each
read, continuation, update or deletion, check ownership and the key's current
permissions. Knowing a resource ID does not grant access.

Resource-bound operations use the original account. If it is unavailable, return
an error instead of switching accounts. Round-robin still applies to new requests
without bound resources. Other access keys do not inherit resource access merely
because they can use the same provider account.

Provider adapters receive the selected account and provider resource IDs. Resource
ownership checks and ID resolution belong above the adapter. A session's account
binding does not replace ownership checks for stored resources.

Resource ID representation, externally created resources, resource retention after
key deletion, and account deletion or credential replacement remain to be defined.
CLAN-issued IDs are not required by this decision; choose their representation
with the resource-operation contracts.

For WebSocket sessions, use CLIProxyAPI's account/connection binding and cleanup
as a reference, adapting its Codex behavior to the public OpenAI API. Keep retries
in request execution. Support explicit `response.create` requests; steering and
its automatic continuations are deferred. Reject `response.steer` explicitly.
Cancelling an active response closes its entire WebSocket session, interrupting
other responses on that connection. Preserve known usage and complete local cleanup.
Do not replay interrupted generations automatically.
See [CLIProxyAPI's session implementation](https://github.com/router-for-me/CLIProxyAPI/blob/09a29bd345bc44c473abe7fd07859e32df2ea543/internal/runtime/executor/codex_websockets_session.go).

## Generation lifetime

The current scope includes ordinary responses and streaming, not provider
background generation. Reject requests to start background work before upstream
dispatch; do not silently change them to ordinary generation.

Do not add provider-job polling, durable job recovery or restored concurrency
slots. Existing budget snapshot persistence and interrupted-request history still
apply. Stored responses and conversations remain in scope.

## Access-key disabling and deletion

Disabling or deleting an access key blocks new requests and cancels its active
ordinary requests and streams. Close upstream resources before releasing slots.
Preserve observed usage and history, including pending accounting writes. Cleanup
does not require the client key to authenticate. Re-enabling does not resume
cancelled work. Permission updates follow
[ADR 0006](0006-provide-management-api-without-bundled-ui.md#access-key-updates).

## Context and alternatives

The supported client APIs need the same access, routing and retry behavior. Keeping these
rules in individual HTTP handlers would duplicate policy. Letting each provider
adapter own retries would make the total attempt limit harder to enforce.

This builds on [ADR 0002](0002-use-common-request-format.md), without copying an
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
| Stream lifetime | Early/repeated close, cancellation during blocked I/O, truncation, trailing metadata, event or error but never both, EOF only on normal completion, no events after termination |
| Event conversion | Text; interleaved calls with fragmented arguments and late identity; reasoning with late signatures or no visible text; trailing usage; repeated snapshots; generation limits and abrupt truncation |
| Client encoding | Protocol ordering and preservation of IDs, content, completion reasons and usage for every supported client protocol |

Define concrete Go types, execution signatures, completion-reason mapping,
feature-specific data, error categories and package layout with the adapters and
execution module. Account lifecycle and storage details belong to their own modules.
