# Running CLAN

Run one gateway process per database.

## Configuration

| Environment variable | From source | Docker Compose |
|---|---|---|
| `CLAN_LISTEN_ADDR` | `127.0.0.1:8080` | `0.0.0.0:8080`, published on host `127.0.0.1:8080` |
| `CLAN_DB_PATH` | `./clan.db` | `/data/clan.db` in the `clan_data` volume |
| `CLAN_ADMIN_TOKEN` | Required separate bearer token | Same, from `.env` |
| `CLAN_ENCRYPTION_KEY` | Required standard Base64 encoding of 32 random bytes | Same, from `.env` |
| `CLAN_OAUTH_CALLBACK_ADDR` | `127.0.0.1:1455` | `0.0.0.0:1455`, published on host `127.0.0.1:1455` |
| `CLAN_USAGE_RETENTION` | `2160h` (90 days) | Same; override in `.env` |

Generate the secrets once, for example with `openssl rand -hex 32` for the admin
token and `openssl rand -base64 32` for the encryption key. Store them privately.
Keep the same encryption key across restarts; a different key cannot open existing
credentials.

`CLAN_USAGE_RETENTION` accepts a positive Go duration: use `720h` for 30 days,
not `30d`. Invalid, zero or negative values prevent startup. CLAN deletes expired
request records hourly.

## Start with Docker

Docker Compose runs one container and keeps its database in the `clan_data` volume.
Copy the example environment file, fill in both secrets, then build and start:

```sh
cp .env.example .env && chmod 600 .env
just build
just run
```

The container runs as a non-root user without a shell and publishes both ports on
loopback only. It restarts automatically, including after a reboot, until
`just down`. Logs are JSON: `docker compose logs -f`.

| Command | Effect |
|---|---|
| `just build` | Build the `clan:local` image from the current source |
| `just run` | Start the built image in the background |
| `just down` | Stop and remove the container; keep the `clan_data` volume |
| `just clean` | Remove the container, image and `clan_data` volume after confirmation |

Back up the `clan_data` volume together with `.env`. Stop CLAN with `just down`
before copying the volume; see [backup and upgrades](#backup-and-upgrades).

## Start from source

Use the Go version in `go.mod` and export the variables above.

```sh
mkdir -p .local
go build -o .local/clan ./cmd/clan
.local/clan
```

SQLite is created on startup; its parent directory must exist and be on a local
file system. Network file systems are unsupported. Keep the database and
encryption key in your backups. Logs are JSON on stderr. Defaults bind only to
loopback. For remote access, use a trusted TLS reverse proxy.

## Backup and upgrades

Back up before upgrading. CLAN upgrades the database automatically at startup,
preserving accounts, keys and encrypted credentials. Keep the same encryption
key. Unsupported schema versions and invalid migration history prevent startup.

SQLite uses WAL mode, with `clan.db-wal` and `clan.db-shm` alongside `clan.db`.
Copying only `clan.db` while CLAN runs can lose committed data. Stop CLAN before
copying its database directory, or run SQLite's backup command on the database host:

```sh
sqlite3 clan.db ".backup backup.db"
```

Keep the backup and encryption key private. CLAN 0.1.x cannot open an upgraded
database. To revert, stop CLAN and restore a pre-upgrade backup; this discards
changes made since the backup. There are no down migrations.

## Connect an account

In another shell with the admin token available (for Docker, run
`set -a; . ./.env; set +a` in the project directory):

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
current OAuth jobs save credentials. Shutdown waits for request recording and
stops retention before closing SQLite. It has a 30-second limit; failure to
finish exits with an error. A forced exit during provider token rotation can
require signing in again. Restart with the same database and encryption key.

With Docker, `just down` sends SIGTERM and allows 40 seconds before forcing a stop.
`just run` starts again with the same volume.
