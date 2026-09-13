# ADR 0002: Use Responses as the content format

Status: Accepted. Reviewed: 2026-09-13.

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

### Request parameters

Accept an explicit supported set of Responses parameters. Reject unknown or
unsupported parameters before dispatch with HTTP 400 and a safe error naming the
field. Do not silently remove them or forward them for Codex to test. The
compatibility exception below applies only to `max_output_tokens`.

JSON Schema properties and function argument keys are client data, not protocol
parameters; do not apply the parameter allowlist to those keys.

See the agreed [base request parameters](../client-contract.md#base-request-parameters).

### Codex compatibility exception

Remove `max_output_tokens` before dispatch to Codex. CLAN does not enforce this
requested output limit. OpenCode supplies it automatically, and
[CLIProxyAPI removes it for Codex](https://github.com/router-for-me/CLIProxyAPI/blob/ac02da6c05e18f465aa7e3ed5b0a65a2f060917d/internal/translator/codex/openai/responses/codex_openai-responses_request.go#L30).
This exception keeps ordinary OpenCode requests usable without a client plugin.

When a valid, non-null value is removed, emit one structured warning per client
request with the request ID and parameter name. Do not log request content.
Document the exception in client setup; it does not allow silently dropping other
parameters.

## Alternatives and consequences

A provider-neutral model could serve many protocol combinations, but adds
unnecessary conversion for the selected scope. Transparent HTTP forwarding alone
does not handle Codex differences, validation or stream completion.

Responses becomes an internal content dependency. A future Anthropic integration
would translate the supported Responses subset to and from Anthropic without
moving protocol parsing into execution. New features still require validation
and compatibility tests; raw JSON does not imply unrestricted passthrough.
