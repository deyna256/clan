# Roadmap

Draft for review. Updated: 2026-10-05. These are planned goals, with no release
dates. See [Architecture](architecture.md) for what works today.
The [provider design](provider-design.md) defines the planned interfaces, request
flows, storage, sessions and provider rules used by these steps.

## Idea and goal

CLAN lets applications use several model providers through one gateway.
Administrators connect provider accounts and manage access, limits and usage.
Applications use CLAN keys. Provider keys and tokens stay inside CLAN.

Our goal is to make providers easy to add and use:

- List models as `codex/<model>`, `claude-code/<model>` and similar names.
  Start with native APIs: each uses its provider's request and response format.
- Match a tested Codex or Claude Code version for login, requests, sessions and
  network behavior. Use documented APIs for regular API-key connections.
- Share access checks, limits, account selection, cancellation and usage records.
  Keep each provider's rules in its integration.
- Later, add a common format for clients that need it. Keep native APIs available.

Protect secrets with access checks and encryption. Matching client behavior does
not guarantee that traffic looks identical or that a connection method is allowed.

## Starting point

CLAN supports Codex OAuth, Responses JSON/SSE, model lists, revocable keys,
concurrency limits and usage reports. Request execution still depends directly
on Codex, OAuth and SQLite code. Full matching with the original client is untested.
See the [recorded checks](client-contract.md#checks-so-far).

Add Claude Code first, then Jev to test API-key access and ordinary JSON calls.
Keep one process and SQLite.

## Delivery order

Build steps 2 and 3 together in small changes. Use the real Claude integration
to shape shared interfaces, and keep Codex working throughout.

| Step | Result | Dependency |
|---|---|---|
| 1 | A tested way to connect Claude Code | Start here |
| 2 | Shared request handling and replaceable storage | Start alongside step 1; finish after its checks pass |
| 3 | Claude Code through the Messages API | Steps 1 and 2 |
| 4 | Jev through its own API | Step 3 |
| 5 | Codex behavior matches a tested client version | Step 2; can overlap steps 3–4 |
| 6 | A common format for a real client need | Working native APIs and an agreed use case |

### 1. Check how to connect Claude Code

**Goal:** know how to connect and which client behavior to support.

**Done when:**

- A short document names the client version, platform, supported calls and
  session rules. It checks the connection method against
  [Anthropic's rules](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use).
- A live connection and model call work. Saved test traffic has secrets removed.
  Findings from source code are separate from live test results.
- Known differences are listed. Each planned header, body or network change
  has a reason and a test. Add custom TLS or WebSocket support only if needed.

Resolve any connection restrictions before enabling access. If this approach
cannot work, agree a new plan with the maintainer. API-key access or running the
original agent is a separate decision.

### 2. Share request handling and separate storage

**Goal:** reuse CLAN's request handling while each provider keeps its own API and
authentication rules.

**Done when:**

- Execution defines the interfaces it needs and no longer imports storage,
  SQLite, Codex or OAuth implementations. JSON calls, streams, model loading and
  authentication are separate; providers implement only what they support.
- One request owns its concurrency slot until delivery, cleanup and recording
  finish. JSON and stream tests cover failed calls, cancellation, repeated close,
  key revocation and shutdown.
- Storage upgrades keep accounts, keys and history. Memory and SQLite tests
  cover failed writes and prevent old refresh results from replacing newer
  credentials. Refresh keeps the account identity and active requests.
- Codex checks pass, as does one real Claude call through the shared interfaces.
  JSON-only and stream-only test integrations also pass. Review and update
  [ADR 0002](decisions/0002-use-responses-as-content-format.md) and
  [ADR 0003](decisions/0003-separate-request-execution-from-protocols.md) before
  merging changes to their rules. Extend
  [ADR 0016](decisions/0016-record-request-metadata.md) to cover each generation API.

### 3. Use Claude Code and Codex with the same CLAN key

**Goal:** use both providers with the same access rules and limits.

**Done when:**

- Administrators can connect, reconnect, disable, enable and delete Claude
  accounts using the method from step 1. `/v1/messages` supports the agreed
  JSON/SSE calls, including tool calls and results.
- Model lists require a CLAN key and show provider prefixes, APIs and response
  modes. Existing Codex names still work. Unsupported combinations fail before
  generation. CLAN never switches providers automatically or forwards its keys
  to them.
- Tests using both providers verify shared limits, cancellation, safe retries
  and usage records. Unknown usage is not recorded as zero. Logs and records
  contain no secrets or request/response content.
- Where sessions are required, the API defines how to start a session and how to
  continue one. Tests cover several sessions per key, equal IDs
  under different keys, concurrent creation, ordered turns, expiry and restart.
  Refresh keeps sessions; reconnect clears affected state. Bound sessions keep
  their account after failure. Continuing a session with lost state fails.
  Cancellation stops blocked reads; slow clients cannot cause unlimited buffering.
- Live JSON/SSE and tool tests pass. Saved test traffic matches the agreed client
  behavior. Client docs list versions, differences and checks needed after updates.

### 4. Add Jev through its native API

**Goal:** add an API-key provider without copying shared code.

**Done when:**

- Administrators can save an encrypted TypeSafe API key. Clients use a CLAN key
  and `typesafe/<model>` through `/v1/systemone`, following the
  [native API](https://docs.typesafe.ai/api) and
  [model naming rules](https://docs.typesafe.ai/models).
- JSON calls preserve inputs, answers, errors and known usage. Unsupported
  streaming fails before a provider call. Request `state` is content, not a
  session ID.
- Jev uses the same limits, cancellation, account management and recording as
  Codex and Claude, with no OAuth or fake stream. Shared tests and a live call pass.
- Review confirms that provider rules stay in the integration, setup and API
  handler. Changes to shared rules are explained and tested. Document how to
  add the next provider using this work as an example.

### 5. Match a tested Codex version

**Goal:** verify Codex behavior against the original client.

**Done when:**

- A short document names the client version and covers login, requests, network
  behavior and sessions.
- Required profile changes are implemented. Automated tests compare saved test
  traffic, with secrets removed, against the agreed behavior. They cover refresh,
  tool continuation and cancellation. Sessions use shared execution code.
- Live checks pass. Differences are documented and client-version updates repeat
  the checks. Existing clients still work.

### 6. Offer a common format alongside native APIs

**Goal:** let clients switch providers without rewriting request and response code.

Start with a real client need. Choose the format and supported features then.

**Done when:** the same use case works with at least two integrations. Tests show
that request, response, stream and error conversion keeps their meaning.
Unsupported conversions fail clearly. Native APIs remain available.

## Keeping the plan current

Agree each step with the maintainer before starting. Review the plan after each
step or new findings. Mark work done with links to merged changes and test results.
Missing live checks remain unfinished work.

Create or update issues as each step starts; check for existing tasks first.
Keep coding details in issues. [#54](https://github.com/deyna256/clan/issues/54)
already covers request ownership. Update [#74](https://github.com/deyna256/clan/issues/74)
from Responses conversion to native Messages before Claude work starts.
Independent fixes can ship at any stage.

This plan excludes automatic session transfer, running agent tools or workspaces,
dynamic plugins, other databases and multiple active gateway instances.

Roadmap guidance: [Atlassian](https://www.atlassian.com/agile/product-management/product-roadmaps),
[SVPG](https://www.svpg.com/changing-how-you-decide-which-problems-to-solve/),
[Product Talk](https://www.producttalk.org/product-roadmaps/).
