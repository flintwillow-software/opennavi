---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - agent-config
priority: medium
spec-refs: [020100, 020101, 020102, 020103, 020104, 020105]
---

# Plan: Persona System

**Scope:** Configurable agent personality via `persona.md` or `persona/` directory. Loaded into system prompt construction.

**Inspiration:** Hermes' SOUL.md concept, but with a more natural name and flexible structure.

---

## 00 Persona File Formats

### 1.1 Single File (`persona.md`) with Configurable Frontmatter

Persona files support YAML frontmatter for configuring personality dimensions. These dimensions are interpreted as system prompt instructions at runtime — no special runtime enforcement, just text generation shaping.

```yaml
---
name: "Navi"                    # character name
style: casual-professional       # preset: casual | professional | playful | formal | warm | direct
humor: low                       # none | low | medium | high
verbosity: concise               # concise | balanced | detailed
empathy: medium                  # low | medium | high
directness: medium               # low | medium | high
creativity: low                  # low | medium | high
adaptation: mirror               # mirror | consistent | context-aware
---
```

**Dimension descriptions:**

| Dimension | Values | Effect on System Prompt |
|-----------|--------|------------------------|
| `style` | casual, professional, playful, formal, warm, direct | Selects a preset that sets defaults for all other dimensions. Can be overridden by individual dimensions. |
| `humor` | none, low, medium, high | Controls joke frequency, playful language, wit |
| `verbosity` | concise, balanced, detailed | Response length: brief answers, normal explanations, or deep dives |
| `empathy` | low, medium, high | Emotional awareness, validation, supportive language |
| `directness` | low, medium, high | How blunt vs. diplomatic the agent is |
| `creativity` | low, medium, high | Novel suggestions vs. conservative/reliable answers |
| `adaptation` | mirror, consistent, context-aware | Mirror user style, stay consistent, or adapt to context |

**Preset mappings:**

| Preset | Humor | Verbosity | Empathy | Directness | Creativity |
|--------|-------|-----------|---------|------------|------------|
| butler | none | concise | high | high | low |
| au-pair | low | balanced | high | medium | low |
| coach | medium | balanced | medium | high | medium |
| companion | high | detailed | high | low | medium |
| minimalist | none | concise | low | high | low |
| casual-professional | low | balanced | medium | medium | medium |

**Example with frontmatter + body:**

```markdown
---
name: "Butler"
style: butler
humor: none
verbosity: concise
empathy: high
directness: high
creativity: low
adaptation: mirror
---

# Persona: Butler

## Identity
You are a discreet, anticipatory butler. You are precise, professional, and attentive to detail.

## Communication Style
- Address the user as "sir" or "madam" by default
- Be concise and precise — no filler
- Anticipate needs before they're expressed
- Warm but formal — never overly familiar

## Do's
- Confirm actions before executing
- Report completions concisely
- Offer alternatives when a request can't be fulfilled

## Don'ts
- Don't use slang or casual language
- Don't interrupt or speak over
- Don't make assumptions about preferences without evidence


### 1.2 Directory Format (`persona/`)

```
persona/
├── core.md          # Identity and communication style (always loaded)
├── humor.md         # Humor preferences, joke style
├── warmth.md        # Warmth level, formality settings
└── traits.md        # Additional personality traits
```

**Directory loading:** All files are concatenated in order (core → traits → warmth → humor) and injected as a single system prompt block.

### Requirement: Single File Persona Format

The system SHALL support a single `persona.md` file with optional YAML frontmatter for configuring personality dimensions.

#### Scenario: Persona file with frontmatter
- GIVEN a `persona.md` file exists with YAML frontmatter containing `name`, `style`, `humor`, `verbosity`, `empathy`, `directness`, `creativity`, and `adaptation` fields
- WHEN the persona is resolved
- THEN the system SHALL parse the frontmatter dimensions
- AND generate corresponding system prompt instructions from the dimension values

#### Scenario: Persona file without frontmatter
- GIVEN a `persona.md` file exists without YAML frontmatter
- WHEN the persona is resolved
- THEN the system SHALL use the raw file content as the persona body without dimension-based instruction generation

### Requirement: Persona Directory Format

The system SHALL support a `persona/` directory format with ordered files that are concatenated into a single persona block.

#### Scenario: Directory persona loading
- GIVEN a `persona/` directory exists with `core.md`, `traits.md`, `warmth.md`, and `humor.md`
- WHEN the persona is resolved
- THEN the system SHALL load the files in order (core, traits, warmth, humor)
- AND concatenate them with `\n\n---\n\n` separators

#### Scenario: Directory persona with missing files
- GIVEN a `persona/` directory exists but some expected files are missing
- WHEN the persona is resolved
- THEN the system SHALL load only the files that exist
- AND concatenate them in the defined order

### Requirement: Persona Resolution Order

The system SHALL resolve persona by attempting the single file format first, then falling back to the directory format.

#### Scenario: Single file takes priority
- GIVEN both `persona.md` and `persona/` directory exist
- WHEN the persona is resolved
- THEN the system SHALL use `persona.md` and SHALL NOT load the `persona/` directory

#### Scenario: Fallback to directory
- GIVEN `persona.md` does not exist but `persona/` directory exists
- WHEN the persona is resolved
- THEN the system SHALL fall back to loading the `persona/` directory

#### Scenario: No persona found
- GIVEN neither `persona.md` nor `persona/` directory exists
- WHEN the persona is resolved
- THEN the system SHALL return `ErrPersonaNotFound`
- AND the agent SHALL use a minimal default identity

---

## 01 Persona Loading

### 2.1 Resolution

```go
type PersonaResolver struct {
    ProfileDir string
}

