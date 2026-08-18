---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - agent-config
priority: medium
spec-refs: [020000, 020001, 020002, 020003, 020004, 020005, 020006, 020007, 020008]
---

# Skills System

**Status:** Draft  
**Last updated:** 2026-07-29

---

## 00 Overview

Skills provide role-specific instructions that shape how the agent behaves. Unlike personas (which define *who* the agent is), skills define *what* the agent can do in specific roles.

**Two-tier loading model:**
- **Compiled-in skills** — loaded from `skills/` directory at project root, compiled into binary at build time (verifiable)
- **Runtime-loaded skills** — loaded from `.config/navi/$NAVI_NAME/skills/` at runtime (user-customized, not verified)

This creates a clear trust boundary: compiled-in skills are part of the verifiable binary, while runtime skills allow end-user customization without recompilation.

### 1.1 Skills vs. Personas vs. Extensions

| Concept | Purpose | Compiled In? | Loaded At | Verified? |
|---------|---------|--------------|-----------|-----------|
| **Persona** | Who the agent is (tone, style) | Yes (navi definition) | Build time | Yes |
| **Compiled-in Skill** | What role the agent plays (developer-defined) | Yes (project root `skills/`) | Build time | Yes |
| **Runtime Skill** | What role the agent plays (user-customized) | No (`.config/navi/$NAVI_NAME/skills/`) | Runtime | No |
| **Extension** | What tools/hooks the agent has | No (standalone binary) | Runtime | Yes (signed) |

A skill typically references a set of tools (compiled into the navi or provided by extensions) and provides the instructions for using them effectively.

### 1.2 Two-Tier Loading Priority

When both compiled-in and runtime skills are active:
1. **Compiled-in skills** load first (higher priority, part of verifiable binary)
2. **Runtime skills** load second (user-customized, appended to system prompt)
3. If a runtime skill has the same `Name` as a compiled-in skill, it overrides that skill's instructions
4. Runtime skills can add new skills not present in the compiled set

### Requirement: Two-Tier Skill Loading

The system SHALL implement a two-tier skill loading model with compiled-in skills (loaded from the project `skills/` directory at build time) and runtime-loaded skills (loaded from `.config/navi/$NAVI_NAME/skills/` at runtime).

#### Scenario: Compiled-in skills load first
- GIVEN the system has both compiled-in and runtime skills available
- WHEN the system prompt is constructed
- THEN compiled-in skills SHALL be loaded first as the higher-priority base
- AND runtime skills SHALL be appended second

#### Scenario: Runtime skill overrides compiled-in skill by name
- GIVEN a runtime skill and a compiled-in skill share the same `Name`
- WHEN the system prompt is constructed
- THEN the runtime skill's instructions SHALL override the compiled-in skill's instructions

### Requirement: Trust Boundary

The system SHALL maintain a clear trust boundary between verified compiled-in skills and unverified runtime skills.

#### Scenario: Compiled-in skills are verified
- GIVEN a skill is loaded from the project `skills/` directory at build time
- WHEN the skill is compiled into the binary
- THEN the skill SHALL be part of the signed, verifiable binary

#### Scenario: Runtime skills are not verified
- GIVEN a skill is loaded from `.config/navi/$NAVI_NAME/skills/` at runtime
- WHEN the skill is injected into the system prompt
- THEN the skill SHALL be marked as unverified and user-modifiable

---

## 01 Skill Definition

Skills are defined in the navi definition using Go structs:

```go
skills: []SkillDefinition{
    {
        Name: "project-management",
        Instructions: `You manage software projects via GitHub issues and project boards.
When asked to check status, query the project board first. For new issues,
always assign appropriate labels and priority.`,
        Keywords: []string{"issue", "PR", "project management", "review"},
    },
    {
        Name: "code-review",
        Instructions: `You review code changes. When asked to review a PR, fetch the diff
and provide structured feedback on architecture, correctness, and style.`,
        Keywords: []string{"review", "PR", "pull request"},
    },
}
```

### 2.1 Skill Definition Fields

| Field | Description | Required |
|-------|-------------|----------|
| `Name` | Unique identifier for the skill | Yes |
| `Instructions` | Role-specific system prompt text | Yes |
| `Keywords` | Triggers that auto-activate this skill | No (defaults to empty) |
| `Priority` | Higher = more important when multiple skills activate | No (defaults to 0) |

### 2.2 Skill Definition Methods

Skills can be defined in two ways:

**Inline in Go code** (compiled into binary):
```go
skills: []SkillDefinition{
    {
        Name: "on-call-engineer",
        Instructions: `You respond to on-call alerts...`,
        Keywords: []string{"alert", "incident"},
    },
}
```

