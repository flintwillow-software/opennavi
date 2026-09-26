---
status: accepted
date: '2026-09-24'
supersedes: []
spec-refs: [000200, 080900]
tags:
  - adr
  - security
  - secrets
---

# 0008 — Secrets are interpolated at runtime, never literals in code

## Context

A navi is compiled once and distributed as a signed binary. API keys and other
credentials must not be embedded in that artifact: they would travel to every
user, they would be covered by (and leak through) the signature, and rotation
would require a rebuild.

At the same time, the navi author knows which credentials the navi expects and
what the sensible default endpoint is. Operators should not have to rediscover
that.

## Decision

- **Developers declare credential *references*, not values.** A component
  declares which environment variable or file path it reads, with a default
  name:
  ```go
  connector.Discord(connector.Config{Credential: "DISCORD_TOKEN"})
  model.OpenRouter(model.Config{Credential: "OPENROUTER_API_KEY"})
  ```
- **Values are interpolated at runtime** from the environment or a file, via
  the existing config merge chain (`defaults → file → env → flags`).
- **Endpoint defaults may be compiled in** (e.g. `https://api.openrouter.ai`);
  the operator can override them at runtime.
- **No secret literals in source, tests, or fixtures.**
- Credential resolution is fail-closed: a missing credential disables the
  component with a clear error rather than silently degrading.

## Consequences

- Rotating a key is a config change, not a rebuild.
- The signed binary contains no secrets, so publishing or sharing it is safe.
- The operator must supply credentials; a navi with an unset credential
  surfaces the missing name clearly.
- Pairs with the credential vault direction (S080900) for hosted deployments.

## Alternatives considered

- **Compiled-in default keys.** Convenient for the developer, catastrophic for
  distribution. Rejected.
- **Config-only declaration (operator names everything from scratch).** Loses
  the developer's knowledge of what the navi needs. Rejected.