func (r *PersonaResolver) Resolve() (string, error) {
    // Try single file first
    singleFile := filepath.Join(r.ProfileDir, "persona.md")
    if content, err := os.ReadFile(singleFile); err == nil {
        return string(content), nil
    }

    // Fall back to directory
    dir := filepath.Join(r.ProfileDir, "persona")
    files := []string{"core.md", "traits.md", "warmth.md", "humor.md"}
    var parts []string

    for _, f := range files {
        path := filepath.Join(dir, f)
        if content, err := os.ReadFile(path); err == nil {
            parts = append(parts, string(content))
        }
    }

    if len(parts) > 0 {
        return strings.Join(parts, "\n\n---\n\n"), nil
    }

    return "", ErrPersonaNotFound
}
```

### 2.2 Persona Resolution with Frontmatter

```go
type PersonaConfig struct {
    Name       string `yaml:"name"`
    Style      string `yaml:"style"`
    Humor      string `yaml:"humor"`
    Verbosity  string `yaml:"verbosity"`
    Empathy    string `yaml:"empathy"`
    Directness string `yaml:"directness"`
    Creativity string `yaml:"creativity"`
    Adaptation string `yaml:"adaptation"`
}

type PersonaResolver struct {
    ProfileDir string
}

func (r *PersonaResolver) Resolve() (string, error) {
    singleFile := filepath.Join(r.ProfileDir, "persona.md")
    content, err := os.ReadFile(singleFile)
    if err != nil {
        return r.resolveDirectory()
    }

    // Parse frontmatter if present
    config, body, err := parseFrontmatter(content)
    if err != nil {
        return string(content), nil
    }

    dimInstructions := generateDimensionInstructions(config)
    return dimInstructions + "\n\n" + body, nil
}

