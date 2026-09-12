# OpenAI provider adapter

`internal/provider/openai` calls the public OpenAI Responses API using an API key
and a configurable base URL. It is an outbound adapter, not CLAN's client API or
Codex OAuth integration. The gateway is not yet runnable.

## Operations

| Area | Methods |
|---|---|
| Generation | `Generate`, `GenerateStream` |
| WebSocket | `OpenSession`, then `SendCreate`, `Next`, `Close` |
| Stored responses | Retrieve JSON or stream from the beginning, delete, list input items, compact, count input tokens |
| Conversations | Create, retrieve, update, delete; add, list, retrieve and delete items |

Generation covers text, image/file input, structured output, reasoning, function
and custom tools. Native tools include web/file search, image generation, Code
Interpreter, shell/local_shell, apply_patch, computer use, hosted MCP, namespaces,
tool search and programmatic calls. Tool declarations and calls are data: CLAN
does not execute shell commands, patches or computer actions.
Common types live in `internal/generation`; wire JSON stays inside the adapter.
Generation requires the concrete model selected by CLAN, including requests that
use a saved prompt.

List methods return one page and its cursors. Resource status is returned as data,
including pending or failed processing. There is no automatic polling.

## Ownership and lifetime

Create clients with `New`, supplying an HTTP client, logger and positive body/event
limits. Clients may serve concurrent calls. Execution supplies selected credentials
in `Attempt` after access, limits and resource ownership have been checked.
Provider IDs do not grant access; bound resources must keep their original account.

`Generate` closes its response body before returning. Streams have one sequential
`Next` consumer; `Close` may overlap a blocked read and is idempotent. EOF without a
provider terminal event is an interrupted generation. Known usage survives content
errors and cancellation; unknown counters are not estimated.

A WebSocket session stays bound to its opening account. One reader routes events
by lane and response ID. Each explicit create needs admission by the caller.
Cancelling a generation closes the whole connection, including its other active
responses. The adapter does not reconnect or replay requests. A lane-level request
error without a response ID is not assigned to an active generation.

Stored-response usage is historical and must not be charged again on retrieval.
Compaction returns its own observed usage. Counting input tokens does not consume
generation tokens and returns only the count.

## Boundaries

Files, Containers and Vector Stores management is deferred. The adapter does not
upload files, download container artifacts or manage file-search indexes. Resource
references in generation data do not grant access; the gateway must authorize them
before dispatch.

Background generation and WebSocket steering are excluded. Stored-stream retrieval
starts from the beginning; it does not resume from an event cursor. Account-wide
resource listings, vector search and batch resource operations are not exposed.

The adapter adds no retry loop and does not follow redirects. The standard HTTP
transport may recover a stale connection for an idempotent read. Generation bodies
cannot be replayed by that transport.

SSE framing uses `go-sse`. Limits count encoded event bytes, including both bytes
of CRLF. A complete terminal event ending in a newline may be accepted at EOF;
EOF without a terminal response remains an interrupted generation.

Malformed optional display metadata can be skipped with a coalesced warning.
Conflicting tool identities, unusable final content and missing completion remain
errors. Logs omit payloads, credentials and opaque reasoning. See
[ADR 0003](decisions/0003-separate-request-execution-from-protocols.md) for the policy.

Protocol references: [Responses](https://developers.openai.com/api/reference/resources/responses),
[WebSocket mode](https://developers.openai.com/api/docs/guides/websocket-mode),
[Conversations](https://developers.openai.com/api/reference/resources/conversations).
