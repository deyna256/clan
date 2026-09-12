# ADR 0004: Start with round-robin behind a replaceable selection contract

Status: Accepted. Recorded: 2026-09-08.
Decision owner: project maintainer.
Implementation: [round-robin selector](../../internal/selection/round_robin.go) implemented;
request execution integration is pending.

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
model, then apply access-key restrictions on upstreams and models. Access keys
do not restrict individual accounts within a permitted upstream.
An upstream is the configured destination, not the client's input API format; see
[ADR 0006](0006-provide-management-api-without-bundled-ui.md#resources). This adopts the
account-selection approach from CLIProxyAPI with explicit access restrictions inspired
by Bifrost; it does not adopt either project's entire configuration or implementation.

### Round-robin state scope

Keep an independent round-robin position for each configured
upstream ID and concrete model name. Access keys share that position; each call
still supplies only the candidates allowed for its request. Do not keep a separate
position per access key or inbound protocol.

This shares account rotation across clients. Different candidate sets do not
guarantee an even distribution for each individual access key.

### State lifetime

One selector instance serves all requests in the gateway
process. Keep its positions only in memory; do not save them to the database.
Restart clears all positions, so each upstream/model pair starts again with the
smallest available account ID. This does not reset saved usage or token budgets.

### Candidate order and membership changes

Use a stable order by account ID, independent of the input
list order. Remember the last selected ID for each upstream/model pair. Choose
the smallest candidate ID greater than it, wrapping to the smallest candidate
when none is greater. Without a previous selection, choose the smallest ID.

Removing the last selected account does not reset the position. New or returning
accounts take their place in ID order. An empty candidate set returns no selection
and leaves the position unchanged.

### Selection interface

```go
Select(scope Scope, candidates []account.ID) (account.ID, bool)
```

`Scope` identifies the upstream and concrete model. The input order does not affect
selection. Return the selected account ID and `true`, or the zero ID and `false`
when there are no candidates. An empty set is an expected result, not an error.
Request execution knows why accounts were excluded and handles that outcome.

Selection performs no I/O and does not wait for account availability. It needs
neither an error return nor a `context.Context` parameter for this operation.
`Scope.UpstreamID` uses `upstream.ID`; candidate IDs use `account.ID`, as defined
in [ADR 0005](0005-encapsulate-credential-types-in-account.md#go-model).

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
Check that different upstream/model pairs have independent positions and access
keys share a position without bypassing candidate restrictions.
Check input-order independence, wraparound, removal of the last selected account,
new and returning candidates, and preservation of the position across empty calls.
Verify that a new selector starts with no positions while an existing instance
retains them across calls.

Define cleanup of positions for removed upstream/model pairs with configuration
lifecycle handling. Session continuity remains an execution and adapter concern;
this selector contract does not guarantee account affinity for a conversation.
Add another strategy only when an actual scheduling requirement calls for it;
no plugin system or runtime strategy setting is required initially.