func generateDimensionInstructions(config PersonaConfig) string {
    var instructions []string

    switch config.Style {
    case "butler":
        instructions = append(instructions, "You are a discreet, anticipatory butler.")
    case "coach":
        instructions = append(instructions, "You are an encouraging, direct coach.")
    }

    switch config.Humor {
    case "none":
        instructions = append(instructions, "Do not use humor or jokes.")
    case "low":
        instructions = append(instructions, "Use humor sparingly and naturally.")
    case "high":
        instructions = append(instructions, "Feel free to use humor and wit when appropriate.")
    }

    switch config.Verbosity {
    case "concise":
        instructions = append(instructions, "Keep responses brief and to the point.")
    case "detailed":
        instructions = append(instructions, "Provide thorough, detailed explanations.")
    }

    switch config.Adaptation {
    case "mirror":
        instructions = append(instructions, "Match the user's communication style and pace.")
    case "consistent":
        instructions = append(instructions, "Maintain a consistent personality regardless of user tone.")
    }

    return strings.Join(instructions, "\n")
}
```

### 2.3 System Prompt Construction

```go
func (a *Agent) buildSystemPrompt() string {
    var parts []string

    // 1. Persona (always first — defines who the agent is)
    persona, err := a.resolver.ResolvePersona()
    if err == nil {
        parts = append(parts, "## Identity\n"+persona)
    }

    // 2. Active skill's SYSTEM.md (role-specific instructions)
    for _, skill := range a.activeSkills {
        if systemPrompt, err := skill.SystemPrompt(); err == nil {
            parts = append(parts, "## "+skill.Name()+" Instructions\n"+systemPrompt)
        }
    }

    // 3. Context files (AGENTS.md, project conventions)
    for _, cf := range a.config.ContextFiles {
        parts = append(parts, "## "+cf.Name+"\n"+cf.Content)
    }

    // 4. Session-specific instructions
    if a.sessionInstructions != "" {
        parts = append(parts, "## Current Session\n"+a.sessionInstructions)
    }

    return strings.Join(parts, "\n\n")
}
```

### Requirement: Persona Resolution

The system SHALL resolve persona content by attempting to read a single `persona.md` file and falling back to a `persona/` directory.

#### Scenario: Resolve persona from single file
- GIVEN a `persona.md` file exists in the profile directory
- WHEN `PersonaResolver.Resolve()` is called
- THEN the system SHALL return the file content
- AND SHALL NOT attempt to load the `persona/` directory

#### Scenario: Resolve persona from directory
- GIVEN `persona.md` does not exist but `persona/` directory exists
- WHEN `PersonaResolver.Resolve()` is called
- THEN the system SHALL load the ordered files from the `persona/` directory
- AND concatenate them with separators

### Requirement: Frontmatter Dimension Processing

The system SHALL parse YAML frontmatter from persona files and generate dimension-based system prompt instructions.

#### Scenario: Frontmatter parsed into dimension instructions
- GIVEN a `persona.md` file contains YAML frontmatter with `style`, `humor`, `verbosity`, and `adaptation` fields
- WHEN the persona is resolved
- THEN the system SHALL parse the frontmatter into a `PersonaConfig` struct
- AND generate dimension instructions using `generateDimensionInstructions()`
- AND prepend the dimension instructions to the persona body

#### Scenario: Preset style sets dimension defaults
- GIVEN a persona has `style: butler` in frontmatter
- WHEN dimension instructions are generated
- THEN the style preset SHALL set default values for humor, verbosity, empathy, directness, and creativity
- AND individual dimension values SHALL override the preset defaults

### Requirement: System Prompt Construction

The system SHALL construct the system prompt with persona as the first block, followed by active skills, context files, and session-specific instructions.

#### Scenario: Persona is first in system prompt
- GIVEN a persona is resolved successfully
- WHEN the system prompt is constructed via `buildSystemPrompt()`
- THEN the persona block SHALL appear first under `## Identity`

#### Scenario: Graceful handling of missing persona
- GIVEN the persona resolver returns `ErrPersonaNotFound`
- WHEN the system prompt is constructed
- THEN the system SHALL omit the `## Identity` block
- AND continue constructing the prompt with remaining blocks

---

## 02 Persona as Config

### 3.1 Agent Config Reference

```yaml
# ~/.opennavi/profiles/<name>/agents.yaml
agents:
  default:
    type: primary
    persona: "persona.md"    # or "persona/" for directory
    # ...
```

### 3.2 Profile-Level Default

```yaml
# ~/.opennavi/profiles/<name>/settings.yaml
persona: "persona.md"  # default for all agents in this profile
```

### 3.3 Agent Override

Individual agents can override the profile default:

```yaml
agents:
  default:
    type: primary
    persona: "persona.md"

  coder:
    type: sub
    persona: "persona/coder.md"  # different persona for this sub-agent
```

### Requirement: Agent-Level Persona Configuration

The system SHALL allow each agent in an `agents.yaml` configuration to specify its own persona file or directory path.

