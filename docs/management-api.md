# Management API

The `internal/management` handler serves HTTP JSON under `/api`. Application
wiring is still pending; the entry point does not start this API yet.
The generated contract is available at `GET /api/openapi.json` when mounted.

## Authentication

Every management request, including the schema, requires
`Authorization: Bearer <admin_token>`. Use a separate random token; CLAN client
keys cannot authorize management. Missing or invalid tokens return 401 before
the request body is parsed. Startup must reject missing admin credentials.

There is no cookie login, documentation UI or CORS configuration. Browser panels
need the same origin, directly or through a reverse proxy. The separate OAuth
callback uses state and PKCE checks instead of the admin token.

## Routes

| Method | Path | Result |
|---|---|---|
| POST | `/api/oauth/login` | Start connecting a named account |
| GET | `/api/oauth/login` | Read the latest login state |
| POST | `/api/oauth/login/cancel` | Cancel the specified login attempt |
| GET | `/api/accounts` | List account statuses |
| GET | `/api/accounts/{id}` | Read one account status |
| POST | `/api/accounts/{id}/reconnect` | Start a new login for an enabled account |
| POST | `/api/accounts/{id}/disable` | Disable and cancel active work |
| POST | `/api/accounts/{id}/enable` | Allow use of the stored credentials again |
| DELETE | `/api/accounts/{id}` | Delete credentials and cancel active work |
| POST | `/api/client-keys` | Create a client key; return its secret once |
| GET | `/api/client-keys` | List key metadata |
| PATCH | `/api/client-keys/{id}` | Set the concurrency limit |
| POST | `/api/client-keys/{id}/revoke` | Revoke the key and cancel its active work |
| GET | `/api/models` | List the shared visible model catalog |

Creation returns 201 for a key and 200 for OAuth instructions. Reads return 200;
other successful operations return 204. Paths use CLAN IDs, never secret keys.

## Client keys

Creation requires a nonblank `name` and an integer `concurrency_limit`.
JSON field names are case-sensitive. Unknown and repeated fields are rejected.
The limit has no default: -1 is unlimited, 0 blocks new requests, and positive
values cap concurrency. Missing, null and values below -1 are rejected.

The creation response contains `id`, `name`, `enabled`, `concurrency_limit` and
`key`. Save `key`: subsequent responses contain neither the secret nor its hash.
PATCH accepts only `concurrency_limit`. Lowering it leaves active requests running;
changing it does not restore a revoked key.

## Accounts and login

Account responses contain `id`, `name`, `state`, and applicable `expires_at` and
`retry_at` dates. States are `connected`, `refreshing`, `temporarily_unavailable`,
`needs_sign_in` and `disabled`. They describe credentials, not a guarantee that
the provider will accept a generation. Dates use RFC 3339 UTC; unknown dates are
omitted. `expires_at` is token expiry; `retry_at` is the next permitted refresh.

POST `/api/oauth/login` takes a nonblank `name`. Start and reconnect return
`login_id`, `account_id`, `authorization_url` and `expires_at` (the login deadline).
Open the URL in a browser and poll GET `/api/oauth/login`. It returns the same
IDs and deadline with `state`, but no authorization URL or OAuth secrets.
Before the first login it returns only `{"state":"idle"}`.

Only one attempt may be pending. A second start returns 409 without replacing it.
The states are `waiting`, `exchanging`, `succeeded`, `failed`, `canceled` and
`expired`. The latest result stays in memory until another attempt starts or CLAN
restarts. Compare `login_id` when polling; a different ID belongs to a newer login.

Cancel takes `{"login_id":"..."}`. An old or unknown ID returns 409 and cannot
cancel a newer attempt. Canceling a matching terminal attempt is a 204 no-op:
cancellation cannot undo a completed credential save. Read status to distinguish
`succeeded` from `canceled`. Account IDs cannot identify attempts because reconnect
uses the same account ID with a new login ID.

Enable keeps the account ID and credentials. It does not restart canceled work,
refresh tokens immediately or bypass a required sign-in. To reconnect a disabled
account, enable it first. For a remote installation, forward the callback as
described in [account setup](architecture.md#management-and-account-setup).

## Lists

Accounts, client keys and management models use the same envelope:

```json
{"items":[],"total":0,"limit":50,"offset":0}
```

`limit` defaults to 50 and accepts 1–100. `offset` defaults to 0 and must be
nonnegative. `total` is the exact count after filtering and before pagination,
from the same selected list as `items`. Empty and past-end pages return 200.

| List | Filters |
|---|---|
| Accounts | `q` searches name/ID; `state` selects one account state |
| Client keys | `q` searches name/ID; `enabled` selects true or false |
| Models | `q` searches ID/name |

Text search matches literal substrings without case sensitivity. Filters combine
with AND. Unknown parameters and invalid values are errors. Results are ordered
by ID. Each page reads current data; additions or removals between requests can
shift offsets and repeat or skip entries.

Model items contain only `id` and `name`. This management format does not change
the OpenAI-compatible client model endpoint.

## Errors and completion

Errors use RFC 9457 `application/problem+json`: `status`, `title`, safe `detail`
and a stable `type` identifier. Validation errors may include `errors` entries
with `location` and `message`. Branch on status/type, never the English text.
Problem type URIs identify errors; they are not extra management operations.

| Status | Meaning |
|---|---|
| 400 | The request cannot be parsed |
| 401 | Missing or invalid admin token |
| 404 | Resource not found |
| 405 | Method not supported; `Allow` lists supported methods |
| 408 | Request body read timed out |
| 409 | State conflict |
| 413 | Body too large |
| 415 | Content type is not `application/json` |
| 422 | Input fails validation |
| 500 | Internal failure |
| 503 | Service unavailable or mutation completion not confirmed |

JSON bodies are limited to 64 KiB with a five-second read timeout. Operations
have a 30-second deadline after parsing.

Revocation, disabling and deletion return 204 only after affected generation
requests release their resources. A cleanup timeout returns 503 with type
`/api/problems/completion-unconfirmed`; the
stored state may already have changed. Timeout or client disconnection does not
roll back a saved change or stop cleanup. Reading disabled/revoked state does not
prove that cleanup has finished. There is no background job or operation registry.

## Request examples

These examples assume a mounted API and shell variables `CLAN_URL` and
`ADMIN_TOKEN`. Keep the token and returned credentials private.

```sh
curl "$CLAN_URL/api/oauth/login" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Primary"}'

curl "$CLAN_URL/api/client-keys" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"OpenCode","concurrency_limit":3}'

curl "$CLAN_URL/api/client-keys?enabled=true&limit=20&offset=0" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```
