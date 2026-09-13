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

Use browser OAuth with a callback as the only built-in sign-in method. Codex uses
`http://localhost:1455/auth/callback`. For a remote server, forward port 1455 from
the administrator's machine over SSH, as described in the
[Codex authentication guide](https://learn.chatgpt.com/docs/auth#login-on-headless-devices).
Docker must publish the callback port on the host's loopback interface; its
listener must also be reachable inside the container network.

Bind the callback listener before returning sign-in instructions. Use PKCE and
single-use state tied to the pending login. Report success only after credentials
are stored. Device Code, auth-file imports and alternate connection methods are
outside the first release. Local browser login has been checked; production OAuth,
refresh and Docker forwarding still need validation. See the
[recorded checks](client-contract.md#checks-so-far).

## Model catalog

Load models from Codex using each enabled account's OAuth credentials and combine
their catalogs. Do not maintain model lists by subscription plan. The
[Codex models endpoint](https://github.com/openai/codex/blob/b4c864dd6497ae764e6a826300b34f7ca77ba965/codex-rs/codex-api/src/endpoint/models.rs)
provides the protocol reference. Temporary account cooldowns affect request
routing, not model visibility.

Expose only models with `visibility: list` in `GET /v1/models`. Accept generation
requests only for IDs in an enabled account's full catalog, including hidden
entries. Select by round-robin among eligible accounts whose catalogs contain
that ID. Reject unknown IDs without dispatch; do not substitute another model.
New models become usable after a catalog refresh. Catalog membership does not
guarantee that Codex will accept a request.

Keep each account's last successful catalog in memory when a refresh fails
temporarily. Disabled and deleted accounts stop contributing immediately.
If an account's first load fails, return the other accounts' known models and log
a safe warning. Return HTTP 503 if discovery failures leave no successfully
loaded catalog for any enabled account. A successfully loaded empty catalog is
not a discovery failure.

Load the catalog on first use, then refresh on demand at most once every five
minutes per account with unchanged credentials, including after failed attempts.
Credential changes allow an earlier refresh; changing the ChatGPT account ID also
discards the old catalog. Concurrent callers share one refresh. There is no
periodic worker. Each catalog HTTP request has a five-second timeout, separate
from generation timeouts. After a failed first load, recovery may wait for the
next refresh interval.

The application owns catalog shutdown: cancel refreshes and wait for their cleanup,
including work started for accounts that have since been removed or updated.

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

The code contains OAuth account snapshots, named access keys and secret
verification, credential encryption, per-key concurrency slots, model-based
round-robin, observed usage types and Retry-After parsing.
[SQLite storage](../internal/storage/storage.go) persists accounts and key settings.

The entry point is empty. HTTP routes, Codex integration, request execution,
and management are not implemented yet.

## Remaining decisions and checks

- Verify browser OAuth callback forwarding for server and Docker deployment.
- Verify the agreed generation features against Codex. Acceptance scenarios must
  cover OpenCode tool execution and Python tool loops, as well as complete JSON
  responses and SSE streaming.
- Define management schemas and key/account update behavior.
- Choose timeout and retry settings with the execution module, using the agreed
  [failure details](decisions/0003-separate-request-execution-from-protocols.md#failure-details).

Do not infer automatic model discovery in OpenCode from the presence of
`GET /v1/models`; verify the client's configuration and behavior.
