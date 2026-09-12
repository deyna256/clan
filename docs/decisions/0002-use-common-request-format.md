# ADR 0002: Use a common typed representation

Status: Accepted. Recorded: 2026-09-08.
Decision owner: project maintainer. Implementation: shared request, result and event
types with the [OpenAI provider adapter](../openai-adapter.md).

## Decision

Use shared Go types for supported LLM requests, responses and stream events.
Adapters convert between these types and client/provider protocols.

Organize types by meaning: generation requests, ordered messages and content,
tool exchanges, reasoning, output settings, results and stream events. Do not add
separate internal Chat, Responses and Messages models just to match API names.
Stored-response and file operations have separate contracts.

The parts of request processing and the owner of retry logic are defined in
[ADR 0003](0003-separate-request-execution-from-protocols.md).

## Context

CLAN will initially serve OpenAI Chat Completions and Responses simultaneously. Account
eligibility, access restrictions, retries and attempt history must work the same way
for both APIs. These rules need only small metadata values, such as model and
account identity. The shared content model serves protocol conversion; execution
does not need to inspect every message or provider field.

The [README](../../README.md#confirmed-scope) defines initial integration scope.
Anthropic's client API is deferred; Anthropic-compatible upstreams remain in scope.
Gemini is deferred on both API sides.

## Alternatives

| Option | Assessment |
|---|---|
| Shared Go types | Selected: adapters reuse a typed content model; execution uses common metadata |
| Direct conversion between each pair of protocols | Gives each pair its own conversion code, but more protocol combinations can mean duplicated logic |

This decision does not select Bifrost or CLIProxyAPI as a dependency or adopt
their interfaces.

## Consequences

The types must preserve supported behavior, including tool calls, model
options and stream ordering. It is not a text-only message format or a promise that
every feature of one provider has an equivalent in every other provider.

Unsupported behavior must be reported explicitly, consistent with the project
principles. Represent shared semantics with common types and provider-specific
features with explicit CLAN-owned types. Keep SDK types inside adapters. Add these
types with the features that use them; do not copy an entire provider schema in
advance or use a generic extension map for unsupported behavior.

A target adapter must reject features it cannot represent faithfully. Provider
details do not become portable merely because they fit in a common request.
Protocol changes may require changes to both CLAN types and adapters.

Keep opaque reasoning data next to its item, with an explicit provider type.
JSON Schema and tool argument documents retain their JSON values without conversion
through floating-point numbers. Do not add a parallel raw-proxy execution path.

## Validation and open details

Before finalizing interfaces, work through ordinary responses, tool calls, streaming,
cancellation and errors across the intended protocol combinations. Later tests must
check the resulting behavior, including values that conversion must preserve.

Types live in `internal/generation`; they cover the features implemented by the
current conversion code, including generated images, custom tools, patch operations,
shell exchanges, computer actions and hosted MCP. Custom input stays separate from
function-call JSON arguments. Shell command strings and local-shell argument arrays
remain distinct; CLAN does not execute them. Computer calls keep single and batched
actions, screenshots and safety checks; adapters never create acknowledgements on the client's behalf.
MCP schemas, annotations and execution-error content keep their JSON values.
An MCP tool failure does not by itself fail the generation.
Tool namespaces remain separate from names; function/custom calls share routing
metadata while preserving their different argument formats.
The adapter guide lists implemented operations and their boundaries. Other provider
adapters and client protocol conversion remain to be implemented.
Revisit if supported operations cannot fit the shared types without adding
protocol-specific parsing to execution logic.
