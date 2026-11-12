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
    go work use ./templates/navi-code ./templates/navi-daemon ./templates/navi-sqlite ./templates/navi-fleet ./templates/navi-standard
    go work edit -replace=github.com/flintwillow-software/opennavi@v0.1.0=.

# Build the navi-code starter template into bin/
build-navi-code: workspace
    cd templates/navi-code && go build -o ../../var/bin/navi-code ./cmd/navi-code

# Build the full-fat navi-fleet template into bin/
build-navi-fleet: workspace
    cd templates/navi-fleet && go build -o ../../var/bin/navi-fleet ./cmd/navi-fleet

# Build the complete navi-standard template into bin/
build-navi-standard: workspace
    cd templates/navi-standard && go build -o ../../var/bin/navi-standard ./cmd/navi-standard

# Build, vet, and test every starter template (what CI's template job runs)
templates: workspace
    #!/usr/bin/env bash
    # Separate from `ci`: the templates are nested modules, so they need the
    # workspace above and are not part of the root module's ./...
    set -euo pipefail
    for t in navi-code navi-daemon navi-sqlite navi-fleet navi-standard; do
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

# --- release ---------------------------------------------------------------

# Cut an annotated SDK release tag from the CHANGELOG.
#
# Tags are DELIBERATE: this is a procedure, not a merge hook. It computes the
# version from the `Semver:` hints in [Unreleased], drafts the tag message,
# opens it in $EDITOR, cuts [Unreleased] into a versioned section, and tags.
#
#   just release            # compute, draft, edit, cut, tag, push
#   just release --dry-run  # compute and draft only; change nothing
release *FLAGS:
    ./scripts/release.sh {{ FLAGS }}

# Self-test the changelog cutter (the release tooling's only logic worth testing)
release-test:
    python3 scripts/changelog-cut.py --self-test

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

# --- navi-standard playground ---------------------------------------------
#
# The complete single-navi template, run locally against a fresh SQLite file
# and a local Ollama. No external services, no trust store: the TUI runs
# IN-PROCESS (it is the same binary, so it is trusted structurally — ADR 0021
# decision 9), which is the simplest thing that works for a playground.

# Run navi-standard in the foreground with its TUI (fresh DB; local Ollama)
run-navi-standard: build-navi-standard
    #!/usr/bin/env bash
    # A FRESH database each run, so the playground starts from a known state
    # and a bad thought never wedges the next run. Delete var/data/navi-standard.db
    # by hand to keep state across runs.
    set -euo pipefail
    mkdir -p var/data
    rm -f var/data/navi-standard.db var/data/navi-standard.db-wal var/data/navi-standard.db-shm
    echo "navi-standard: ollama at ${OLLAMA_HOST:-http://localhost:11434}"
    echo "navi-standard: database var/data/navi-standard.db (fresh)"
    echo "navi-standard: starting the TUI in-process — press 'n' to queue a thought, 'q' to quit"
    export OLLAMA_HOST="${OLLAMA_HOST:-http://localhost:11434}"
    export NAVI_DB="$PWD/var/data/navi-standard.db"
    exec ./var/bin/navi-standard

# Start navi-standard headless, detached, and print the TUI connect command
run-navi-standard-headless: build-navi-standard
    #!/usr/bin/env bash
    # The remote-attach variant: the navi runs as a service (no terminal) and
    # a TUI attaches over the portal gateway. A REMOTE front-end must
    # authenticate, so this generates an Ed25519 key and a trust store on
    # first run. Prefer `run-navi-standard` (in-process) for local play; this
    # exists to exercise the headless deployment shape.
    set -euo pipefail
    mkdir -p var/data
    DB="$PWD/var/data/navi-standard.db"
    PORT="${NAVI_PORTAL_PORT:-8091}"
    LOG="$PWD/var/data/navi-standard.log"
    PIDFILE="$PWD/var/data/navi-standard.pid"
    KEY="$PWD/var/data/navi-standard-tui-key.pem"
    TRUST="$PWD/var/data/navi-standard-trust.yaml"

    # Stop any previous instance.
    if [[ -f "$PIDFILE" ]]; then kill "$(cat "$PIDFILE")" 2>/dev/null || true; rm -f "$PIDFILE"; fi
    sleep 1

    # A development trust store with one service:cli principal holding an
    # Ed25519 key (ADR 0021 decision 6). Generated once and reused.
    if [[ ! -f "$KEY" ]]; then
        openssl genpkey -algorithm ed25519 -out "$KEY" 2>/dev/null
    fi
    if [[ ! -f "$TRUST" ]]; then
        PUB="$(openssl pkey -in "$KEY" -pubout -outform DER 2>/dev/null | tail -c 32 | base64)"
        printf '%s\n' \
            'principals:' \
            '  - ref: service:cli' \
            '    keys:' \
            '      - id: tui-1' \
            "        public_key: $PUB" \
            > "$TRUST"
    fi

    echo "navi-standard: starting headless on 127.0.0.1:$PORT (log: $LOG)"
    export NAVI_DAEMON=1
    export NAVI_PORTAL_ADDR="127.0.0.1:$PORT"
    export NAVI_DB="$DB"
    export NAVI_TRUST_STORE="$TRUST"
    export OLLAMA_HOST="${OLLAMA_HOST:-http://localhost:11434}"
    setsid ./var/bin/navi-standard > "$LOG" 2>&1 < /dev/null &
    echo $! > "$PIDFILE"

    for _ in $(seq 1 30); do
        ss -ltn 2>/dev/null | grep -q ":$PORT" && break
        sleep 1
    done
    ss -ltn 2>/dev/null | grep -q ":$PORT" || { echo "navi-standard failed to start:" >&2; tail -20 "$LOG" >&2; exit 1; }
    echo "navi-standard: up (pid $(cat "$PIDFILE"))"
    echo
    echo "Connect a TUI with:"
    echo "  ./var/bin/navi connect --principal service:cli --key \"$KEY\" --key-id tui-1 http://127.0.0.1:$PORT"

# Stop the headless navi-standard started by run-navi-standard-headless
stop-navi-standard:
    #!/usr/bin/env bash
    set -euo pipefail
    PIDFILE="$PWD/var/data/navi-standard.pid"
    if [[ -f "$PIDFILE" ]]; then
        kill "$(cat "$PIDFILE")" 2>/dev/null || true
        rm -f "$PIDFILE"
    fi
    pkill -f 'var/bin/navi-standard$' 2>/dev/null || true
    echo "navi-standard: stopped"

# --- cleanup ---------------------------------------------------------------

# Remove build artifacts and runtime state
clean:
    rm -rf var
