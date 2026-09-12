# ADR 0002: Use Responses as the content format

Status: Accepted. Reviewed: 2026-09-12.

## Context

The first release serves Responses clients through Codex OAuth. A separate
provider-neutral model would require another set of message, tool and event
types, plus conversion in both directions.

## Decision

Use Responses as the reference content format and keep execution metadata small.

- HTTP validates the request and supported features.
- Execution reads model, account, attempt state, failures and usage without
  parsing messages or tool arguments.
- The Codex integration converts only the differences required by its protocol.
- Use small Go structs for interpreted values and `json.RawMessage` for JSON
  carried through without conversion.
- Validated fields and dispatched values have one source of truth. Do not keep
  independently editable copies of the same request data.
- Preserve supported content, including opaque reasoning continuation data.
  Do not rebuild it as a universal message or event model.

The [integration contract](0003-separate-request-execution-from-protocols.md)
defines results, streams and failure ownership.

## Alternatives and consequences

A provider-neutral model could serve many protocol combinations, but adds
unnecessary conversion for the selected scope. Transparent HTTP forwarding alone
does not handle Codex differences, validation or stream completion.

Responses becomes an internal content dependency. A future Anthropic integration
would translate the supported Responses subset to and from Anthropic without
moving protocol parsing into execution. New features still require validation
and compatibility tests; raw JSON does not imply unrestricted passthrough.
