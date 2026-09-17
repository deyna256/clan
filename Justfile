# Run all tests in the module with race detection and shuffled execution.
test:
    go test -race -shuffle=on -count=1 ./...

# Run fast unit tests through go test.
test-unit:
    go test -short -race -shuffle=on -count=1 ./...

# Run Go and Dockerfile static analysis without modifying files.
lint:
    #!/usr/bin/env sh
    set -eu
    go vet ./...
    case "$(hadolint --version)" in
        *" 2.15.1") ;;
        *)
            printf '%s\n' 'Hadolint 2.15.1 is required.' >&2
            exit 1
            ;;
    esac
    hadolint Dockerfile

# Format Go files, or check them without changes with --check.
[positional-arguments]
format mode="":
    #!/usr/bin/env sh
    set -eu
    case "$1" in
        "") gofmt -w . ;;
        --check)
            files=$(gofmt -l .)
            if [ -n "$files" ]; then
                printf '%s\n' "$files"
                printf '%s\n' 'Run just format to format these files.' >&2
                exit 1
            fi
            ;;
        *)
            printf '%s\n' 'Usage: just format [--check]' >&2
            exit 2
            ;;
    esac

# Synchronize module dependencies and verify their cached contents.
deps:
    go mod tidy
    go mod verify

# Build the project Docker image from the current source.
build:
    docker compose build

# Stop the project and remove its containers/networks, retaining persistent state.
down:
    docker compose down

# Start the already-built image without rebuilding or pulling.
run:
    docker compose up --detach --no-build --pull never

# Remove project containers, networks, image and data volume, including all accounts and keys.
[confirm("This deletes the CLAN data volume, including all accounts and keys. Continue?")]
clean:
    docker compose down --volumes --rmi all
