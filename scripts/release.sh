#!/usr/bin/env bash
set -euo pipefail

# release — cut an annotated SDK release tag from the CHANGELOG.
#
#   just release              # compute, draft, edit, cut the section, tag, push
#   just release --dry-run    # compute and draft only; change nothing
#   just release --yes        # skip the confirmation prompt (not the editor)
#   just release --no-edit    # accept the generated draft as-is
#
# Tags are DELIBERATE: this is a procedure, not a merge hook. Every merge to
# main does not publish a release; the operator decides when one is cut. What
# is automated is the part that used to be remembered by hand — the version
# arithmetic, the entry inventory, and the mechanical parts of the tag message.
#
# The version comes from the `Semver: patch|minor|major` hints in the
# `## [Unreleased]` section. Every entry carries exactly one such hint (the
# changelog.yml CI check requires one in a PR body), so the input already
# exists and nothing new has to be maintained. The highest hint present wins:
# one `minor` among fifty `patch`es is still a minor release.
#
# The script also CUTS the [Unreleased] section into a versioned one. Without
# that, every release would re-list every change since the beginning — which is
# exactly the state this repo was in when the tool was written.

CHANGELOG="${CHANGELOG:-CHANGELOG.md}"
REMOTE="${REMOTE:-origin}"
DRY_RUN=false
ASSUME_YES=false
NO_EDIT=false

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
die() { echo -e "${RED}release: $*${NC}" >&2; exit 1; }
info() { echo -e "${BLUE}release:${NC} $*"; }
ok() { echo -e "${GREEN}release:${NC} $*"; }
warn() { echo -e "${YELLOW}release:${NC} $*"; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=true; shift ;;
        --yes|-y) ASSUME_YES=true; shift ;;
        --no-edit) NO_EDIT=true; shift ;;
        -h|--help) sed -n '3,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) die "unknown argument: $1 (try --help)" ;;
    esac
done

[[ -f "$CHANGELOG" ]] || die "no $CHANGELOG (run from the repository root)"
git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository"

# --- preconditions ---------------------------------------------------------

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
[[ "$BRANCH" == "main" ]] || die "releases are cut from main, not '$BRANCH'"

if [[ -n "$(git status --porcelain -- "$CHANGELOG" scripts/release.sh)" ]]; then
    die "$CHANGELOG or the release script has uncommitted changes — commit first"
fi

git fetch --quiet "$REMOTE" main 2>/dev/null || true
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse "$REMOTE/main" 2>/dev/null || echo '')" ]]; then
    warn "local main differs from $REMOTE/main — the tag will name your local HEAD"
fi

# --- version ---------------------------------------------------------------

LAST_TAG="$(git tag --sort=version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | tail -1)"
[[ -n "$LAST_TAG" ]] || die "no existing vX.Y.Z tag to increment from"

HINTS="$(awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{f=0} f' "$CHANGELOG" \
    | grep -o 'Semver: [a-z]*' | sed 's/Semver: //' | sort -u | tr '\n' ' ')"
[[ -n "${HINTS//[[:space:]]/}" ]] || die "no 'Semver: patch|minor|major' hints in [Unreleased]"

BUMP="patch"
grep -qw minor <<<"$HINTS" && BUMP="minor"
grep -qw major <<<"$HINTS" && BUMP="major"

VER="${LAST_TAG#v}"
IFS=. read -r MA MI PA <<<"$VER"
case "$BUMP" in
    major) MA=$((MA + 1)); MI=0; PA=0 ;;
    minor) MI=$((MI + 1)); PA=0 ;;
    patch) PA=$((PA + 1)) ;;
esac
NEXT="v$MA.$MI.$PA"

info "last tag:  $LAST_TAG"
info "hints:     $HINTS"
info "bump:      $BUMP"
ok   "next:      $NEXT"

git rev-parse -q --verify "refs/tags/$NEXT" >/dev/null && die "$NEXT already exists"

# --- draft the tag message -------------------------------------------------
#
# A CONCISE draft, not the whole changelog: the entry titles grouped by section
# with counts, plus the issue reference list. The operator supplies the prose
# summary in the editor, matching how every existing tag was written (see
# `git tag -l --format='%(contents)' v0.4.1`).

