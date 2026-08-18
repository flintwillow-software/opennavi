---
name: pr-update
description: "Update an existing PR's title and description using gh CLI — with conventional commit formatting."
---

# Update a PR Skill

Update the current branch's open PR title and description using the `gh` CLI.

## Rules

> **Note:** Before following these rules, check the repo's `contributing.md` (or equivalent) for any repository-specific conventions on PR creation — branch naming, required reviewers, CI checks, description templates, label requirements, or other workflow rules. If those differ from what is defined here, follow the repo's guidelines instead.

- **Title** must use strict conventional commit format: `type(scope): description` (e.g., `feat(auth): add OAuth2 login`). Scope is optional. Allowed types: `feat`, `fix`, `chore`, `docs`, `refactor`, `perf`, `test`, `ci`, `build`, `style`.
- For breaking changes, append `!` after type/scope (e.g., `feat(api)!: remove v1 endpoints`).
- Keep the title under 72 characters.
- Add a body only when the title alone is insufficient to explain the change.
- Do NOT include any "Co-authored by" or any other way to indicate AI was used.
- Show the updated PR description for review before applying it.

## Steps

1. Get the current branch's open PR: `gh pr view --json number,title,body`
2. If no open PR exists, inform the user and stop
3. Get commits on this branch vs main: `git log main..HEAD`
4. Get diff summary: `git diff main...HEAD --stat`
5. Draft updated PR title (conventional commit) and description using the same format as **Create a PR** above
6. Present for my review before updating
7. Apply with: `gh pr edit <number> --title "<title>" --body "<body>"`

## Description Format

- Group changes by conventional commit type using these headers:
  - **Features** (`feat`)
  - **Bug Fixes** (`fix`)
  - **Refactoring** (`refactor`)
  - **Performance** (`perf`)
  - **Documentation** (`docs`)
  - **Chores** (`chore`)
  - **Tests** (`test`)
  - **CI** (`ci`)
  - **Build** (`build`)
  - **Styles** (`style`)
- Each entry is a bullet point (one per commit or logical group).
- Only include sections that have entries.
- No prose preamble — start directly with headers.

## Semver Hint

Include exactly one line at the very bottom of the description body:

```
Semver: patch   # fixes only
Semver: minor   # new features
Semver: major   # breaking changes
```
