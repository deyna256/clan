# Admission and accounting research

Research for issues #22 and #23, based on CLAN `dde51a3`.
The agreed behavior is in [ADR 0008](../decisions/0008-enforce-token-budgets-at-admission.md#coordinator-contract).
The provider adapter remains deferred.

## Competitor findings

Source review on 2026-09-11; links pin the inspected commits. These findings cover
the linked paths, not every deployment or plugin. No competitor runtime experiment
was performed. Earlier storage research is in [accounting storage](accounting-storage.md).

| Project | Admission and accounting | Lesson for CLAN |
|---|---|---|
| Bifrost | Checks local counters before forwarding; charges observed usage; saves counters periodically | Closest fit for one process; keep its counting and failure policies distinct from ours |
| CLIProxyAPI | Client-key authentication, upstream account quotas and asynchronous usage publication | Provider cooldowns and telemetry do not implement client token budgets |
| LiteLLM | Cached spend, optional Redis, estimated-cost reservations and separate SQL writes | Useful failure-handling examples; distributed counters and reservations exceed our scope |

**Bifrost.** With SQL configured, failed initial loading returns an error from the
governance-store constructor. It does not substitute zero budgets. This loads all
governance data at startup; CLAN restores each key before its first admission.
The local tracker runs every ten seconds and logs dump failures. Its inspected
budget check has no save-failure latch. Request counts are charged for successful
completions, whereas CLAN consumes RPM at admission, including work that later fails.
[Initialization and loading](https://github.com/maximhq/bifrost/blob/0145f674ec5f61b87f05c69940a8e1cbaa086dbc/plugins/governance/store.go#L299-L324),
[budget check](https://github.com/maximhq/bifrost/blob/0145f674ec5f61b87f05c69940a8e1cbaa086dbc/plugins/governance/store.go#L2115-L2156),
[tracker](https://github.com/maximhq/bifrost/blob/0145f674ec5f61b87f05c69940a8e1cbaa086dbc/plugins/governance/tracker.go#L80-L229).

**CLIProxyAPI.** Usage publication queues records without acknowledging persistence.
It is unsuitable as the authority for admission. A concrete stream error path can
publish empty failure usage before deferred buffered usage; a shared `sync.Once`
then suppresses the latter. CLAN must account received usage independently of the
final outcome. Its cumulative attempt state already avoids duplicate charging;
no separate terminal-event deduplication registry is needed.
[Usage queue](https://github.com/router-for-me/CLIProxyAPI/blob/09a29bd345bc44c473abe7fd07859e32df2ea543/sdk/cliproxy/usage/manager.go#L340-L395),
[stream error path](https://github.com/router-for-me/CLIProxyAPI/blob/09a29bd345bc44c473abe7fd07859e32df2ea543/internal/runtime/executor/openai_compat_executor.go#L425-L570),
[failure publication](https://github.com/router-for-me/CLIProxyAPI/blob/09a29bd345bc44c473abe7fd07859e32df2ea543/internal/runtime/executor/helps/usage_helpers.go#L359-L386).

**LiteLLM.** Database-outage access and strict budget verification are separate
policies. `allow_requests_on_db_unavailable` defaults to false; enabling it permits
fallback for recognized database outages. `fail_closed_budget_enforcement` adds
stricter budget verification. Neither is equivalent to CLAN's initial restoration
followed by authoritative local counters. Avoid importing its Redis/cache fallback
chain just to keep one process running without a known initial state.
[Database failure policy](https://github.com/BerriAI/litellm/blob/362033cb2a703e591184a4ec5887a42ec7114873/litellm/proxy/db/exception_handler.py),
[budget settings](https://docs.litellm.ai/docs/proxy/users#budget-reservation).

Its SQL writer keeps uncommitted work for retry, including restoration to Redis
when that buffer is used. We need the retention rule, not the distributed buffer
or writer election. Its limiter can charge RPM before acquiring concurrency;
CLAN's no-RPM-on-rejection contract requires our own ordering. Cancellation
reconciliation can estimate input cost when final usage is missing; CLAN records
only known tokens.
[SQL writer](https://github.com/BerriAI/litellm/blob/362033cb2a703e591184a4ec5887a42ec7114873/litellm/proxy/db/db_spend_update_writer.py#L1072-L1249),
[limiter ordering](https://github.com/BerriAI/litellm/blob/362033cb2a703e591184a4ec5887a42ec7114873/litellm/proxy/hooks/parallel_request_limiter_v3.py#L1250-L1370),
[cancellation](https://github.com/BerriAI/litellm/blob/362033cb2a703e591184a4ec5887a42ec7114873/litellm/proxy/spend_tracking/budget_reservation.py#L351-L380).

There is no universal rule to allow or reject traffic when a limiter is unavailable.
For example, Envoy explicitly configures this through `failure_mode_deny` and bounds
the limiter RPC with a timeout. CLAN's accepted initial-load rule is a deliberate
trade-off: temporary unavailability rather than silently resetting usage.
[Envoy failure policy](https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/http/ratelimit/v3/rate_limit.proto).

## Keep the implementation small

- Reuse the pure usage/budget transitions and existing limiters. One coordinator
  owns the combined decision; no public transaction or callback protocol.
- Keep latest budget state, a pending-save flag and a save-failure flag per loaded
  key. Attempt handles own cumulative usage; no event queue or global attempt map.
- Acquire RPM last, after all fallible checks and a provisional slot. Publishing
  the prepared opening then cannot fail, so RPM needs no reservation or refund.
- Retry failed snapshots on the next save cycle. Use absolute replacement so a
  lost acknowledgement cannot add tokens twice. Keep changes received during I/O.
- Stop and join the worker before a bounded final save. A five-second cadence is
  an initial choice, not a hard bound on persistence lag during slow or failed I/O.

Direct SQL admission would couple request latency and availability to every write.
Distributed reservations would add reconciliation and recovery states. Neither is
needed for the accepted single-process, observed-usage policy. Crash loss since
the last successful snapshot and overruns from already admitted work remain accepted.

## Meaningful tests

Use public operations with real permission/limit primitives and a controlled
snapshot store for read/write failure, blocked saving and acknowledgement loss.
Cover rejection side effects, shared slots/RPM across retries, concurrent attempts,
cumulative usage across expiry, isolation, load recovery, updates during saving,
failure recovery while new usage arrives, and shutdown cleanup.
Include usage followed by a stream failure, and a failed initial load followed by
successful restoration of nonzero saved counters, with and without token caps.

Use standard [testing/synctest](https://pkg.go.dev/testing/synctest) for timers and
concurrency instead of sleeps or a custom clock framework. Real database wiring
is a separate integration test on SQLite and PostgreSQL, outside synctest.
