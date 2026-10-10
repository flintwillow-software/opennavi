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

# Create the go.work the starter templates need
workspace:
    #!/usr/bin/env bash
    # The templates are nested modules pinned to a release that does not exist
    # yet — this repository has no version tags — so `use` alone is not enough:
    # without the replace, Go still resolves the requirement, tries to fetch
    # v0.1.0, and fails with "unknown revision v0.1.0". go.work is gitignored,
    # so this is safe to run repeatedly and never commits anything.
    set -euo pipefail
    go work init . 2>/dev/null || true
    go work use ./templates/navi-code ./templates/navi-daemon ./templates/navi-sqlite
    go work edit -replace=github.com/flintwillow-software/opennavi@v0.1.0=.

# Build the navi-code starter template into bin/
build-navi-code: workspace
    cd templates/navi-code && go build -o ../../var/bin/navi-code ./cmd/navi-code

# Build, vet, and test every starter template (what CI's template job runs)
templates: workspace
    #!/usr/bin/env bash
    # Separate from `ci`: the templates are nested modules, so they need the
    # workspace above and are not part of the root module's ./...
    set -euo pipefail
    for t in navi-code navi-daemon navi-sqlite; do
        echo "--- $t"
        (cd "templates/$t" && go build ./... && go vet ./... && go test ./...)
    done

# Build, vet, gofmt-check, and race-test the root module
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
    ./var/bin/navi host --daemon -db var/data/navi.db "$@"

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
