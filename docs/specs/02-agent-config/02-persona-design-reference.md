# Persona System — Design Reference

> Design details for the configurable personality system, extracted from the original gap analysis. The persona frontmatter dimensions define how a navi presents itself to the user.

## Persona Frontmatter Spec

```yaml
---
name: "Butler"                   # character name
style: casual-professional        # casual | professional | playful | formal | warm | direct
humor: low                        # none | low | medium | high
verbosity: concise                # concise | balanced | detailed
empathy: medium                   # low | medium | high
directness: medium                # low | medium | high
creativity: low                   # low | medium | high
adaptation: mirror               # mirror (match user) | consistent | context-aware
---
```

## How It Works

- Frontmatter fields are parsed at persona load time
- Each dimension maps to a system prompt instruction block
- The `style` field selects a preset that sets defaults for all dimensions
- Individual dimensions override the preset
- The persona body (content after frontmatter) is the free-form personality description

## Preset Mappings

| Preset | Formality | Humor | Verbosity | Empathy | Directness | Creativity |
|--------|-----------|-------|-----------|---------|------------|------------|
| butler | formal | none | concise | high | high | low |
| au-pair | warm | low | balanced | high | medium | low |
| coach | casual | medium | balanced | medium | high | medium |
| companion | warm | high | detailed | high | low | medium |
| minimalist | direct | none | concise | low | high | low |
| default | casual-professional | low | balanced | medium | medium | medium |

## Implementation Notes

- Extend persona loading to support frontmatter parsing
- Add `PersonaConfig` struct with dimension fields
- Generate system prompt instructions from dimensions at persona load time
- Keep `.md` format — frontmatter + free-form body, no new file format

## Related

- [Persona System Spec](./01-persona-system.md) — the formal spec this design detail feeds into