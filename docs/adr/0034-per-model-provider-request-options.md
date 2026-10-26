---
status: accepted
date: '2026-10-26'
supersedes: []
tags:
  - adr
  - llm-provider
  - openrouter
  - model
---

# 0034 — Per-model provider request options (OpenRouter routing and reasoning)

## Context

OpenRouter is a **gateway**: one key, many underlying models, and — for each
model — several upstream vendors that can serve it. The request body may carry a
`provider` object that selects and orders those vendors:

```json
{
  "model": "deepseek/deepseek-v4.1-flash",
  "provider": {
    "only": ["inference-net/fp8"],
    "order": ["inference-net/fp8"],
    "allow_fallbacks": false
  }
}
```

This matters because OpenRouter's default routing is a *choice we did not make*.
The same model id can be served by a different vendor at a different
quantisation, with a different context window, different cost, and different
output. `allow_fallbacks: false` is the difference between "this model" and
"this model, or whatever OpenRouter picks when the first choice is busy".

OpenRouter likewise takes a `reasoning` object (`{"effort": "high"}`) to request
a reasoning model's extended thinking, or its absence.

**The workspace's own pi uses both, and a navi cannot.** The pi model config
(`~/.pi/agent/models.json`, mirrored at `hosts/navi-dock/pi/models.json`) declares
per model:

```json
"compat": {
  "thinkingFormat": "openrouter",
  "openRouterRouting": {
    "only": ["inference-net/fp8"],
    "order": ["inference-net/fp8"],
    "allow_fallbacks": false
  }
}
```

so an interactive session is pinned to `inference-net/fp8` with no fallbacks,
and `deepseek/deepseek-v4.1-flash` reasons at the configured level. A navi
thought on the same model id gets neither.

**Why not today.** The request body is built by `buildOpenAIRequest`
(`internal/llm/openai_compat.go:58`) into `openaiRequest`, whose fields are
exactly `model`, `messages`, `stream`, `tools`, `temperature`, `max_tokens`.
There is no `provider` and no `reasoning`. Nothing above it can express either:

- `model.Provider` (`model/model.go:16`) — `Name`, `Model`, `Endpoint`,
  `Credential`, `Mode`, `Thinking`, `Default`.
- `llm.Config` (`internal/llm/types.go:79`) — `Provider`, `Model`,
  `Temperature`, `MaxTokens`, `Thinking`, `Endpoint`, `APIKey`.
- `config.ProviderConfig` (`internal/config/runtime.go:26`) — `Primary`,
  `Fallback`, `Model`, `Endpoint`, `Thinking`, `APIKey`.

The only OpenRouter-specific surface implemented is the identity headers
(`HTTP-Referer`, `X-Title`) in `newRequest` (`internal/llm/openrouter.go:128`).

**A second, silent bug is the same gap.** `model.Provider.Thinking` is plumbed
for **Ollama only** (`internal/provider/provider.go:127` sets `op.Think`). The
OpenRouter branch builds `NewOpenRouterProviderWithClient(endpoint, secret,
client)`, which takes no thinking setting, and no `reasoning` parameter is ever
emitted. So `model.WithThinking` / `--think` is accepted, validated, reported by
`model_list` — and **has no effect** on OpenRouter. That is the failure mode this
repository's own security documentation names as the one to design against: a
capability that appears present and is not.

Both are the same underlying gap — *the OpenRouter request body carries no
per-model options* — so they are decided here together.

## Decision

1. **Provider request options are declared per model, on the provider
   descriptor.** `model.Provider` gains an options field keyed by model id:

   ```go
   n.Models.Add(model.OpenRouter("deepseek/deepseek-v4.1-flash",
       model.Default(),
       model.WithRouting("deepseek/deepseek-v4.1-flash", model.Routing{
           Only:           []string{"inference-net/fp8"},
           Order:          []string{"inference-net/fp8"},
           AllowFallbacks: model.Bool(false),
       }),
       model.WithReasoning("deepseek/deepseek-v4.1-flash", model.Reasoning{Effort: "high"}),
   ))
   ```

   **Why keyed by model and not on the descriptor.** Routing is a property of a
   *model*, not of a provider: `inference-net/fp8` serves deepseek and would
   never serve a Claude model. `model.Provider` carries one default model, but a
   thought may name a different model per run (ADR 0009). Routing attached to
   the descriptor alone would therefore apply to the wrong model the moment a
   thought overrode it — silently. Keying by model id makes the declaration
   honest and makes an unmatched override carry no routing rather than the wrong
   routing.

2. **Options are validated at composition time, and an option naming a model the
   provider does not serve is an error.** A typo in a model id must fail
   assembly, not silently route nothing. This follows the compiled-ceiling
   precedent: declarations are validated by name rather than trusted.