DRAFT="$(mktemp)"
trap 'rm -f "$DRAFT"' EXIT

python3 - "$NEXT" "$LAST_TAG" > "$DRAFT" <<'PY'
import re, sys
from collections import OrderedDict

version, last_tag = sys.argv[1], sys.argv[2]
text = open("CHANGELOG.md").read()

m = re.search(r'^## \[Unreleased\]\s*\n(.*?)(?=^## \[|\Z)', text, re.S | re.M)
body = m.group(1) if m else ""

# Collect (heading, entry) pairs, joining continuation lines.
entries = []
heading = "Changed"
for raw in body.splitlines():
    line = raw.rstrip()
    if line.startswith("### "):
        heading = line[4:].strip()
    elif line.startswith("- "):
        e = re.sub(r'\s*—\s*`Semver:\s*\w+`\s*$', '', line[2:])
        entries.append([heading, e])
    elif line.strip() and entries:
        entries[-1][1] += " " + line.strip()

def title_of(e):
    m = re.match(r'\*\*(.+?)\*\*', e)
    if m:
        return m.group(1).rstrip('.')
    return re.split(r'(?<=[.!?])\s', e)[0][:110]

def refs_of(e):
    return [int(n) for n in re.findall(r'NAVI-(\d+)', e)]

by_head = OrderedDict()
for h, e in entries:
    by_head.setdefault(h, []).append(e)

refs = sorted({n for _, e in entries for n in refs_of(e)})

# Subject: the operator edits this. Seed it with the version and the counts so
# it is obviously a draft.
print(f"{version} — <one-line summary of this release>")
print()
n = len(entries)
print(f"{n} change{'s' if n != 1 else ''} since {last_tag}.")
print()

MAX_PER_HEAD = 8
for h, es in by_head.items():
    print(f"{h}:")
    for e in es[:MAX_PER_HEAD]:
        print(f"  - {title_of(e)}")
    if len(es) > MAX_PER_HEAD:
        print(f"  - … and {len(es) - MAX_PER_HEAD} more")
    print()

if refs:
    # Bounded so the Refs line stays readable; the full list is in the changelog.
    shown = refs if len(refs) <= 30 else refs[:30]
    line = ", ".join(f"NAVI-{n}" for n in shown)
    if len(refs) > 30:
        line += f", … (+{len(refs) - 30} more)"
    print(f"Refs {line}")
PY

echo
echo "────────────── draft tag message (edit in \$EDITOR) ──────────────"
cat "$DRAFT"
echo "──────────────────────────────────────────────────────────────────"
echo

if [[ "$DRY_RUN" == true ]]; then
    info "--dry-run: nothing changed."
    exit 0
fi

if [[ "$NO_EDIT" != true ]]; then
    "${EDITOR:-vi}" "$DRAFT"
    # Reject a draft the operator did not actually touch.
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
    read -r -p "Cut $NEXT at $(git rev-parse --short HEAD) and push? [y/N] " reply
    [[ "$reply" =~ ^[Yy]$ ]] || { info "aborted."; exit 0; }
fi

# --- cut the changelog section --------------------------------------------
#
# [Unreleased] is emptied of its entries and a versioned section is inserted
# below it. Without this, every future release would re-list everything since
# the beginning.

TODAY="$(date +%Y-%m-%d)"
python3 scripts/changelog-cut.py "${NEXT#v}" "$TODAY" "$CHANGELOG"

ok "cut [Unreleased] into [$NEXT] in $CHANGELOG"

git add "$CHANGELOG"
git commit -m "docs(changelog): cut $NEXT"
ok "committed the changelog cut"

git tag -a "$NEXT" -F "$DRAFT"
ok "tagged $NEXT"

git push "$REMOTE" main "$NEXT"
ok "pushed main and $NEXT to $REMOTE"

echo
info "CI runs on the tag (ci.yml triggers tags: ['v*'])."
info "Next: bump the navi's SDK pin — see the release-navi skill."
