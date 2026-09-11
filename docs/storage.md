# Budget snapshot storage

## Open and close

`internal/storage.Open(ctx, Config{Backend: ..., DSN: ...})` returns a concrete
store and applies embedded SQL migrations with Goose's instance provider.
The caller closes it. `Backend` accepts `sqlite` (also the empty default) or
`postgres`. SQLite's required `DSN` is a filesystem path without NUL bytes,
including paths with spaces or URI punctuation; PostgreSQL's is a URI or keyword
connection string.
An invalid explicit selection fails without switching databases. Application
environment loading and the default SQLite location belong to application startup.

## Load and save

`Load(ctx, accesskey.ID)` returns `(budget.State, found, error)`. A missing row
has `found=false` and no error; a saved zero state has `found=true`. Failed or
invalid reads return an error and no state. `Save(ctx, id, state)` replaces both
windows with one upsert in a transaction, without using the current time or
touching key settings. A failed or cancelled upsert never sends a commit; a commit
error can still leave the caller unsure whether the snapshot was saved.
Callers serialize saves for each key. Different keys and reads may overlap.
After an unconfirmed write, retry the pending snapshot; repeated replacement
does not add usage. The accounting coordinator owns save order and pending state.

Opening times use canonical UTC RFC3339Nano text, preserving nanoseconds in years
0000 through 9999 UTC. SQL NULL means unopened; the Unix epoch is an ordinary
opening. Round trips preserve `time.Time.Equal`, not the original location or
monotonic clock metadata. Counts are signed 64-bit nonnegative integers; unopened
windows must have zero usage. IDs must be nonblank UTF-8 without NUL and are
otherwise preserved exactly. Save and Load validate these storage boundaries.

## Database settings

SQLite uses one pooled connection, WAL and `synchronous=FULL`; the driver applies
those settings and a five-second busy timeout to each replacement connection.
This is a single-process installation on either backend. Goose handles versioned,
transactional migrations; no migration locks for overlapping application instances
are provided. SQL and driver failures are translated to operation-level errors
without connection strings or server-provided contents. Cancellation and deadlines
remain recognizable through `errors.Is`.

The dependencies are `database/sql`, [pgx's standard SQL driver](https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib),
[modernc SQLite](https://pkg.go.dev/modernc.org/sqlite) (no C toolchain for builds),
and [Goose's provider](https://pressly.github.io/goose/documentation/provider/).
Queries are handwritten. Goose's library supplies migration parsing and version
tracking without importing its CLI database-driver bundle; versions are pinned
in `go.mod`.

## Run the database tests

`just test` runs the same integration behavior on temporary file-backed SQLite
and real PostgreSQL. Set `CLAN_TEST_POSTGRES_DSN` to a test database where the user
can create schemas. Each fixture creates and removes its own uniquely named
schema; existing tables are untouched. Missing PostgreSQL configuration or an
unreachable database fails the full suite. `just test-unit` uses `-short` and skips
database integration tests.

For example, start a disposable local PostgreSQL instance:

```sh
docker run --rm --name clan-storage-test \
  -e POSTGRES_USER=clan -e POSTGRES_PASSWORD=clan-test \
  -e POSTGRES_DB=clan_test -p 127.0.0.1:55432:5432 -d postgres:18.6
docker exec clan-storage-test pg_isready -U clan -d clan_test
export CLAN_TEST_POSTGRES_DSN='postgres://clan:clan-test@127.0.0.1:55432/clan_test?sslmode=disable'
just test
docker stop clan-storage-test
```

Wait for `pg_isready` to report accepting connections before running tests.
The example credentials belong only to this disposable local test instance.
CI provisions the same PostgreSQL version as a service and sets the test DSN.
