# Development guide

These are CLAN's coding rules. Product behavior is
defined in the [ADRs](../README.md#decision-log), and commands are in the
[Justfile](../Justfile). LLM instructions should link here instead of copying these rules.

## Packages and responsibility

Give each package a clear job and export only what callers need. Split files when
it helps navigation, and packages when responsibilities differ. File length alone
is not a reason to split. Avoid catch-all packages such as `utils` or `common`.

## Interfaces follow their consumers

Define small interfaces around the calling code's needs, usually in that code's
package. Constructors should normally return concrete types. Do not add an
interface for every struct or just to mock the code being tested. Reuse standard
interfaces such as `io.Reader` when they fit.

## Domain types and validation

Define a type when it prevents mistakes or gives values useful behavior.
Separate account and upstream ID types help catch accidental mixing at compile
time. A new type for every primitive value is unnecessary.

`type AccountID string` defines a distinct type; `type AccountID = string` is an
alias and does not distinguish account IDs from strings. Defined types still
allow explicit conversions and do not validate their contents. Validate external
input before treating it as trusted data.

## Initialization and nil

Initialize required dependencies and mutable data structures during construction.
Use `nil` where its meaning is clear: a nil error means success, and a nil slice
can represent an empty sequence. Do not allocate a slice just to avoid nil.

Check required dependencies in the constructor. Document when construction is
required: callers can still create the zero value of an exported struct.

## Generics

Use type parameters when actual uses share an algorithm across different types.
Prefer concrete types or small interfaces when they are simpler. Reuse suitable
functions from `slices` and `maps` before writing helpers.

## Ownership of mutable data

State whether a method reads, changes or keeps its arguments. Copy data when the
caller and callee need independent copies. Synchronize shared access, and document
any access to mutable internal state.

Cloning a slice or map is shallow: nested mutable data may still be shared.
Copying also needs safe access to the source; it does not fix concurrent writes.

## Construction and dependencies

Create dependencies explicitly and pass each component only what it needs.
Keep mutable application state out of package globals. Create shared objects once
at startup and pass them to callers; tests can create their own instances.

Use ordinary constructors. Avoid passing a whole application container to a
component that needs only a few dependencies. Keep environment reads, database
connections and worker startup out of `init()`. Constants and predefined errors
do not need dependency injection.

## Errors and failure ownership

Return errors for expected failures. Use a plain error for a description, a
sentinel error when callers need to recognize a specific failure, and a custom
error type when they need extra fields. Use `errors.Is` and `errors.As` instead
of comparing error messages.

Add useful context. Wrapping with `%w` makes the underlying error available to
callers, so wrap only when that is part of the contract. Translate errors at
module boundaries when callers should not depend on internal details. Keep
secrets out of errors and client responses.

Decide where errors are handled, returned and logged. Avoid logging the same
failure at every layer. Provider adapters classify failures, request execution
owns retries, and inbound adapters encode client errors; see
[ADR 0003](decisions/0003-separate-request-execution-from-protocols.md).

## Synchronization and invariants

Identify what must stay true when calls overlap. Protect related changes as one
operation: safe individual reads and writes do not make the whole sequence atomic.

Start with a private `sync.Mutex` around shared state. Never copy a mutex after
first use; use pointer receivers for objects that hold one. Keep network calls
and long waits outside the lock. Split locks only if measurements show a need.
`sync.Map` does not make a sequence of operations atomic.

## Goroutine lifetime

Start goroutines when work needs to run concurrently. Give each one an owner,
exit conditions, error handling and a way to wait for completion. Bound concurrent
work according to the task.

Cancellation asks work to stop; it does not terminate a goroutine or wait for
cleanup. `sync.WaitGroup` waits for completion but does not cancel work or collect
errors. Ensure blocking I/O and channel operations can finish when work is stopped.

Request work follows the request through cleanup. The application owns background
work and must stop and wait for it during shutdown.

## Context follows the operation

Pass context first to operations that need cancellation or deadlines, and carry
the request context through to I/O. Derive child contexts from it instead of
replacing it with `context.Background()`. Context values are for request
information, not dependencies, configuration or model parameters.

When creating a context with a cancel function, assign responsibility for calling
it when the operation ends. A stream's operation includes reading it: do not defer
cancellation in a function returning a stream that still needs that context.
Follow the
[stream contract](decisions/0003-separate-request-execution-from-protocols.md#stream-consumption-next-and-close).

Do not store a request context in a shared executor or application-wide object.
An object for one stream may hold its context, with ownership and lifetime
documented. Pure calculations need no context.

## Resource cleanup

Assign an owner to each resource and clean it up on every exit path. Code receiving
a resource normally owns cleanup unless it transfers that responsibility.
Use `defer` when function exit is the right time to release the resource.

A defer inside a loop runs at function exit, not after each iteration. Avoid
keeping resources open longer than needed. A function returning a live resource
must leave it usable and transfer cleanup to the caller.

Report cleanup errors that make the result unreliable. Preserve the original
failure if cleanup also fails. Repeated `Close` calls are not safe for every
resource; CLAN's stream guarantees this in
[ADR 0003](decisions/0003-separate-request-execution-from-protocols.md#stream-consumption-next-and-close).
Hold the concurrency slot through cleanup, as required by
[ADR 0010](decisions/0010-limit-concurrent-client-requests.md).

## Names, comments and control flow

Choose names that explain their purpose. Short names such as `i`, `ctx` and `err`
work when their meaning and scope are clear. Format with `gofmt`.

Give each function one clear job. Handle errors and boundary cases early, keeping
cleanup correct on every return. Avoid deep nesting, mixed boolean conditions
and variables that change meaning. Extract a helper when its name explains a
useful step, not to meet a line limit. Avoid splitting code just
to lower a [complexity score](https://www.sonarsource.com/resources/cognitive-complexity/).
A score can prompt review, but cannot replace it.

Prefer clear names and simple code over explanatory comments. Omit comments that
repeat the code, label obvious steps or describe unexported symbols whose purpose
is clear. Add a comment only when it explains a reason or constraint the code
cannot show.

Keep Go doc comments short: start with one sentence describing the exported
symbol. Add only what callers need, such as ownership, concurrency, zero-value
behavior and required initialization. Keep these guarantees even when they need
more than one sentence. Use doc links to related symbols instead of repeating
their contracts. Put design rationale and alternatives in ADRs, not source comments.

Update or remove comments when the related code changes. Avoid implementation
walkthroughs and plans for future work that can go stale.

## Tests exercise behavior

Test the real module through its caller-facing contract. Set expected results
independently; do not copy the production algorithm to calculate them. Tests
should remain useful when internal details change.

Use standard `testing`, tables for related cases and separate tests for different
scenarios. Failure messages should show the case, actual result and expected
result. Use simple test doubles when a dependency needs to return a controlled
response or failure.

Use `testify/require` for repeated equality and error checks. Call it only from
the test goroutine. JSON comparisons that need exact integers must use
`json.Decoder.UseNumber`; `require.JSONEq` decodes numbers through `float64`.

Use [Arrange, Act, Assert (AAA)](https://learn.microsoft.com/en-us/dotnet/core/testing/unit-testing-best-practices#arrange-your-tests):
prepare the state, run the operation, then check the results.
Separate the stages with blank lines and comments when helpful.
A stateful scenario may need several calls in Act. Name fields in test tables,
keep expected results visible, and avoid test helpers that hide the behavior.
For sequential steps, check each result next to its call when this is clearer.
Do not collect intermediate results just to put every assertion at the end.
In concurrent tests, collect results and check them after workers finish. Do not
call `t.Fatal` or setup helpers that can call it from worker goroutines. Do not
assume goroutine execution order.

Keep unit tests independent of external services. Test SQL, migrations and
transactions with real SQLite and PostgreSQL. `just test` runs all tests;
`just test-unit` adds `-short`. Integration tests skip when `testing.Short()` is
true; build tags do not separate the suites.

### Test data and helpers

Keep the values that explain a scenario visible in the test. Move repeated setup
into small functions with fixed defaults for unrelated fields. Keep short struct
literals when their fields are the point of the test. Local named values can
remove repetition within a table without adding a helper.

Use ordinary functions and explicit field assignments. Start with helpers in the
same `_test.go` file; share them across packages only when actual callers need it.
Avoid builder frameworks and random data for ordinary examples. Each call must
return independent mutable data, including nested slices and maps.
When testing that input is preserved, save an independent expected value before
the call. A shared map or slice can let a bug change both the input and expectation.

A setup helper that reports failures takes `*testing.T`, calls `t.Helper()` and
stops on a setup error. Pure data functions need no `testing.T`. Keep the operation
under test and its expected result in the test. When testing a constructor's
validation, call it directly instead of using a helper that requires success.

Use tables when cases share setup, execution and checks. Split scenarios when a
table needs switches or callbacks to run different behaviors. Do not remove
distinct cases just to shorten a file.

Sources: [Go test helpers](https://google.github.io/styleguide/go/decisions.html#test-helpers),
[table-driven tests](https://go.dev/wiki/TableDrivenTests), and
[sharing test data](https://abseil.io/resources/swe-book/html/ch12.html#sharing_code_tests_and_the_dry_principle).

## Race detection, properties and fuzzing

Use the race detector, as configured in the Justfile. It checks executed paths
for data races; it does not prove correct concurrent behavior. Also assert the
operation's guarantees.

Alongside specific examples, check rules that hold across inputs, such as a selected
account always belonging to the candidate set. Ordinary Go tests can check these properties.

Use fuzzing where generated inputs help test complex input handling, such as
JSON and stream parsers. Define useful properties and keep failing inputs as
regression cases. Ordinary `go test` runs saved seed cases; finding new inputs
needs a separate `-fuzz` run.

These techniques fit within unit and integration tests. Active fuzzing is outside
the default full run. Coverage alone does not show test quality.

## Tooling and dependencies

For now, `just lint` runs `go vet ./...`. When golangci-lint is added, start with
`govet`, `staticcheck`, `errcheck` and `unused`.
Add checks when their purpose is clear. Use `just format` to format with `gofmt`
and `just format --check` to check without changing files. Suppress a
finding only where needed, naming the linter and explaining the exception.
Ignoring an error needs a reason.

Pin tool versions. Use the same golangci-lint version locally and in CI, compatible
with the project's Go version. Review upgrades separately: analyzer updates may
report new findings even with the same checks enabled.

Add dependencies with the code that uses them. First consider the standard library
and existing dependencies. Explain what a new library solves and why it helps.
Prefer a maintained library when it removes protocol or schema code we would
otherwise own. Count the required wrappers and conversions when judging the saving;
do not duplicate the library's implementation or tests.

Use `just deps` to keep module files consistent with the code. `go.mod` records
version requirements; `go.sum` records checksums. `go mod tidy` updates those files.
`go mod verify` checks cached module contents for changes; it does not scan for
vulnerabilities.

## Review checklist

- Does each package and function have a clear job? Are dependencies explicit?
- Do exported types and interfaces serve actual callers?
- Are validation, data ownership and construction requirements clear?
- Can callers identify errors without parsing messages? Are secrets kept private?
- Are related state changes atomic? Who stops workers and releases resources?
- Can a reader follow the main path without extra explanation?
- Do tests catch contract violations, with visible expectations and independent data?
- Do tools and dependencies solve a current need?