**External file** (loaded at build time, compiled into binary):
```go
skills: []SkillDefinition{
    {
        Name: "on-call-engineer",
        Instructions: "file:./skills/oncall/SYSTEM.md",  // loaded at build time
        Keywords: []string{"alert", "incident"},
    },
}
```

Both approaches result in the skill being **compiled into the binary** — there's no runtime file loading.

### Requirement: Skill Definition Structure

The system SHALL define skills via a `SkillDefinition` struct with fields for `Name`, `Instructions`, `Keywords`, and `Priority`.

#### Scenario: Define a skill with all required fields
- GIVEN a skill is being defined with a `Name` and `Instructions`
- WHEN the skill definition is processed
- THEN the `Name` field SHALL be a unique identifier for the skill
- AND the `Instructions` field SHALL contain the role-specific system prompt text

#### Scenario: Keywords and Priority are optional
- GIVEN a skill is defined without `Keywords` or `Priority`
- WHEN the skill definition is processed
- THEN `Keywords` SHALL default to an empty slice
- AND `Priority` SHALL default to zero

### Requirement: Skill Definition Methods

The system SHALL support two methods for defining skills: inline in Go code and via external file reference, both resulting in build-time compilation into the binary.

#### Scenario: Inline skill definition
- GIVEN a skill is defined inline in Go code with `Instructions` as a string literal
- WHEN `sdk.Build()` is invoked
- THEN the skill SHALL be compiled into the binary

#### Scenario: External file skill definition
- GIVEN a skill is defined with `Instructions` referencing an external file path via `"file:./path/to/file"`
- WHEN `sdk.Build()` is invoked
- THEN the system SHALL load the file contents at build time
- AND the skill SHALL be compiled into the binary with the loaded instructions

---

## 02 Skill Compilation

Skills are compiled into the navi binary at build time via `sdk.Build()`. The builder:

1. Reads skill definitions from the navi definition (Go code or external files)
2. Validates skill names for uniqueness
3. Embeds skill instructions and keywords into the system prompt template
4. Produces a single binary with skills baked in

```go
// At build time (in sdk.Build())
func compileSkills(skills []SkillDefinition) ([]CompiledSkill, error) {
    var compiled []CompiledSkill
    
    for _, s := range skills {
        // Load instructions from file or inline string
        instructions := loadInstructions(s.Instructions)
        
        compiled = append(compiled, CompiledSkill{
            Name:       s.Name,
            Instructions: instructions,
            Keywords:   s.Keywords,
            Priority:   s.Priority,
        })
    }
    
    // Validate uniqueness
    if hasDuplicateNames(compiled) {
        return nil, fmt.Errorf("duplicate skill names")
    }
    
    return compiled, nil
}
```

**Key point:** Skills are **not loaded from disk at runtime**. They're part of the binary's embedded data. This means:
- No file system access needed for skills
- No permission issues for skill files
- Skills can't be modified by end users after build (intentional — they're part of the navi definition)

### Requirement: Build-Time Compilation

The system SHALL compile skills into the navi binary at build time via `sdk.Build()`, embedding instructions and keywords into the system prompt template.

#### Scenario: Skills are compiled at build time
- GIVEN skill definitions are provided in the navi definition
- WHEN `sdk.Build()` is invoked
- THEN the system SHALL read all skill definitions
- AND compile them into the binary as embedded data
- AND produce a single binary with skills baked in

#### Scenario: Skills are not loaded from disk at runtime
- GIVEN a navi binary has been built with compiled skills
- WHEN the navi runs
- THEN the system SHALL NOT load skills from filesystem at runtime
- AND the skills SHALL be part of the binary's embedded data

### Requirement: Skill Name Uniqueness Validation

The system SHALL validate that all skill names are unique during compilation.

#### Scenario: Duplicate skill names are rejected
- GIVEN two skill definitions share the same `Name`
- WHEN `compileSkills()` is invoked
- THEN the system SHALL return an error indicating duplicate skill names
- AND the build SHALL fail

#### Scenario: Unique skill names compile successfully
- GIVEN all skill definitions have unique `Name` values
- WHEN `compileSkills()` is invoked
- THEN the system SHALL compile all skills without error

---

## 03 Skill Activation at Runtime

Skills are activated when their keyword triggers match the user's message:

```go
// At runtime (in agent loop)
func activateSkills(userMessage string, compiledSkills []CompiledSkill) []*CompiledSkill {
    var active []*CompiledSkill
    lower := strings.ToLower(userMessage)

    for i := range compiledSkills {
        skill := &compiledSkills[i]
        
        for _, keyword := range skill.Keywords {
            if strings.Contains(lower, strings.ToLower(keyword)) {
                active = append(active, skill)
                break
            }
        }
    }

    // Sort by priority (highest first)
    sort.Slice(active, func(i, j int) bool {
        return active[i].Priority > active[j].Priority
    })

    return active
}
```

