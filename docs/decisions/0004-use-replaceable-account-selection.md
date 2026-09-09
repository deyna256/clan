# ADR 0004: Start with round-robin behind a replaceable selection contract

Status: Accepted. Recorded: 2026-09-08.
Decision owner: project maintainer. Implementation: not started.

## Decision

Start with round-robin as the only account-selection strategy: take turns choosing
accounts from the allowed set. Give selection a small interface so another strategy
can later replace it without rewriting request processing.

Request execution builds the candidate set using client permissions, the requested
model, supported capabilities, availability and attempt policy. The selector chooses
from those candidates. Request execution manages attempts, checks access and updates
account state, as defined in
[ADR 0003](0003-separate-request-execution-from-protocols.md).

Named account pools are deferred. Group candidates by configured upstream and concrete
model, then apply access-key restrictions on upstreams, models and accounts.
An upstream is the configured destination, not the client's input API format; see
[ADR 0006](0006-provide-management-api-without-bundled-ui.md#resources). This adopts the
account-selection approach from CLIProxyAPI with explicit access restrictions inspired
by Bifrost; it does not adopt either project's entire configuration or implementation.

## Alternatives and rationale

| Option | Assessment |
|---|---|
| Round-robin embedded in request execution | Less initial separation, but replacing it would change request-processing code |
| Round-robin behind a selection contract | Selected: lets us replace the strategy without changing request processing |
| Multiple strategies or a dynamic plugin system immediately | Deferred: only round-robin is required initially |

## Contract requirements

- Return an account ID or an explicit result saying no account was selected. Never introduce
  an account outside the supplied candidates; the caller must enforce this condition.
- Candidate data must not expose credentials or mutable account records to selection.
- Choosing an account does not authorize sending the request or reserve quota. Account
  availability can change after selection; request execution handles this.
- Round-robin state must support concurrent calls safely. Count selections, not tokens
  or completed-request durations; equal request counts do not imply equal resource use.

## Validation and open details

Test rotation over a stable candidate set, empty candidates, membership changes,
concurrent selection and rejection of an invalid selector result by request execution.

Define the Go contract, cursor scope, candidate ordering, membership changes and
session continuity with the selection module. Add another strategy only when an actual
scheduling requirement calls for it; no plugin system or runtime strategy setting is
required initially.
