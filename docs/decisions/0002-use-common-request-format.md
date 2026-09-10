# ADR 0002: Use a common typed representation

Status: Accepted. Recorded: 2026-09-08.
Decision owner: project maintainer. Implementation: not started.

## Decision

Use shared Go types for supported LLM requests, responses and stream events.
Adapters convert between these types and client/provider protocols.

The parts of request processing and the owner of retry logic are defined in
[ADR 0003](0003-separate-request-execution-from-protocols.md).

## Context

CLAN will initially serve OpenAI and Anthropic client APIs simultaneously. Account
eligibility, access restrictions, retries and attempt history must work the same way
for both APIs. Shared types let these rules inspect a request without parsing each
provider's JSON format.

The [README](../../README.md#confirmed-scope) defines initial integration scope.
Gemini is deferred on both API sides.

## Alternatives

| Option | Assessment |
|---|---|
| Shared Go types | Selected: execution rules and adapters use the same request model |
| Direct conversion between each pair of protocols | Gives each pair its own conversion code, but more protocol combinations can mean duplicated logic |

This decision does not select Bifrost or CLIProxyAPI as a dependency or adopt
their interfaces.

## Consequences

The types must preserve supported behavior, including tool calls, model
options and stream ordering. It is not a text-only message format or a promise that
every feature of one provider has an equivalent in every other provider.

Unsupported behavior must be reported explicitly, consistent with the project
principles. How provider-specific fields are represented remains an open design issue.
Protocol changes may require changes to both the shared types and adapters.

## Validation and open details

Before finalizing interfaces, work through ordinary responses, tool calls, streaming,
cancellation and errors across the intended protocol combinations. Later tests must
check the resulting behavior, including values that conversion must preserve.

Exact Go types and provider-specific extensions will be designed with the adapters.
Revisit if supported operations cannot fit the shared types without adding
protocol-specific parsing to execution logic.