**Key difference from pi-mono:** Skills are compiled into the binary, so activation is checking against embedded data structures, not scanning a directory.

Manual skill management (list/deactivate) is available via slash commands:

```
/skill list                    # List available skills in this navi
/skill deactivate <name>       # Deactivate a skill for current session
```

### Requirement: Keyword-Based Auto-Activation

The system SHALL activate skills at runtime by matching keyword triggers from the skill definition against the user's message.

#### Scenario: Skill activates on keyword match
- GIVEN a compiled skill has `Keywords` defined
- WHEN the user's message contains one of the skill's keywords (case-insensitive)
- THEN the skill SHALL be activated and included in the active skills list

#### Scenario: Multiple skills activate simultaneously
- GIVEN the user's message matches keywords from multiple compiled skills
- WHEN skill activation is evaluated
- THEN all matching skills SHALL be activated
- AND the active skills list SHALL be sorted by `Priority` in descending order

### Requirement: Manual Skill Management

The system SHALL provide slash commands for manual skill management during a session.

#### Scenario: List available skills
- GIVEN the user sends `/skill list`
- WHEN the command is processed
- THEN the system SHALL display all available skills in the current navi

#### Scenario: Deactivate a skill
- GIVEN the user sends `/skill deactivate <name>`
- WHEN the command is processed
- THEN the named skill SHALL be deactivated for the current session

---

## 04 System Prompt Integration

Skills are injected into the system prompt after persona but before context files:

```go
func buildSystemPrompt(persona string, activeSkills []*CompiledSkill) string {
    var parts []string

    // 1. Persona (always first — defines identity)
    if persona != "" {
        parts = append(parts, "## Identity\n"+persona)
    }

    // 2. Active skills (role-specific instructions)
    for _, skill := range activeSkills {
        parts = append(parts, "## "+skill.Name+" Instructions\n"+skill.Instructions)
    }

    return strings.Join(parts, "\n\n")
}
```

### Requirement: System Prompt Ordering

The system SHALL inject active skill instructions into the system prompt after the persona identity block and before context files.

#### Scenario: Skills are injected after persona
- GIVEN a persona is defined and active skills are present
- WHEN the system prompt is constructed
- THEN the persona identity block SHALL appear first
- AND each active skill's instructions SHALL appear after the persona block

#### Scenario: Skill instructions are labeled by name
- GIVEN an active skill with a `Name` of "code-review"
- WHEN the system prompt is constructed
- THEN the skill's instructions SHALL be prefixed with `## code-review Instructions`

### Requirement: Active Skills Only Injection

The system SHALL inject only active skills into the system prompt, excluding deactivated and non-matching skills.

#### Scenario: Deactivated skill is excluded
- GIVEN a skill has been deactivated via `/skill deactivate <name>`
- WHEN the system prompt is constructed
- THEN the deactivated skill's instructions SHALL NOT be included in the system prompt

#### Scenario: Non-matching skill is excluded
- GIVEN a compiled skill whose keywords do not match the user's message
- WHEN the system prompt is constructed
- THEN the non-matching skill SHALL NOT be included in the system prompt

---

## 05 Bundled Skills (Optional)

Navi developers can ship bundled skills with their navi definition:

```go
skills: []SkillDefinition{
    // Navi-specific skills compiled into the binary
    {
        Name: "on-call-engineer",
        Instructions: `You respond to on-call alerts...`,
        Keywords: []string{"alert", "incident"},
    },
}
```

Bundled skills are part of the navi definition — they're compiled into the binary, not loaded from a separate directory. End users get these skills automatically when they download and run the navi.

**Key point:** Unlike pi-mono's profile-scoped skills, navis have **navi-specific skills** that are always available (unless deactivated by the user).

### Requirement: Bundled Skill Availability

The system SHALL make bundled skills, defined in the navi definition, automatically available to end users without additional setup.

#### Scenario: Bundled skills are available by default
- GIVEN a navi definition includes `skills: []SkillDefinition{...}`
- WHEN the navi binary is built and run
- THEN the bundled skills SHALL be compiled into the binary
- AND end users SHALL have them available automatically

#### Scenario: Bundled skills can be deactivated
- GIVEN a bundled skill is active
- WHEN the user sends `/skill deactivate <name>`
- THEN the skill SHALL be deactivated for the current session

### Requirement: Bundled Skill Compilation

The system SHALL compile bundled skills from the navi definition into the binary as part of the navi's shipped behavior.

#### Scenario: Bundled skills are compiled into the binary
- GIVEN a navi definition includes bundled `SkillDefinition` entries
- WHEN `sdk.Build()` is invoked
- THEN the bundled skills SHALL be compiled into the binary as part of the navi definition
- AND SHALL NOT be loaded from a separate directory

