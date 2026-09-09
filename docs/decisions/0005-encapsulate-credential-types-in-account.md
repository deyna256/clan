# ADR 0005: Encapsulate distinct credential types in Account

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: not started.

## Decision

`Account` represents credentials and availability for one configured upstream,
referenced by ID as defined in [ADR 0006](0006-provide-management-api-without-bundled-ui.md#resources).
Other modules use the same account interface for both authorization methods.
Each account contains exactly one credential type: `OAuthCredentials` or
`APIKeyCredentials`. These names describe the design; exact Go types and methods
are still open.

```text
Account
  ID, name, upstream reference
  Configuration and availability state
  Credentials: exactly one variant
    OAuthCredentials: tokens, expiry and provider-specific renewal data
    APIKeyCredentials: API key
```

Each credential type holds the data its authorization method needs, including
provider-specific fields. API-key credentials do not need OAuth renewal or a dummy
`Refresh` method. Credentials belong to the account; they have no separate IDs or
management operations in this design.

Account selection and access rules work with account identity and eligibility,
without inspecting the credential variant or receiving its secrets. The integration
preparing an authenticated upstream call handles the relevant authorization details.
Access keys authorizing access to CLAN remain separate from upstream credentials.

`Account` does not make HTTP calls, select other accounts or manage retries.
Those responsibilities remain with the provider
adapter and request execution as defined in [ADR 0003](0003-separate-request-execution-from-protocols.md).

## OAuth renewal timing

Check OAuth credential freshness before dispatching an upstream generation attempt
and renew only when needed. Reuse a token that is still fresh enough;
do not refresh it on every request. API-key
accounts do not participate in OAuth renewal.

Keep credential preparation and renewal independent of the HTTP handler and any
background scheduler. A future hybrid approach can add a background trigger using
the same renewal mechanism, while retaining the check before dispatch. No background
worker, scheduler configuration or additional scheduling interface is needed initially.

Concurrent requests for the same account must coordinate renewal and recheck the
latest credentials before starting another refresh. Requests needing the refreshed
token wait for the shared work; renewal for one account must not hold a global lock
across network I/O and block other accounts.

Renewal must not hide generation retries or account switching.

## Alternatives and rationale

- A single credentials record with optional OAuth and API-key fields makes mixed or
  incomplete combinations easy to represent.
- Separate OAuth and API-key account types would need extra code to share their
  selection, access and status rules.
- Separate credential types inside one account keep a common interface while storing
  the data each authorization method needs. This is the selected approach.

## Consequences and validation

Account creation must enforce exactly one valid credential type.
Test this rule and the same account-selection and
access behavior for both authorization methods, without exposing credentials to selection.

Renewal checks should cover fresh-token reuse, renewal before dispatch when due,
concurrent requests sharing an update, and independent progress for other accounts.

Define OAuth connection/import flows, expiry margins and unknown expiry, shared-refresh
cancellation, persistence failures and recovery after `401` with the account module.
Availability transitions and concurrent state updates also belong there. Storage
requirements are defined in [ADR 0007](0007-support-sqlite-and-postgresql.md#secret-storage).
Two account records may still share the same upstream quota.
