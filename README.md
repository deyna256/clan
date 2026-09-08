<div align="center">

<h1>CLAN</h1>

<p><strong>Cooperative LLM Access Network</strong></p>

<p><em>One account hits its limit — the next one picks up.</em></p>

</div>

---

CLAN is a self-hosted gateway that puts a single OpenAI-compatible endpoint in front of
every LLM account you own — CLI subscriptions and plain API keys alike.

Accounts join a shared pool. CLAN routes each request to an eligible account and can retry
on another when limits or failures prevent it from serving the request. Failover follows
explicit rules and never silently restarts a response that has already begun streaming.

**Language:** Go. Its concurrency primitives and standard HTTP library suit a gateway
handling simultaneous requests, long-lived streams, and cancellation.

> **Status:** early. The design is still being worked out and there is no usable build yet.

## Principles

- **Free and open forever.** CLAN and all its features will always be free and open
  source, with no paid tiers or proprietary editions.
- **Owner control.** The operator controls deployment, connected accounts, allowed
  destinations, and data retention. Secrets and request content stay out of default logs.
- **Predictable behavior.** Model changes, paid API fallback, and semantic changes to
  requests require explicit configuration. Retries have limits and respect whether a
  request can safely be replayed.
- **Faithful compatibility.** Preserve streaming, tool calls, and model options for
  supported integrations. Document compatibility boundaries and reject unsupported
  behavior rather than silently dropping it.
- **Explainable decisions.** Show why an account was selected or excluded and what
  happened on each attempt. Distinguish observed limits from estimates and unknowns.
- **Simple operation.** Keep setup, configuration, and required infrastructure small.
  Provide actionable errors and safe defaults.

## Confirmed scope

These features are agreed goals, not implemented capabilities. Other proposed features
remain under discussion.

- **Shared account pools.** Connect CLI subscriptions and API keys through one gateway.
- **Automatic failover.** Switch to another eligible account on retryable limits or
  failures, within the configured access and routing rules.
- **Pool status and attempt history.** Inspect account availability, reasons for
  failures and switches, and the attempts made for a request, without exposing secrets.
- **Scoped client keys.** Give applications and team members separate client keys with
  access to selected models and pools. Enforce these restrictions on every attempt,
  including fallback.
