---
name: commit
description: Commit staged changes with a conventional commit message. Analyzes staged diffs, drafts a message, and presents it for review before committing.
---

# Commit Skill

Commit currently staged changes with a conventional commit message.

## Rules

> **Note:** Before following these rules, check the repo's `contributing.md` (or equivalent) for any repository-specific conventions on committing — branch naming, commit format requirements, signed commits, or other workflow rules. If those differ from what is defined here, follow the repo's guidelines instead.

- Commit message must use strict conventional commit format: `type(scope): description` (e.g., `feat(auth): add OAuth2 login`). Scope is optional. Allowed types: `feat`, `fix`, `chore`, `docs`, `refactor`, `perf`, `test`, `ci`, `build`, `style`.
- For breaking changes, append `!` after type/scope (e.g., `feat(api)!: remove v1 endpoints`).
- Keep the subject line under 72 characters.
- Add a body only when the subject line alone is insufficient to explain the change.
- Do NOT include any "Co-authored by" or any other way to indicate AI was used.
- Show the commit message for review before committing.

## Steps

1. Run `git diff --cached --stat` to see what is staged
2. If nothing is staged, inform the user and stop
3. Run `git diff --cached` to analyze the staged changes
4. Draft a conventional commit message following the rules above
5. Present for my review before committing
6. Commit using multiple `-m` flags: `git commit -m "subject" -m "body"`. If no body is needed, use a single `-m`.

## Commit Types

| Type | Use For | Example |
|------|---------|--------|
| `feat` | New feature | `feat(auth): add OAuth2 login` |
| `fix` | Bug fix | `fix(api): handle null pointer` |
| `docs` | Documentation | `docs(readme): update install guide` |
| `style` | Formatting, semicolons | `style: fix lint errors` |
| `refactor` | Restructuring, no behavior change | `refactor(auth): simplify middleware` |
| `perf` | Performance improvements | `perf(db): optimize queries` |
| `test` | Adding or correcting tests | `test(auth): add login tests` |
| `build` | Build system, external deps | `build: update Dockerfile` |
| `ci` | CI configuration | `ci: add GitHub Actions` |
| `chore` | Other changes, no src/test | `chore: update dependencies` |

## Subject Line Guidelines

- Maximum 72 characters
- Use imperative mood ("add" not "added", "fix" not "fixed")
- No period at the end
- Start with lowercase
- Be specific about what changed

## Body Guidelines

- Explain **what** and **why**, not how
- Wrap at 72 characters
- Use bullet points for lists
- Reference related issues with `#123`

## Breaking Changes

Mark with `!` after type/scope in the subject:

```
feat(api)!: remove deprecated v1 endpoints

BREAKING CHANGE: The /api/v1 endpoint is removed. Use /api/v2 instead.
```
