<div align="center">

<h1>CLAN</h1>

<p><strong>Cooperative LLM Access Network</strong></p>

<p><em>One account hits its limit — the next one picks up.</em></p>

</div>

---

CLAN is a self-hosted gateway for your LLM accounts, including CLI subscriptions
and API keys. It serves OpenAI- and Anthropic-compatible APIs in one installation.

Administrators configure external LLM services (upstreams), connect accounts, and
issue access keys that control which services applications and people can use.

CLAN chooses an account for the requested model and upstream. After a failure known
to allow retry, it can try another allowed account before the client response begins.
It does not resend requests whose upstream outcome is unknown.

**Language:** Go. Its concurrency support and standard HTTP library suit a gateway
handling simultaneous requests, long-lived streams, and cancellation.

**HTTP:** `net/http.Server` with [chi](https://github.com/go-chi/chi) for routing
and middleware groups. Handlers use standard `net/http` types.

> **Status:** early. The design is still being worked out and there is no usable build yet.

See the [documentation](#documentation) for project research and accepted decisions.

## Principles

- **Free and open forever.** CLAN and all its features will always be free and open
  source, with no paid tiers or proprietary editions.
- **Owner control.** The operator controls deployment, connected accounts, allowed
  destinations, and how long data is kept. Secrets and request content stay out of default logs.
- **Predictable behavior.** Switching models, falling back to a paid API, or changing
  what a request means requires explicit configuration. Retries are limited and
  allowed only when the request can safely be sent again.
- **Faithful compatibility.** Preserve streaming, tool calls, and model options for
  supported integrations. Document what each integration supports and reject unsupported
  behavior rather than silently dropping it.
- **Explainable decisions.** Show why an account was selected or excluded and what
  happened on each attempt. Distinguish observed limits from estimates and unknowns.
- **Simple operation.** Keep setup, configuration, and required infrastructure small.
  Provide errors that help users fix the problem, and safe defaults.

## Confirmed scope

This is the agreed product scope. Some core modules are implemented; the gateway
is not yet usable.

| Area | Initial scope |
|---|---|
| Client APIs | OpenAI Chat Completions, OpenAI Responses and Anthropic Messages, available simultaneously |
| Upstream integrations | Codex OAuth; Claude OAuth/API keys; OpenAI-compatible and Anthropic-compatible services through a base URL and API key, including [MiniMax](https://platform.minimax.io/docs/api-reference/text-anthropic-api) |
| Accounts and routing | Accounts belong to upstreams. Choose an allowed account for the requested model using round-robin; check access on every attempt |
| Failover | Limit retries to failures that allow them. Never silently restart a response or resend a request whose outcome is unknown |
| Access keys | Separate keys for applications and people, restricted by upstream, model and account |
| Limits | Request-rate limits with token-bucket bursts, concurrent client requests, and token budgets over independent fixed 5-hour and 7-day windows |
| Consumption | Count known input/output tokens, including failed attempts; show when usage is unknown. An attempt that passes budget checks may finish over budget |
| History | Request outcomes, separate attempts, account status and safe failure details; one year of configurable retention and reports computed on demand |
| Management | HTTP JSON API under `/api`, with a separate admin token. Generate OpenAPI from Go and publish it through a dedicated endpoint |
| Model discovery | Expose models permitted by the caller's access key. OpenCode, Codex and Claude Code are required clients; discovery behavior depends on the client |
| Storage | SQLite by default; PostgreSQL selected through environment configuration, with the same application features and no silent fallback |
| Deployment | One process or container per installation, handling concurrent requests |
| Secrets | Show access keys once, then store only hashes for checking them. Encrypt upstream credentials with a key kept outside the database |

The web panel will be delivered from a separate repository. This repository provides
its backend API. Request bodies, model responses and individual stream chunks are
not stored by default. Recording these contents is outside the initial scope.

Each integration will list the operations it supports and any client limitations.
See the [accepted ADRs](#decision-log) for exact contracts and trade-offs,
including budget overruns, incomplete history and possible loss of unsaved usage on a crash.

## Request flow

The planned request path runs inside one CLAN process. Solid arrows show outgoing
requests; dashed arrows show results returning to the client. These are runtime
flows, not Go package dependencies.

```mermaid
flowchart TB
    accTitle: CLAN request flow
    accDescr: A client request passes through an inbound adapter, request execution and a provider adapter within one CLAN process. Results return through the same blocks. Request execution owns access checks, account selection and retries.

    client["Client application"]

    subgraph clan["CLAN · one process"]
        inbound["Inbound adapter<br/>Validate client requests<br/>Convert client protocol"]
        execution["Request execution<br/>Check access and limits<br/>Filter and select accounts<br/>Manage retries"]
        provider["Provider adapter<br/>Use account credentials<br/>Convert upstream protocol<br/>Classify errors"]
    end

    upstream["External LLM service"]

    client -->|"Request + access key"| inbound
    inbound -->|"Common request + identity"| execution
    execution -->|"Attempt + account + model"| provider
    provider -->|"Authenticated request"| upstream

    upstream -.->|"Upstream result"| provider
    provider -.->|"Common result"| execution
    execution -.->|"Common result"| inbound
    inbound -.->|"Client result"| client

    classDef edge fill:#f1f5f9,stroke:#64748b,color:#0f172a
    classDef adapter fill:#eff6ff,stroke:#2563eb,color:#172554
    classDef core fill:#ecfdf5,stroke:#059669,color:#064e3b
    class client,upstream edge
    class inbound,provider adapter
    class execution core
    style clan fill:transparent,stroke:#94a3b8,stroke-dasharray:5 5
```

A result is a complete response, a stream or an error. Internally, streaming uses
shared `Event` values through `Next`/`Close`. Validation or admission can reject a
request before it reaches the upstream.

Request execution selects eligible accounts with round-robin and owns retries,
cancellation, timeouts, token accounting and attempt history. Each attempt keeps
the requested model and the caller's access rules. See
[ADR 0003](docs/decisions/0003-separate-request-execution-from-protocols.md) for the
full contract.

## Deferred

Gemini on both API sides, named account pools, model aliases or automatic model
switching, additional selection strategies, cost tracking and USD budgets,
and multiple active gateway instances are outside the initial scope.

## Documentation

See the [development guide](docs/development.md) for agreed coding conventions
and review questions. The [contribution guide](CONTRIBUTING.md) covers issues,
branches, commits and checks. Community behavior is covered by the
[Code of Conduct](CODE_OF_CONDUCT.md).

### Research

| I want to… | Read |
|---|---|
| Review fixed budget-window transitions for issue #9 | [Budget windows](docs/research/budget-window-transitions.md) |
| Review retry eligibility and delay rules for issue #10 | [Retry policy](docs/research/retry-policy.md) |
| Review access-key generation and verification for issue #11 | [Access-key verification](docs/research/access-key-verification.md) |
| Review encryption of stored credentials for issue #12 | [Credential encryption](docs/research/upstream-credential-encryption.md) |

### Decision log

| Record | Status | What it decides |
|---|---|---|
| [0001 — Use Go](docs/decisions/0001-use-go.md) | Accepted | Application language |
| [0002 — Common typed representation](docs/decisions/0002-use-common-request-format.md) | Accepted | Common internal request, response and event types |
| [0003 — Execution and protocol adapters](docs/decisions/0003-separate-request-execution-from-protocols.md) | Accepted | Protocol adapters, execution ownership and Next/Close stream contract |
| [0004 — Replaceable account selection](docs/decisions/0004-use-replaceable-account-selection.md) | Accepted | Round-robin with a replaceable selection contract |
| [0005 — Account and credential types](docs/decisions/0005-encapsulate-credential-types-in-account.md) | Accepted | Account credential variants and OAuth renewal before dispatch |
| [0006 — Management API](docs/decisions/0006-provide-management-api-without-bundled-ui.md) | Accepted | Management resources, admin access and Go-generated OpenAPI |
| [0007 — Storage backends](docs/decisions/0007-support-sqlite-and-postgresql.md) | Accepted | Two database backends, one gateway instance and secret storage |
| [0008 — Token-budget enforcement](docs/decisions/0008-enforce-token-budgets-at-admission.md) | Accepted | Token windows, observed usage, overruns and accounting failures |
| [0009 — Request history](docs/decisions/0009-record-request-and-attempt-history.md) | Accepted | Request/attempt history, retention, reporting and recovery |
| [0010 — Client-request concurrency](docs/decisions/0010-limit-concurrent-client-requests.md) | Accepted | One slot per active client request; reject without queuing |
| [0011 — Sliding-window RPM](docs/decisions/0011-use-sliding-window-rpm.md) | Superseded by 0015 | Previous exact sliding-window policy |
| [0012 — Retry policy](docs/decisions/0012-retry-classified-transient-failures.md) | Accepted | Retry eligibility, attempt/wait limits and Retry-After |
| [0013 — Timeout policies](docs/decisions/0013-separate-ordinary-and-streaming-timeouts.md) | Accepted | Separate overall, startup, inactivity and write timeouts |
| [0014 — HTTP routing](docs/decisions/0014-use-chi-for-http-routing.md) | Accepted | chi over net/http, route groups and standard handlers |
| [0015 — Token-bucket rate limits](docs/decisions/0015-use-token-bucket-rate-limits.md) | Accepted | Sustained request rate and burst capacity, counted once per client request |

### Design sequencing

Next, finish the remaining
[management contract decisions](docs/decisions/0006-provide-management-api-without-bundled-ui.md#remaining-contract-decisions).
Resolve other open questions with the modules responsible for them: account
availability and OAuth recovery, live configuration
changes, model discovery and protocol coverage, selection and conversation continuity,
client-facing errors, and startup/shutdown behavior. Select numeric timeout defaults
during implementation.

These details are still open; they are not approved behavior. If a choice affects
another module, document the shared rules and check them against the accepted ADRs.

### Documentation conventions

Keep Markdown in this repository. This README describes purpose, principles, scope
and documentation links. Architecture decision records (ADRs) describe agreed behavior
and explain why it was chosen.
Research records dated evidence and alternatives, not additional requirements.
Link to ADRs and code for current contracts instead of copying their rules and types.

An ADR explains the decision, the problem, alternatives, trade-offs, how to test it,
and remaining questions. Keep each record focused and link to another ADR instead of
repeating its rules. This follows [Michael Nygard's ADR guidance](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions).

**Accepted** means the maintainer agreed to the decision; it does not mean implemented.
New decisions start as **Proposed**. If a decision changes, keep the reasons for it,
mark it **Superseded** and link its replacement. Wording corrections do not change
accepted behavior or require another ADR.

Add guides when users can follow them, and reference pages when configuration and
API contracts exist. Link commands to the [Justfile](Justfile).
No documentation site, extra framework sections or empty documents are needed.

Use text diagrams or Mermaid beside the explanation when they clarify a boundary or
flow. State whether arrows show runtime calls or code dependencies. Keep Go
interfaces small and based on what the calling code needs. Separate responsibilities
do not require a service, package hierarchy or interface for every function.
See [Go's interface guidance](https://go.dev/wiki/CodeReviewComments#interfaces).

When a module changes, update its affected contracts and links in the same change.
LLM instructions should link to these decisions rather than duplicate them.
