#!/usr/bin/env bash
set -euo pipefail

# release-tag — tag an already-merged changelog cut.
#
#   just release-tag v0.6.0
#   just release-tag v0.6.0 --dry-run
#
# The second half of `just release`, and deliberately a separate command: the
# changelog cut goes through a pull request (main is protected), and the tag
# must name a commit that is really on main. Tagging before the merge is what
# produced a published tag pointing at an unmerged commit the first time this
# ran.
#
# The tag message is drafted from the VERSIONED section (`## [0.6.0] - <date>`)
# rather than from the [Unreleased] section the cut consumed — so the message
# describes exactly what the tag names, and cannot drift from the committed
# changelog.

CHANGELOG="${CHANGELOG:-CHANGELOG.md}"
REMOTE="${REMOTE:-origin}"
DRY_RUN=false
ASSUME_YES=false
NO_EDIT=false

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
die() { echo -e "${RED}release-tag: $*${NC}" >&2; exit 1; }
info() { echo -e "${BLUE}release-tag:${NC} $*"; }
ok() { echo -e "${GREEN}release-tag:${NC} $*"; }
warn() { echo -e "${YELLOW}release-tag:${NC} $*"; }

VERSION=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=true; shift ;;
        --yes|-y) ASSUME_YES=true; shift ;;
        --no-edit) NO_EDIT=true; shift ;;
        -h|--help) sed -n '3,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        -*) die "unknown flag: $1 (try --help)" ;;
        *) [[ -z "$VERSION" ]] || die "unexpected argument: $1"; VERSION="$1"; shift ;;
    esac
done

[[ -n "$VERSION" ]] || die "missing <version> (e.g. v0.6.0)"
[[ "$VERSION" == v* ]] || die "version must look like vX.Y.Z, got '$VERSION'"

git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository"
cd "$(git rev-parse --show-toplevel)"

[[ -f "$CHANGELOG" ]] || die "no $CHANGELOG at the repository root"
[[ -d cmd/navi-release ]] || die "cmd/navi-release is missing — is this an opennavi checkout?"

# --- preconditions ---------------------------------------------------------
#
# The tag must name a commit that is ON main. Every check below exists because
# the first version of this tooling skipped it and published a tag pointing at
# an unmerged commit.

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
[[ "$BRANCH" == "main" ]] || die "tags are cut from main, not '$BRANCH' (checkout main first)"

if [[ -n "$(git status --porcelain)" ]]; then
    die "working tree is dirty — commit or stash first"
fi

git fetch --quiet "$REMOTE" main 2>/dev/null || true
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse "$REMOTE/main" 2>/dev/null || echo '')" ]]; then
    die "local main differs from $REMOTE/main — the tag must name the merged commit (git pull --ff-only)"
fi

git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null && die "$VERSION already exists"

# The changelog cut must be present, or the tag would name a release whose
# section was never written.
if ! grep -q "^## \[${VERSION#v}\]" "$CHANGELOG"; then
    die "no '## [${VERSION#v}]' section in $CHANGELOG — run \`just release\` and merge the cut first"
fi

info "tagging $VERSION at $(git rev-parse --short HEAD) ($(git log -1 --format=%s))"

# --- draft the message from the versioned section --------------------------

RELEASE_DIR="$(mktemp -d)"
DRAFT="$(mktemp)"
trap 'rm -rf "$RELEASE_DIR"; rm -f "$DRAFT"' EXIT
go build -o "$RELEASE_DIR/navi-release" ./cmd/navi-release \
    || die "could not build cmd/navi-release"

"$RELEASE_DIR/navi-release" draft-section "$CHANGELOG" "${VERSION#v}" > "$DRAFT"

echo
echo "────────────── draft tag message (edit in \$EDITOR) ──────────────"
cat "$DRAFT"
echo "──────────────────────────────────────────────────────────────────"
echo

if [[ "$DRY_RUN" == true ]]; then
    info "--dry-run: nothing tagged."
    exit 0
fi

if [[ "$NO_EDIT" != true ]]; then
    "${EDITOR:-vi}" "$DRAFT"
    if grep -q '<one-line summary of this release>' "$DRAFT"; then
        die "the draft subject was not filled in — edit it before tagging"
    fi
fi

echo
echo "──────────── final tag message ────────────"
cat "$DRAFT"
echo "───────────────────────────────────────────"
echo

if [[ "$ASSUME_YES" != true ]]; then
    read -r -p "Tag $VERSION and push? [y/N] " reply
    [[ "$reply" =~ ^[Yy]$ ]] || { info "aborted."; exit 0; }
fi

git tag -a "$VERSION" -F "$DRAFT"
ok "tagged $VERSION"

git push "$REMOTE" "$VERSION"
ok "pushed $VERSION"

echo
info "CI runs on the tag (ci.yml triggers tags: ['v*'])."
info "Next: bump the navi's SDK pin — see the release-navi skill."
