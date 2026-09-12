# ADR 0001: Use Go

Status: Accepted. Reviewed: 2026-09-12.

## Context

CLAN handles concurrent HTTP requests, streaming, cancellation and shared account
state. It needs explicit error handling and reliable resource cleanup.

## Decision

Use Go, with one module and an entry point under `cmd/clan`. Prefer the standard
library and existing dependencies before adding packages.

## Consequences

Go's [HTTP library](https://pkg.go.dev/net/http) and
[context API](https://pkg.go.dev/context) fit this workload. Concurrency and cleanup
still need explicit ownership and tests.

Python is also viable, but uses a different runtime and concurrency model.
This decision does not assume Go is faster for every workload.