#### Scenario: Agent specifies persona
- GIVEN an agent configuration includes a `persona` field set to `"persona.md"`
- WHEN the agent initializes
- THEN the system SHALL resolve the persona from the specified path

#### Scenario: Agent omits persona
- GIVEN an agent configuration does not include a `persona` field
- WHEN the agent initializes
- THEN the system SHALL use the profile-level default persona

### Requirement: Profile-Level Persona Default

The system SHALL support a profile-level persona default in `settings.yaml` that applies to all agents in the profile.

#### Scenario: Profile default persona applies to all agents
- GIVEN `settings.yaml` defines `persona: "persona.md"` as the profile default
- WHEN an agent without its own `persona` field initializes
- THEN the system SHALL use the profile default persona

#### Scenario: Agent override takes precedence
- GIVEN `settings.yaml` defines a profile default persona
- AND an agent configuration specifies a different `persona` field
- WHEN the agent initializes
- THEN the agent-level persona SHALL override the profile default

---

## 03 Persona CLI

```bash
# View current persona
opennavi persona show

# Edit persona (opens in editor)
opennavi persona edit

# Switch persona (for primary agents)
opennavi persona use persona.md
opennavi persona use persona/

# List available personas
opennavi persona list

# Create a new persona
opennavi persona create my-style
```

### Requirement: Persona CLI Commands

The system SHALL provide CLI commands for viewing, editing, switching, listing, and creating personas.

#### Scenario: View current persona
- GIVEN the user runs `opennavi persona show`
- WHEN the command is executed
- THEN the system SHALL display the current active persona content

#### Scenario: Edit persona
- GIVEN the user runs `opennavi persona edit`
- WHEN the command is executed
- THEN the system SHALL open the persona file in the user's default editor

#### Scenario: Switch persona
- GIVEN the user runs `opennavi persona use persona.md`
- WHEN the command is executed
- THEN the system SHALL switch the active persona to the specified file

#### Scenario: List available personas
- GIVEN the user runs `opennavi persona list`
- WHEN the command is executed
- THEN the system SHALL display all available persona files

#### Scenario: Create a new persona
- GIVEN the user runs `opennavi persona create my-style`
- WHEN the command is executed
- THEN the system SHALL create a new persona file with the given name

### Requirement: Primary Agent Persona Switching

The system SHALL restrict the `persona use` command to primary agents, applying the persona change at the agent level.

#### Scenario: Primary agent persona switch
- GIVEN the user runs `opennavi persona use persona.md` on a primary agent
- WHEN the command is executed
- THEN the system SHALL switch the active persona to the specified file or directory

#### Scenario: Persona switching targets agent config
- GIVEN a persona switch is performed via `opennavi persona use persona.md`
- WHEN the command is executed
- THEN the system SHALL update the agent's persona configuration to the specified path

---

## 04 Key Design Decisions

1. **`persona.md` or `persona/`** — flexible format, single file for simple use, directory for detailed personality.
2. **Natural name** — "persona" is clear, professional, and doesn't sound like AI psychosis.
3. **Loaded into system prompt** — persona is the first block in the system prompt, defining the agent's identity.
4. **Per-agent override** — each agent can have its own persona.
5. **Profile-level default** — profile default persona applies to all agents unless overridden.
6. **CLI support** — `opennavi persona` commands for easy management.
7. **Graceful fallback** — if persona is missing, agent uses a minimal default identity.
8. **Configurable frontmatter dimensions** — YAML frontmatter maps to system prompt instructions. No special runtime enforcement, just text generation shaping.
9. **Presets as defaults** — preset style sets defaults for all dimensions; individual dimensions override.

## 05 Gaps / Future Work

- [ ] **Contex-aware tone adaptation** — persona adapts based on conversation context (e.g., serious during incidents, relaxed during casual chat)
- [ ] **Mood mirroring** — persona detects and mirrors user mood from text cues
- [ ] **Dynamic persona switching** — switch persona mid-session based on task type (e.g., "switch to on-call mode")
- [ ] **Avatar state integration** — persona dimensions drive avatar expression and gesture (see `avatar/` spec)
- [ ] **Persona templates** — community-shared persona templates with dimension presets


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S020100 | 0.1.0 | Migrated from docs/spec/ to spec-tree |