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
and makes the account available.

Use browser OAuth with a callback as the only built-in sign-in method. Codex uses
`http://localhost:1455/auth/callback`. For a remote server, forward port 1455 from
the administrator's machine over SSH, as described in the
[Codex authentication guide](https://learn.chatgpt.com/docs/auth#login-on-headless-devices).
Docker must publish the callback port on the host's loopback interface; its
listener must also be reachable inside the container network.

Bind the callback listener before returning sign-in instructions. Use PKCE and
single-use state tied to the pending login. Report success only after credentials
are stored. Device Code, auth-file imports and alternate connection methods are
outside the first release. Docker forwarding still needs validation;
see the [recorded checks](client-contract.md#checks-so-far).

### Token refresh and reconnect

Refresh on demand when at most five minutes remain. Concurrent callers share one
refresh; canceling a waiter does not abandon token persistence. There is no
periodic refresh worker. New credentials need a known access-token expiry:
use `expires_in`, falling back to the access token's `exp` claim.

After a network error, HTTP 429 or 5xx, keep using the old access token only while
it is unexpired. Wait at least one minute before another refresh, respecting a
longer `Retry-After`. A rejected refresh or unusable credentials require sign-in.
If saving rotated tokens fails, retry saving that replacement before another
provider refresh; do not expose it before persistence succeeds.

Only explicit reconnect may change the ChatGPT account or workspace. Refresh
preserves its ID. Disablement, deletion and newer credentials invalidate pending
results through [conditional storage updates](decisions/0007-use-sqlite.md).
The manager owns shutdown of the callback listener and OAuth jobs.

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

Execution owns catalog shutdown: cancel refreshes and wait for their cleanup,
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

The [Codex client](../internal/codex/client.go) implements Responses generation,
SSE streaming and authenticated model discovery. Its catalog refreshes on demand.

The [OAuth manager](../internal/codexoauth/manager.go) handles browser login,
on-demand refresh and persisted credential replacement.

The [executor](../internal/execution/execution.go) joins key admission, OAuth,
catalog validation, selection and attempts. It owns the catalog and active request
cleanup. Management must use its mutation methods for revocation, account removal
and concurrency edits. Close execution before the OAuth manager and storage;
then close the Codex client's idle connections.

The entry point is empty. Gateway and management HTTP routes and application
wiring are not implemented yet.

## Remaining decisions and checks

- Verify browser OAuth callback forwarding for server and Docker deployment.
- Verify the agreed generation features against Codex. Acceptance scenarios must
  cover OpenCode tool execution and Python tool loops, as well as complete JSON
  responses and SSE streaming.
- Define management schemas and HTTP server settings, including downstream write
  timeouts. Execution behavior is defined in [ADR 0003](decisions/0003-separate-request-execution-from-protocols.md).

Do not infer automatic model discovery in OpenCode from the presence of
`GET /v1/models`; verify the client's configuration and behavior.
