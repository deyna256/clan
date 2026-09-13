# Running CLAN

Use the Go version in `go.mod`. Run one process per database. Container packaging
and full OpenCode/Python acceptance checks are still pending.

## Configure and start

| Environment variable | Default |
|---|---|
| `CLAN_LISTEN_ADDR` | `127.0.0.1:8080` |
| `CLAN_DB_PATH` | `./clan.db` |
| `CLAN_ADMIN_TOKEN` | Required separate bearer token |
| `CLAN_ENCRYPTION_KEY` | Required standard Base64 encoding of 32 random bytes |
| `CLAN_OAUTH_CALLBACK_ADDR` | `127.0.0.1:1455` |

Generate the secrets once, for example with `openssl rand -hex 32` for the admin
token and `openssl rand -base64 32` for the encryption key. Store them privately
and export them as the variables above. Keep the same encryption key across
restarts; a different key cannot open existing credentials.

```sh
mkdir -p .local
go build -o .local/clan ./cmd/clan
.local/clan
```

SQLite is created on startup; its parent directory must exist. Keep the database
and encryption key in your backups. Logs are JSON on stderr. Defaults bind only
to loopback. For remote access, use a trusted TLS reverse proxy.

## Connect an account

In another shell with the admin token available:

```sh
export CLAN_URL=http://127.0.0.1:8080

curl "$CLAN_URL/api/oauth/login" \
  -H "Authorization: Bearer $CLAN_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Primary"}'
```

Open the returned `authorization_url` in your browser. After sign-in, check:

```sh
curl "$CLAN_URL/api/oauth/login" \
  -H "Authorization: Bearer $CLAN_ADMIN_TOKEN"
```

Wait for `state: "succeeded"`. The redirect is always
`http://localhost:1455/auth/callback`; changing the callback bind address does not
change it. For a remote server, forward local port 1455 to the server before
opening the sign-in URL. Keep the callback reachable only by the administrator.

## Create a client key and generate

```sh
curl "$CLAN_URL/api/client-keys" \
  -H "Authorization: Bearer $CLAN_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Local client","concurrency_limit":3}'
```

Save the returned `key` privately as `CLAN_API_KEY`; it is shown only once. Use it
to discover model IDs:

```sh
curl "$CLAN_URL/v1/models" -H "Authorization: Bearer $CLAN_API_KEY"
```

Replace `MODEL_ID` with an ID from that list:

```sh
curl "$CLAN_URL/v1/responses" \
  -H "Authorization: Bearer $CLAN_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"MODEL_ID","input":"Say hello.","store":false}'
```

For streaming, add `"stream":true` to the JSON and `-N` to curl. Check the terminal
status: HTTP 200 alone does not mean generation completed successfully.
See [client setup](client-contract.md#opencode) and the
[management reference](management-api.md) for further operations.

## Stop and restart

Send SIGINT or SIGTERM. CLAN cancels generations, waits for cleanup and lets
current OAuth jobs save credentials. Shutdown has a 30-second limit; failure to
finish exits with an error. A forced exit during provider token rotation can
require signing in again. Restart with the same database and encryption key.
