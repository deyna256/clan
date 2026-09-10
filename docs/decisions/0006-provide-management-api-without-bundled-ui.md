# ADR 0006: Provide a management API without a bundled web panel

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer.
Implementation: [access-key permissions](../../internal/accesskey/access_key.go)
implemented; management HTTP API not started.

## Decision

Provide an HTTP JSON management API with resources for upstreams, accounts, access
keys, limits and consumption. Deliver the web panel from a separate repository as
an API client. Management must also work without the panel; authorization, validation
and business rules belong in the backend.

## Context and alternatives

Configuration files alone do not meet the requirement for interactive management.
A bundled panel requires shipping the backend and frontend together. A separate panel uses the
same API as scripts and other clients, with no additional backend service required.

## Management route prefix

Use `/api` without a version segment. Inference retains its protocol-specific paths,
including `/v1/...`. No alternative version-selection mechanism is selected.
Rules for compatibility between panel and backend releases are still open.

## Resources

| Resource | Responsibility |
|---|---|
| `upstreams` | Configured service: integration type, base URL and shared connection settings |
| `accounts` | Credentials and availability for exactly one upstream; an upstream can have many accounts |
| `access-keys` | ID, name, enabled setting, permissions and configured limits |
| `models` | Read-only catalog: concrete model name, upstream and known capabilities |
| `requests` | Read-only request list and details, including upstream attempts |
| `usage` | Read-only consumption summaries by period, access key, model, upstream or account |

All resource paths are relative to `/api`. Configure an upstream before adding its
accounts. Upstreams are destinations, not named account pools. Account credential
variants are defined in [ADR 0005](0005-encapsulate-credential-types-in-account.md).

Call credentials issued to applications and people **access keys** (Russian:
**ключи доступа**), with Go name `AccessKey`. They are distinct from the admin token
and upstream credentials. Permissions restrict upstreams, models and accounts.
RPM, burst, concurrency and fixed 5-hour/7-day token limits are settings of the
access key, not independent policy resources. Observed consumption cannot be edited
as settings.
Enforcement is defined in [ADR 0008](0008-enforce-token-budgets-at-admission.md),
[ADR 0010](0010-limit-concurrent-client-requests.md) and
[ADR 0015](0015-use-token-bucket-rate-limits.md).

History and reporting follow [ADR 0009](0009-record-request-and-attempt-history.md).
Keep distinct catalog entries when the same model name exists on different upstreams.
A listed model may have no account available to serve it at that moment.

## Access-key permission rules

Agreed on 2026-09-10: deny access unless it is explicitly permitted. Upstream,
model and account restrictions must all allow the target. A disabled key denies
every target.

| Restriction | Meaning |
|---|---|
| Absent or empty list | Allow nothing in that dimension |
| Nonempty list | Allow only the listed values |
| Explicit all setting | Allow every value in that dimension |

Identify a model by its upstream ID and exact name. The same name on another
upstream is a different target. Match IDs and model names exactly; do not treat
strings as wildcard patterns. An account must belong to the requested upstream,
even when all accounts are permitted.

The Go model uses `accesskey.ID`, `Identity`, `Permissions` and `AccessKey`.
`Permissions` has an all setting and a list for each dimension. Combining all
with a nonempty list is invalid. IDs, names and list entries must be nonblank;
validation preserves supplied values. Checking that referenced resources exist
belongs to the configuration/storage boundary. Lists act as sets, so repeated
entries do not grant additional access.

Construct an immutable key snapshot with `accesskey.New`. It copies permission
lists; later edits to the input cannot change access. The zero key denies all
access. Changing settings requires a new snapshot.

`Allows` checks one target and account identity. `Filter` returns allowed account
IDs in candidate order, without changing or retaining the candidate slice.
Candidates must come from trusted account inventory, not client-supplied account
metadata. These operations inspect no credentials and perform no I/O. They do not
prove that the presented access-key secret is valid or that a model is supported
or an account is currently available.

## Model discovery for clients

Client-facing model listings are separate from the admin catalog and respect the
caller's access key. Avoid manually maintained model lists where client discovery
allows it. OpenCode, Codex and Claude Code are required clients for the first version;
their discovery behavior and supported models need not be identical.

Validate model selection and an actual inference request in each supported client
version, including access restrictions. Define discovery routes, how to collect model
details, how to distinguish identical names across upstreams, and how to support
clients without suitable discovery in the model module.

## Resource creation

Create resources with `POST` to their collection. This does not provide duplicate
protection after a lost response. How IDs are assigned, repeated-POST behavior and
successful response schemas remain to be agreed.

