#!/usr/bin/env python3
"""Cut the CHANGELOG's [Unreleased] section into a versioned one.

    changelog-cut.py <version> <date> [path]

Moves every entry under `## [Unreleased]` into a new
`## [<version>] - <date>` section placed directly below it, leaving
`[Unreleased]` empty (and still present, so the next release has a home).

This is what makes repeated releases tractable: without it, the [Unreleased]
section grows without bound and every release re-lists everything since the
beginning — which is the state the repository was in when this was written
(200 entries, spanning v0.1.0 to the present).

It is a separate file rather than an inline heredoc so it can be run and tested
on its own, and so the release script has no Python interpolation to get wrong.

Exit codes: 0 ok, 1 the file or section is missing, 2 the version already exists.
"""

import re
import sys

UNRELEASED_RE = re.compile(r"^(## \[Unreleased\]\s*\n)(.*?)(?=^## \[|\Z)", re.S | re.M)


def cut(text: str, version: str, date: str) -> str:
    """Return text with [Unreleased]'s entries moved into a versioned section."""
    m = UNRELEASED_RE.search(text)
    if not m:
        raise ValueError("no '## [Unreleased]' section found")

    released = m.group(2).strip("\n")

    # A section that is already versioned, or an [Unreleased] with no entries,
    # is not a release — refuse rather than write an empty section.
    if not released.strip():
        raise ValueError("[Unreleased] is empty — nothing to release")
    if f"## [{version}]" in text:
        raise ValueError(f"[{version}] already exists in the changelog")

    # Normalise the released body's spacing, then drop the trailing blank lines
    # so the inserted section reads like the ones above it.
    body = "\n".join(released.splitlines()).rstrip()

    new_section = f"## [{version}] - {date}\n\n{body}\n\n"
    head = m.group(1)  # "## [Unreleased]\n"
    tail = text[m.end():]

    return text[: m.start()] + head + "\n" + new_section + tail


SAMPLE = """# Changelog

## [Unreleased]

### Added
- **Thing one** (NAVI-1). A description. — `Semver: minor`
- **Thing two** (NAVI-2). Another. — `Semver: patch`

### Fixed
- **A bug** (NAVI-3). Fixed it. — `Semver: patch`

## [1.0.0] - 2020-01-01

### Added
- Initial release

[Unreleased]:
[1.0.0]:
"""


def self_test() -> int:
    """Exercise cut() against a fixture. Returns 0 when every check passes.

    Run by `just release-test`. It lives here rather than in a separate test
    file so the release tooling stays a single dependency-free script — this
    repo's CI is `go test`, and a release tool is not worth a Python CI job.
    """
    failures = []

    def check(name, cond):
        if not cond:
            failures.append(name)

    out = cut(SAMPLE, "v1.1.0", "2026-11-12")

    m = re.search(r"^## \[Unreleased\]\s*\n(.*?)(?=^## \[|\Z)", out, re.S | re.M)
    check("Unreleased is emptied", bool(m) and not m.group(1).strip())
    check("versioned section inserted", "## [v1.1.0] - 2026-11-12" in out)
    check("versioned section sits below Unreleased",
          out.index("## [Unreleased]") < out.index("## [v1.1.0]"))
    check("previous release preserved", "## [1.0.0] - 2020-01-01" in out)
    check("entries moved", "Thing one" in out and "A bug" in out)
    check("link block preserved", "[Unreleased]:" in out)

    # A second cut of the same content must refuse: Unreleased is now empty.
    try:
        cut(out, "v1.2.0", "2026-11-13")
        failures.append("empty Unreleased was not refused")
    except ValueError:
        pass

    # A version that already exists must refuse.
    try:
        cut(SAMPLE.replace("## [1.0.0]", "## [v1.1.0]"), "v1.1.0", "2026-11-12")
        failures.append("existing version was not refused")
    except ValueError:
        pass

    # A changelog with no Unreleased section must refuse.
    try:
        cut("# Changelog\n\n## [1.0.0]\n", "v1.1.0", "2026-11-12")
        failures.append("missing Unreleased was not refused")
    except ValueError:
        pass

    if failures:
        for f in failures:
            print(f"FAIL: {f}", file=sys.stderr)
        return 1
    print("changelog-cut: all checks passed")
    return 0


def main(argv: list[str]) -> int:
    if len(argv) > 1 and argv[1] == "--self-test":
        return self_test()

    if len(argv) < 3:
        print("usage: changelog-cut.py <version> <date> [path]", file=sys.stderr)
        print("       changelog-cut.py --self-test", file=sys.stderr)
        return 1

    version, date = argv[1], argv[2]
    path = argv[3] if len(argv) > 3 else "CHANGELOG.md"

    try:
        text = open(path, encoding="utf-8").read()
    except OSError as e:
        print(f"changelog-cut: {e}", file=sys.stderr)
        return 1

    try:
        out = cut(text, version, date)
    except ValueError as e:
        print(f"changelog-cut: {e}", file=sys.stderr)
        return 2 if "already exists" in str(e) else 1

    with open(path, "w", encoding="utf-8") as f:
        f.write(out)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
