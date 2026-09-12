# Architecture

This is the first-release design. The [README](../README.md#first-release) defines
its supported features. The gateway is not yet runnable.

## Responsibilities

| Module | Responsibility |
|---|---|
| HTTP API | Routes, request parsing and validation, JSON/SSE output and client errors |
| Request execution | Key checks, concurrency slots, account selection, safe retries, cancellation and outcome logs |
| Codex integration | OAuth protocol, upstream calls, required conversion, failure classification and usage extraction |
| Management | Account connection and status, access-key lifecycle and settings |
| Storage | SQLite persistence for accounts, encrypted credentials, key hashes and settings |

These are responsibilities within one process, not required packages or an
interface for every module. Keep interfaces small and define them where callers
need them. Wire dependencies explicitly at startup.

The [request-flow diagram](../README.md#request-flow) shows the generation path.
Management changes the accounts and key settings used by that path; it does not
run generations.

## Request data and execution

Use Responses as the content format. Preserve JSON that needs no conversion and
use small Go types for values CLAN interprets. Execution needs the model, account,
attempt state, failure details and usage; it does not inspect messages or tool
arguments. See [ADR 0002](decisions/0002-use-responses-as-content-format.md).

The Codex integration runs one generation attempt. Execution owns account
selection and retries, holds the client slot through cleanup, and handles key
revocation. Ordinary and streaming calls use separate methods with the same
validated input. Lifecycle rules are in
[ADR 0003](decisions/0003-separate-request-execution-from-protocols.md).

## Management and account setup

The HTTP JSON management API lives under `/api` and requires a separate admin token.

| Resource | Operations |
|---|---|
| Codex accounts | Connect, inspect status, disable and delete |
| Client access keys | Create, list, revoke and update concurrency limits |
| Models | List available models |

All client keys share the connected accounts and available models. They do not
carry individual account or model permissions.

An administrator starts OAuth connection through the management API and receives
sign-in instructions. After browser sign-in, CLAN stores encrypted credentials
and makes the account available. Refresh expiring tokens before requests and report
when an account needs sign-in again.

Implement one OAuth connection method. Select the concrete flow after checking
server deployments without a browser and Docker. Auth-file imports and alternate
connection methods are outside the first release.

## State and diagnostics

SQLite stores account records, encrypted OAuth credentials, access-key hashes and
key settings. Active requests and round-robin positions live in memory.
[ADR 0007](decisions/0007-use-sqlite.md) defines storage ownership.

Use structured JSON logs for:

- Request outcomes: request ID, access-key ID, model, duration and result.
- Failures and account switches: safe reasons and account IDs.
- Observed token usage, with unknown counts distinct from zero.

Credentials, request content and response content must stay out of logs. There
is no database request history or management endpoint for historical consumption.
Known usage does not block new requests; a client can consume the subscription
through sequential requests despite its concurrency limit.

## Existing code

The source has not yet been aligned with this design. The entry point is empty.
The current admission coordinator combines budgets, RPM and snapshot persistence;
replace it with simpler execution for the first-release scope.

Reuse applicable account, key-verification, selection, concurrency, usage and
credential-encryption code after checking it against these requirements. Existing
permission rules, API-key credential variants and budget storage are
not first-release requirements. Their presence does not expand the planned scope.

## Remaining decisions and checks

- Select and verify the OAuth flow for server and Docker deployment.
- Verify the agreed generation features against Codex. Acceptance scenarios must
  cover OpenCode tool execution and Python tool loops, as well as complete JSON
  responses and SSE streaming.
- Define management schemas, key/account update behavior and model catalog loading.
- Define how attempt outcomes and usage are exposed on stream failure, and choose
  timeout and retry settings with the execution module.
- Agree on implementation milestones before starting them.

Do not infer automatic model discovery in OpenCode from the presence of
`GET /v1/models`; verify the client's configuration and behavior.