3. **Routing is expressed as a typed value, not an opaque map.** The fields
   OpenRouter documents (`only`, `order`, `ignore`, `allow_fallbacks`,
   `require_parameters`, `quantizations`, `sort`, `max_price`, `data_collection`)
   are enumerated. A typed value gives a composition-time error for a typo and
   keeps the declaration reviewable; an opaque map would trade that for tracking
   OpenRouter's API without an SDK release. The set is additive — a new field is
   a new struct field, never a breaking change.

4. **`allow_fallbacks` is a pointer, so "unset" and "false" are different.**
   `nil` sends no `allow_fallbacks` and keeps OpenRouter's default (`true`);
   `false` is a deliberate refusal to degrade. This mirrors `Thinking *bool` on
   the Ollama provider, which already distinguishes the two.

5. **Reasoning is per-model too, and reaches OpenRouter as `reasoning`.** A
   `model.Reasoning{Effort}` value is emitted as `{"reasoning": {"effort": ...}}`,
   matching the pi `openrouter` thinking format. This closes the silent-drop bug
   in decision 1's context: `model.WithThinking` remains the *provider-level*
   on/off (Ollama's `think`), and per-model `reasoning` is the OpenRouter
   expression of the same intent.

6. **The options travel with the model, not with the request.** `llm.Config`
   gains the resolved options for the model the request is being made for, so a
   provider adapter receives them per call and cannot drift from the selection.
   The OpenRouter adapter emits `provider` and `reasoning` only when set, so a
   navi that declares neither sends a byte-identical body to today's.

7. **Non-OpenRouter providers ignore options they do not understand, but the
   declaration is still validated.** An option declared for an Ollama model is a
   composition error naming the provider, rather than a value silently dropped at
   request time.

## Consequences

- A navi can pin an upstream vendor exactly as the pi config does, so "the navi
  and pi run the same model" means the same *backend*, not merely the same model
  id. Cost, context window, and quantisation stop varying by luck.
- `model.WithThinking` stops being a silent no-op on OpenRouter, which removes a
  capability-that-appears-present-and-is-not.
- `model_list` can report the effective routing and reasoning, so "which
  upstream served this" becomes answerable from the navi rather than from
  OpenRouter's dashboard.
- The declaration surface grows by one nested value per model. A navi that
  declares no options is unaffected — the request body is unchanged.
- The typed routing struct is a small ongoing maintenance cost as OpenRouter's
  API grows; each new field is additive.
- A thought that overrides the model to one with no declared routing gets
  OpenRouter's default routing, which is the honest outcome and is reportable.

## Alternatives considered

- **Routing on the provider descriptor, applying to its declared model.**
  Rejected: a per-thought model override (ADR 0009) would silently carry the
  wrong model's routing, which is worse than carrying none.
- **An opaque `map[string]any` passthrough.** Rejected as the primary shape: it
  loses composition-time validation and makes the declaration unreviewable. Kept
  as a possible escape hatch if OpenRouter ships a field faster than the SDK
  tracks it.
- **A general "provider request options" seam covering every provider.** Deferred
  rather than rejected: OpenRouter is the only provider here with per-model
  routing, and inventing an abstraction from one instance would be speculative.
  The `llm.Config` carriage in decision 6 is deliberately shaped so a second
  provider can reuse it.
- **Reading the pi `models.json` at runtime.** Rejected: it is a config format of
  a different program, in a different language, and it would make a navi's
  request shape depend on a file the navi does not own. The values are declared
  where every other capability is (ADR 0006/0009).
- **Fixing only the routing and leaving `reasoning`.** Rejected: both are the
  same missing seam, and shipping one while the other stays a silent no-op keeps
  the worse bug.
- **A `provider` object on the thought instead of the declaration.** Rejected: it
  makes the routing a runtime input, which is the injection surface ADR 0013
  decision 4 exists to close, and it would not be reviewable in `main.go`.

## Open questions

1. **`max_price` shape.** OpenRouter accepts it as a number or a per-token
   object; the typed struct currently takes the simple form.
2. **Reasoning effort vocabulary.** OpenRouter's effort levels are model-specific
   (`none`/`low`/`medium`/`high`, and pi maps a richer set through
   `thinkingLevelMap`). Whether the SDK models the level map or passes the effort
   through as a string is undecided; the passthrough is the current shape.
3. **Reporting.** Whether `model_list` surfaces routing inline or behind a flag,
   given the inventory already costs context on every thought.
4. **Session affinity.** The pi config sets `sendSessionAffinityHeaders` for
   OpenRouter, which keeps a conversation on one upstream. Not implemented here;
   whether a navi thought should be sticky to an upstream is undecided.

## Related

- [ADR 0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — build-time omission, runtime ceiling
- [ADR 0008](./0008-secrets-interpolated-never-literal.md) — credentials are references
- [ADR 0009](./0009-providers-set-with-per-thought-selection.md) — providers are a set with per-thought selection
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — the injection boundary this keeps closed