## Resource updates

Use `PATCH` for settings of `upstreams`, `accounts` and `access-keys`, without a
parallel `PUT`. Accept `Content-Type: application/merge-patch+json` under
[RFC 7396](https://www.rfc-editor.org/rfc/rfc7396.html).

| Patch content | Meaning |
|---|---|
| Omitted field | Preserve the existing value |
| Scalar, including `false` or `0` | Set the value if permitted by the schema |
| Nested object | Apply merge-patch rules recursively |
| Array, including an empty array | Replace the entire array |
| `null` | Remove the field if the schema permits its absence |

Validate the resulting resource. Removing a required field is an error. Resource
schemas must preserve the permission rules above and define optional limit fields;
the patch format alone does not decide their meaning.

## Concurrent configuration updates

Return an ETag that identifies the version of editable configuration. Clients send
it in `If-Match` when changing settings with PATCH. Check the ETag and apply the update
atomically, so another update cannot occur between the check and the write.
An outdated ETag returns HTTP 412 without changing configuration. Reject a missing
`If-Match`. The panel shows the conflict and lets the administrator reload; it must
not silently retry the overwrite with a new ETag.

This follows [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html#section-13.1.1).
How to generate ETags, which configuration fields they cover, and the missing-header error
remain open. This decision does not set preconditions for DELETE or other mutations.

## Management error responses

Use [Problem Details, RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html) with
`Content-Type: application/problem+json`: a stable `type` URI, explanatory `title`
and `detail`, and `status` matching the HTTP response. Include `request_id` to find
the matching server logs. Validation errors include an extension identifying invalid fields and
problems; exact identifiers, status mappings and extension schemas remain open.
Inference errors retain their OpenAI- or Anthropic-compatible representation.

## Management list pagination

Use cursor pagination for management lists: `limit`, optional `cursor`, and a response
with `items` and `next_cursor` (`null` at the end). A cursor marks where to continue;
clients pass it back unchanged without interpreting it. Use stable ordering and a
unique tie-breaker, such as ID, when sort values are equal.

Do not require a `total` field or total-count query. Page-number navigation is outside
this contract; filters help find records. Data may change between page requests;
cursor pagination does not freeze it. Page sizes, ordering, filters, cursor validation and behavior
under concurrent changes remain open. Inference model lists and aggregate usage
reports retain their own response formats.

## Administrator access

Use a separate installation-level admin token, supplied at startup through
`CLAN_ADMIN_TOKEN` and accepted only as `Authorization: Bearer <admin-token>`.
Access keys cannot authorize management; the admin token cannot authorize inference.
A shared admin token grants access without identifying an individual administrator.
Individual admin accounts and login sessions are outside the initial scope.

Rotate or recover the token by changing the environment value and restarting CLAN.
The old token then stops working. No API-based token management or live rotation is
required. Never expose management unauthenticated when the token is missing or invalid;
exact startup behavior and how the token is held and checked in memory remain open.

## Browser access

Allow cross-origin management access only for an explicit origin allowlist, empty
by default. The separate panel can also share an origin through a reverse proxy.
Support preflight and the required `Authorization`, `Content-Type` and `If-Match`
headers; expose `ETag` to the panel.

Preflight requires no admin token and grants no management operation. Actual
operations require admin authentication; CORS does not replace it. Configuration
names and preflight details remain open. This policy does not define inference CORS.

## OpenAPI source and publication

Describe HTTP types and operations in Go and generate OpenAPI from that description.
Go is the source of truth; keep control of the HTTP implementation in handwritten Go
rather than generating a server from OpenAPI. Publish the generated specification
through a dedicated endpoint for the panel and other consumers. It must describe
the running release without exposing runtime secrets.

Review each module's contract before implementing its behavior. Types for business
rules and storage must not depend on the API-description tool. The library,
OpenAPI version, generation at build time or startup, and publication path/access
policy will be selected with the management module.

## Consequences and validation

Consumers get a machine-readable contract without a separately maintained YAML copy.
Go operation metadata still needs maintenance, and generation cannot prove runtime
behavior. Check the published schema against HTTP responses and test authorization,
Merge Patch semantics, atomic concurrent updates, errors, pagination and browser access.
Verify that credentials do not appear in the published specification.

## Remaining contract decisions

Agree on successful responses (status, body and headers), ID assignment and duplicate
creation, how clients submit secrets and responses hide them, read-only fields, deletion and references,
and compatibility between backend and panel releases. Define exact resource fields,
filters and the other details identified above with their owning modules.
