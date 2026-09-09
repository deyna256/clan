# ADR 0014: Use chi for HTTP routing

Status: Accepted. Recorded: 2026-09-09.
Decision owner: project maintainer. Implementation: not started.

## Decision

Use `github.com/go-chi/chi/v5` for routing on top of `net/http.Server`.
Keep HTTP handlers and middleware compatible with `net/http`. Use chi to register
routes and apply middleware to groups; keep request execution, account selection
and storage independent of the router.

## Context and alternatives

Management and inference need separate authentication and error formats.
Management also needs CORS with unauthenticated preflight. Chi provides route
groups, middleware composition and custom 404/405 handlers, reducing the code
needed to organize these rules.

Standard `http.ServeMux` supports methods and path parameters and remains a viable
alternative. Chi adds convenient group-level configuration while retaining
standard handlers. Gin is not selected; its handler context and additional
framework conventions are unnecessary for our HTTP layer.

See the [chi routing interface](https://github.com/go-chi/chi#router-interface)
and [ServeMux documentation](https://pkg.go.dev/net/http#ServeMux).

## Consequences

Add and pin chi when the first HTTP routes are implemented. Chi does not implement
our token budgets, retries, streaming lifecycle or timeout policies. Choose
middleware individually and check it against those contracts.

OpenAPI tooling remains a separate decision under
[ADR 0006](0006-provide-management-api-without-bundled-ui.md#openapi-source-and-publication).
Huma is a candidate, not a selected dependency.

## Validation

Test authentication boundaries, CORS preflight, error bodies, 404/405 responses
and the `Allow` header for 405. Verify path and method matching, including HEAD
and trailing slashes, rather than assuming chi behaves exactly like ServeMux.
Test streaming flush, cancellation and write deadlines through the actual
middleware stack when streaming is implemented.
