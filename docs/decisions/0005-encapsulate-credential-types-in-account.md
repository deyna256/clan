# ADR 0005: Encapsulate distinct credential types in Account

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer.
Implementation: [account model](../../internal/account/account.go) implemented;
availability and OAuth renewal are pending.

## Decision

`Account` represents credentials and availability for one configured upstream,
referenced by ID as defined in [ADR 0006](0006-provide-management-api-without-bundled-ui.md#resources).
Other modules use the same account interface for both authorization methods.
Each account contains exactly one credential type: `OAuthCredentials` or
`APIKeyCredentials`.

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

## Go model

Implemented for issue #3 on 2026-09-10:

- `account.New(identity, credentials)` returns an immutable `Account` snapshot.
  Its zero value is invalid. To replace credentials, construct a new snapshot.
- `Identity()` returns only the account ID, name and upstream ID. `Credentials()`
  returns the credential value for a provider integration. Whole accounts and
  credentials contain secrets and must not be logged or sent to clients.
- `Credentials` is an interface with a private marker method. The constructor
  accepts only `APIKeyCredentials` and `OAuthCredentials` values, rejecting nil,
  pointers and wrappers. This keeps one variant without mixing optional fields.
- ID, name and upstream ID must be nonblank. Each credential variant also
  requires a nonblank API key or OAuth access token.
  Validation preserves supplied values and returns field-specific descriptions
  without their contents. An error returns a zero account. ID generation,
  uniqueness and checking that the upstream exists belong to later boundaries.
- OAuth refresh tokens are optional: renewal may be handled externally.
  `ExpiresAt` is a `time.Time`; zero means unknown expiry. Expired records are
  valid data. Deciding whether they can serve a request belongs to token preparation.
- OAuth `ProviderData` is an optional `map[string]string` for provider-specific
  credential data. The account copies it on construction and on every read.
  Callers must not change it during construction; later changes are independent.
- `account.ID` and `upstream.ID` are the shared identifier types. Selection uses
  them directly and receives only candidate IDs, never credentials.

The model performs no I/O and needs no synchronization: its public API cannot
change stored data. Coordinating replacement snapshots during renewal is separate
work, described below.

## OAuth renewal timing

Before each upstream attempt, check whether the OAuth token needs renewal.
Reuse it when still fresh enough. API-key accounts need no renewal.

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
Test this rule and the same selection and access behavior for both authorization
methods, without exposing credentials to selection.

Renewal checks should cover fresh-token reuse, renewal before dispatch when due,
concurrent requests sharing an update, and independent progress for other accounts.

Define OAuth connection/import flows, expiry margins, handling of unknown expiry,
shared-refresh cancellation, persistence failures and recovery after `401` with
the account module.
Availability transitions and concurrent state updates also belong there. Storage
requirements are defined in [ADR 0007](0007-support-sqlite-and-postgresql.md#secret-storage).
Two account records may still share the same upstream quota.
