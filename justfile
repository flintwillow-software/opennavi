# opennavi development tasks
#
# Artifacts and runtime state are kept out of the source tree:
#   var/bin/   — compiled binaries (build artifacts)
#   var/data/  — runtime state (navi.db, logs)
# Both are gitignored at the repo root (anchored patterns), so stray
# files there can never shadow tracked source like cmd/navi/ once did
# (NAVI-54).

set positional-arguments

default:
    @just --list

# --- build ---------------------------------------------------------------

# Build the navi CLI into bin/
build:
    go build -o var/bin/navi ./cmd/navi

# Build, vet, gofmt-check, and race-test — same gates as CI
ci: build vet fmt-check test-race

# --- checks ---------------------------------------------------------------

# Run go vet
vet:
    go vet ./...

# Check gofmt formatting (CI merge gate; no files rewritten)
fmt-check:
    #!/usr/bin/env bash
    unformatted="$(gofmt -l .)"
    if [[ -n "$unformatted" ]]; then
        echo "gofmt needed on:" >&2
        echo "$unformatted" >&2
        exit 1
    fi

# Rewrite gofmt formatting in place
fmt:
    gofmt -w .

# Run unit tests
test:
    go test ./...

# Run tests with the race detector (CI-only merge gate; 10m timeout)
test-race:
    go test -race -timeout 10m ./...

# --- run (state under var/) ----------------------------------------------

# Start the daemon with its database under var/
daemon *args:
    mkdir -p var/data
    ./var/bin/navi daemon -db var/data/navi.db "$@"

# Run a health probe against the data/ database
health *args:
    ./var/bin/navi health -db var/data/navi.db "$@"

# Enqueue a thought against the data/ database
enqueue *args:
    ./var/bin/navi enqueue -db var/data/navi.db "$@"

# --- cleanup ---------------------------------------------------------------

# Remove build artifacts and runtime state
clean:
    rm -rf var
