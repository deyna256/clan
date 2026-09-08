# Command skeleton: checks and lifecycle operations are not configured yet.

# Run both test suites, preserving failure from either suite.
test:
    #!/bin/sh
    status=0
    just test-unit || status=1
    just test-integration || status=1
    exit "$status"

# Run fast unit tests through gotestsum.
test-unit:
    @echo "test-unit: not configured; Go module and unit tests are missing." >&2
    @exit 1

# Run integration tests through gotestsum with isolated external dependencies.
test-integration:
    @echo "test-integration: not configured; Go module and integration tests are missing." >&2
    @exit 1

# Check formatting, static analysis, module consistency and Dockerfile.
lint:
    @echo "lint: not configured; Go module and check configuration are missing." >&2
    @exit 1

# Scan Go source and the built image for vulnerabilities.
vuln:
    @echo "vuln: not configured; Go module and image configuration are missing." >&2
    @exit 1

# Build the project Docker image from the current source.
build:
    @echo "build: not configured; Dockerfile is empty and application code is missing." >&2
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
