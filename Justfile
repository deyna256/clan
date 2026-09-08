# Command skeleton: checks and lifecycle operations are not configured yet.

# Run all tests in the module with race detection and shuffled execution.
test:
    go test -race -shuffle=on -count=1 ./...

# Run fast unit tests through go test.
test-unit:
    go test -short -race -shuffle=on -count=1 ./...

# Run static analysis without modifying source files.
lint:
    golangci-lint run

# Format Go files in place.
format:
    gofmt -w .

# Synchronize module dependencies and verify their cached contents.
deps:
    go mod tidy
    go mod verify

# Build the project Docker image from the current source.
build:
    @echo "build: not configured; Dockerfile is empty." >&2
    @exit 1

# Stop the project and remove its containers/networks, retaining persistent state.
down:
    @echo "down: not configured; project container configuration is missing." >&2
    @exit 1

# Start the already-built image without rebuilding or pulling.
run:
    @echo "run: not configured; project container configuration is missing." >&2
    @exit 1

# Remove project artifacts, caches, images and volumes, including their state.
clean:
    @echo "clean: not configured; project resource ownership is not defined yet." >&2
    @exit 1