#### Scenario: Bundled skills ship with the navi
- GIVEN a user downloads and runs the built navi
- WHEN the navi starts
- THEN the bundled skills SHALL be available automatically without additional installation

---

## 06 Runtime-Loaded Skills

End users can customize a navi's behavior by adding skill files to `.config/navi/$NAVI_NAME/skills/`. These skills are loaded at runtime, NOT compiled into the binary.

### 7.1 Where Runtime Skills Come From

```
.config/navi/oncall-bot/
├── skills/
│   ├── custom-alert-classifier.md    # User's own skill for specific alerts
│   └── internal-tool-usage.md        # How to use their private APIs
├── policy.yaml                       # Runtime permission overrides
├── persona.md                        # Optional personality override
└── settings.yaml                     # Navi-specific settings
```

### 7.2 Loading Process

At startup, the navi:
1. Loads compiled-in skills (from binary)
2. Scans `.config/navi/$NAVI_NAME/skills/` for `.md` files
3. Parses each file as a skill definition
4. Merges runtime skills with compiled-in skills (runtime overrides by Name)
5. Injects all active skills into system prompt context

### 7.3 Runtime Skill Format

Runtime skills follow the same format as compiled-in skills but are loaded from markdown files:

```markdown
<!-- .config/navi/oncall-bot/skills/custom-alert-classifier.md -->
# Custom Alert Classifier

You classify alerts based on your organization's specific severity definitions.
Use these criteria when categorizing incoming alerts:

## Severity Levels
- P0: Service down, data loss risk → immediate escalation
- P1: Degraded performance, no data loss → investigate within 15 minutes
- P2: Minor issues, no user impact → queue for next sprint
```

### 7.4 Trust Boundary

Runtime skills are **not verified** — they're loaded from disk at runtime and can be modified by end users. This is intentional:

| Aspect | Compiled-in Skill | Runtime Skill |
|--------|------------------|---------------|
| Location | `skills/` in project root | `.config/navi/$NAVI_NAME/skills/` |
| Verified? | Yes (part of signed binary) | No (user-modifiable) |
| Override priority | Higher (loads first) | Lower (loads second, overrides by Name) |
| Use case | Core behavior the developer ships | User customization for specific workflows |

**Key point:** Runtime skills allow end-user customization without recompilation. They augment or override compiled-in skills but are clearly marked as unverified in system prompt context.

### Requirement: Runtime Skill Loading

The system SHALL load runtime skill files from `.config/navi/$NAVI_NAME/skills/` at startup.

#### Scenario: Runtime skills are loaded from disk
- GIVEN a navi starts with `.md` files in `.config/navi/$NAVI_NAME/skills/`
- WHEN the navi initializes
- THEN the system SHALL scan the directory for `.md` files
- AND parse each file as a skill definition

#### Scenario: No runtime directory exists
- GIVEN `.config/navi/$NAVI_NAME/skills/` does not exist
- WHEN the navi initializes
- THEN the system SHALL proceed with only compiled-in skills
- AND SHALL NOT return an error

### Requirement: Runtime Skill Merging

The system SHALL merge runtime skills with compiled-in skills, allowing runtime skills to override compiled-in skills by name.

#### Scenario: Runtime skill overrides compiled-in skill
- GIVEN a runtime skill file has the same `Name` as a compiled-in skill
- WHEN skills are merged at startup
- THEN the runtime skill's instructions SHALL override the compiled-in skill's instructions

#### Scenario: Runtime skill adds new skill
- GIVEN a runtime skill file has a `Name` not present in compiled-in skills
- WHEN skills are merged at startup
- THEN the runtime skill SHALL be added to the active skills list

---

## 07 Implementation Order

1. **Skill struct** — Name, Instructions, Keywords, Priority (simplified from pi-mono's multi-file format)
2. **Skill compilation** — Build-time processing of skill definitions
3. **Auto-activation** — Keyword matching against user messages at runtime
4. **System prompt integration** — Inject skill instructions into prompt during agent loop
5. **Slash commands** — `/skill list/deactivate`
6. **Validation** — Check for duplicate names, missing required fields
7. **Runtime loading** — Scan `.config/navi/$NAVI_NAME/skills/` at startup

---

## 08 Key Design Decisions

1. **Compiled at build time, not runtime loaded** — Skills are part of the navi definition and compiled into the binary. No file system scanning or permission issues.
2. **Inline Go definitions or external files** — Developers can define skills directly in code or reference external files (both compiled in).
3. **Auto-activation via keywords** — Skills activate based on keyword matching. Simple, predictable behavior.
4. **Priority-based ordering** — When multiple skills activate, higher priority wins for conflicts.
5. **Bundled with the navi** — Skills are part of what the developer ships. Users get them automatically.
6. **Injected into system prompt** — Skills are just text in the system prompt. No special runtime handling needed beyond keyword matching.


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S020000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |