# ADR 0001: Use Go for CLAN

Status: Accepted. Recorded: 2026-09-08.
Decision owner: project maintainer. Implementation: module and empty entry point only.

## Decision

Implement the gateway in Go. Keep application code in one Go module initially, with
the executable entry point under `cmd/clan`. Dependency and build-tool
versions are separate decisions.

## Context

CLAN coordinates simultaneous HTTP requests, long-lived upstream streams, cancellation
and shared account availability. It needs limited retries,
consistent access restrictions and reliable resource cleanup.

## Alternatives

| Option | Assessment |
|---|---|
| Go | Selected: concurrency support, explicit errors and the standard HTTP library fit the gateway |
| Python | Also viable, with asynchronous I/O and many useful libraries; uses a different runtime and concurrency model |

This choice does not assume Go is faster for every workload.
See Go's [HTTP library](https://pkg.go.dev/net/http) and
[context API](https://pkg.go.dev/context) for the capabilities this choice relies on.

## Consequences

Use standard Go tooling and ordinary packages. Keep cancellation and resource ownership
explicit. The language does not prevent races, too many goroutines or incorrect retry
behavior; we still need to design and test these carefully.

Go also makes it practical to study the implementations of Bifrost and CLIProxyAPI.
It does not by itself make their packages suitable dependencies for CLAN.

## Validation and open details

The repository contains [go.mod](../../go.mod) and an empty
[entry point](../../cmd/clan/main.go). Gateway behavior is not implemented. Validate
concurrency, cancellation and cleanup as those modules are built. Revisit the language
only if a concrete requirement cannot reasonably be met in Go.

Related: [project principles](../../README.md), [decision log](../../README.md#decision-log).
