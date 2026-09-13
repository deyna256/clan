# ADR 0014: Use chi for HTTP routing

Status: Accepted. Reviewed: 2026-09-12.

## Context

Generation and management need separate routes and authentication. Route groups
help organize these rules while retaining standard Go HTTP handlers.

## Decision

Use `github.com/go-chi/chi/v5` on top of `net/http.Server`. Keep handlers and
middleware compatible with `net/http`; execution and storage do not depend on
the router.

For management only, use Huma on chi to validate typed requests and generate
OpenAPI from the same Go declarations. Serve one authenticated JSON specification.
Keep Huma types out of execution, OAuth and storage; client Responses routes
retain their own protocol contract.

## Alternatives and consequences

`http.ServeMux` is viable; chi adds convenient route groups and middleware
composition. Gin's separate handler conventions are unnecessary here.
See the [chi interface](https://github.com/go-chi/chi#router-interface).

Huma avoids separate handwritten validation and schema definitions. It adds
handler conventions at the management boundary; it does not replace service
interfaces. See [Huma OpenAPI generation](https://huma.rocks/features/openapi-generation/).

Choose middleware for actual requirements and test JSON/SSE delivery and
cancellation through the HTTP stack.
