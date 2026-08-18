---
status: draft
created: '2026-08-14'
updated: '2026-08-14'
tags:
  - spec-tree
  - feature
  - creative-pipeline
priority: high
spec-refs: [110000, 110001, 110002, 110003, 110004, 110005]
---

# 00 Asset Submission & Validation

> Asset submission CLI, validation rules engine, budget configuration, and structured validation reports for the creative pipeline.

**Status:** Draft  
**Last updated:** 2026-08-14  
**Spec code:** S110000

---

## 00 Asset Submission CLI

The submission CLI (`opennavi asset submit`) is the primary entry point for artists to submit creative assets to the pipeline. It accepts a file path and optional metadata file, validates metadata completeness, and triggers the validation pipeline.

### Asset Types

The pipeline supports the following asset types with corresponding formats, validation rules, and integration steps:

| Type | Formats | Validation | Integration |
|------|---------|------------|-------------|
| **3D Mesh** | FBX, glTF 2.0, OBJ | Poly count, vertex count, UV layout, normals, skeleton | LOD gen, collision mesh, tangent space calc |
| **Texture** | PNG, TGA, EXR, PSD, TIFF | Resolution, channel count, format, mip chain | Compression (BC7/ASTC/ETC2), material instantiation |
| **Animation** | FBX, glTF, BVH, raw tracks | Bone count, skeleton match, keyframe count, duration | Retargeting, compression, blending |
| **Audio** | WAV, FLAC, MP3, OGG | Sample rate, bit depth, format, duration | SoundBank build (FMOD/Wwise), compression |
| **VFX/Shader** | HLSL, GLSL, graph files | Syntax check, parameters, target platform | Compilation, permutation generation |
| **Material** | JSON, MDL, Substance | Parameters, texture references, shader binding | Material instantiation, parameter baking |

### Non-Functional Targets

| Category | Requirement | Target |
|----------|-------------|--------|
| **Format Support** | Mesh formats | FBX, glTF 2.0 (import), glTF 2.0 (export) |
| | Texture formats | All common formats in; BC7/ASTC/ETC2 out |
| | Audio formats | WAV, FLAC, MP3, OGG in; target platform out |
| **Performance** | Validation time | < 5s per asset |
| **Concurrency** | Parallel validation | Up to 8 concurrent asset validations |
| **Reliability** | Validation false positive | < 1% |

### Requirement: CLI Submission Command

The system SHALL provide an `opennavi asset submit` CLI command that accepts a file path and optional metadata file, parses the metadata, detects asset type from file extension when not specified, validates metadata completeness, and triggers the validation pipeline.

#### Scenario: Valid asset submission with metadata
- GIVEN an artist invokes `opennavi asset submit character-01.fbx --metadata character-01-metadata.yaml`
- WHEN the CLI parses the metadata file and validates completeness
- THEN the asset SHALL be accepted and the validation pipeline SHALL begin
- AND the exit code SHALL indicate success (0) when validation passes

#### Scenario: Asset submission without metadata
- GIVEN an artist invokes `opennavi asset submit texture-01.png` without a metadata file
- WHEN the CLI detects the asset type from the file extension
- THEN the asset SHALL be accepted using default metadata inferred from extension
- AND the validation pipeline SHALL proceed with automatic type detection

#### Scenario: Invalid file path
- GIVEN an artist invokes `opennavi asset submit nonexistent.fbx`
- WHEN the CLI checks the file path
- THEN the system SHALL return an error with a clear message
- AND exit with a non-zero code

### Requirement: Asset Type Detection

The system SHALL detect asset type from file extension when the metadata file does not specify it, supporting the defined set of asset types and formats.

#### Scenario: Type detection from extension
- GIVEN an asset is submitted as `character-01.fbx` without type specified
- WHEN the CLI processes the file extension
- THEN the asset type SHALL be detected as "3D Mesh"
- AND validation SHALL proceed with mesh-specific rules

#### Scenario: Unknown extension
- GIVEN an asset is submitted with an unsupported extension (e.g., `.custom`)
- WHEN the CLI attempts type detection
- THEN the system SHALL return an error indicating unsupported format
- AND exit with a non-zero code

### User Stories

- **US-01:** As an artist, I want to submit a 3D model to the pipeline so it can be validated and integrated into the game engine.
- **US-02:** As an audio designer, I want to submit audio assets with metadata (loop points, attenuation, category) so they are processed into the SoundBank.
- **US-03:** As a texture artist, I want to submit texture maps with metadata (channel packing, usage flags) so they are compressed and materialized correctly.
- **US-04:** As a technical artist, I want to submit rigged characters with skeleton definitions so the pipeline can validate bone matching and retarget animations.

---

## 01 Validation Rules Engine

The validation rules engine provides a pluggable rule registry where each rule implements a common interface and returns a structured result. Rules are grouped by category and applied based on the asset type.

### Rule Interface

```go
type ValidationRule interface {
    Name() string
    Category() string   // "format", "size", "naming", "skeleton", "texture", "animation"
    Validate(path string, metadata AssetMetadata) ValidationResult
}

type ValidationResult struct {
    Passed    bool
    Severity  string   // "error", "warning"
    Message   string
    Details   map[string]interface{}
}
```

### Built-in Rules

| Category | Rule | Checks |
|----------|------|--------|
| Format | FileExtension | Allowed extensions per asset type |
| | FormatVersion | Version compatibility (glTF 2.0, FBX 2020) |
| | InternalConsistency | No missing references, valid hierarchy |
| Size | PolyCount | Triangle/vertex count vs budget |
| | TextureResolution | Width/height vs max, power-of-two |
| | FileSize | Disk size vs budget |
| Naming | ConventionMatch | Matches project naming convention (regex) |
| | NoSpecialChars | No spaces, unicode, or invalid chars |
| | UniqueName | No duplicate asset names in project |
| Skeleton | BoneCount | Bone count vs skeleton definition |
| | BoneNames | Bone names match skeleton spec |
| | Hierarchy | Skeleton hierarchy matches reference |
| Texture | Dimension | Power-of-two, square only flag |
| | ChannelCount | RGB vs RGBA vs required channels |
| | MipChain | Mip chain present vs required |
| Animation | BoneCount | Animated bones vs skeleton |
| | KeyframeCount | Min/max keyframes per track |
| | Duration | Min/max duration bounds |

### Requirement: Validation Rule Interface

The system SHALL define a `ValidationRule` interface with `Name()`, `Category()`, and `Validate()` methods, and a `ValidationResult` struct with `Passed`, `Severity`, `Message`, and `Details` fields.

#### Scenario: Rule registration
- GIVEN a validation rule implements the `ValidationRule` interface
- WHEN the rule is registered in the rule registry
- THEN the system SHALL accept the rule and apply it during validation of matching asset types

#### Scenario: Rule validation result
- GIVEN a validation rule is executed against an asset
- WHEN the rule completes
- THEN the system SHALL return a `ValidationResult` with `Passed` (bool), `Severity` ("error" or "warning"), `Message` (actionable text), and `Details` (structured map)

### Requirement: Built-in Validation Rules

The system SHALL include built-in validation rules for format, size, naming, skeleton, texture, and animation categories, covering at minimum the rules defined in the rules table above.

#### Scenario: PolyCount validation
- GIVEN a mesh asset with 120,000 triangles against a budget of 100,000
- WHEN the PolyCount rule validates
- THEN the rule SHALL return a `ValidationResult` with `Passed: false` and `Severity: "error"`
- AND the message SHALL indicate the actual count and the limit

#### Scenario: Naming convention validation
- GIVEN an asset with a name containing spaces (e.g., `"my asset.fbx"`)
- WHEN the NoSpecialChars rule validates
- THEN the rule SHALL return a `ValidationResult` with `Passed: false`
- AND the message SHALL indicate which characters are invalid

#### Scenario: Skeleton bone match
- GIVEN a rigged character with 58 bones against a skeleton definition expecting 64
- WHEN the BoneCount rule validates
- THEN the rule SHALL return a `ValidationResult` with `Passed: false`
- AND the message SHALL list the missing bones

### Requirement: Validation as Gating Stage

The system SHALL require that all validation rules pass (no errors) before an asset proceeds to the integration pipeline. No integration SHALL run on unvalidated assets.

#### Scenario: Validated asset proceeds to integration
- GIVEN an asset passes all validation rules with no errors
- WHEN the validation stage completes
- THEN the asset SHALL be eligible for the integration pipeline

#### Scenario: Failed validation blocks integration
- GIVEN an asset fails one or more validation rules with severity "error"
- WHEN the validation stage completes
- THEN the asset SHALL NOT proceed to the integration pipeline
- AND the validation report SHALL describe all errors

### User Stories

- **US-05:** As a technical artist, I want the pipeline to validate format, poly count, texture resolution, and skeleton match so I catch errors before integration.
- **US-07:** As an artist, I want validation errors to include specific, actionable messages so I can fix issues quickly.

---

## 02 Budget Configuration

Asset budgets are defined in a `asset-budget.yaml` file at the project root. Budget configuration controls validation thresholds for poly count, texture resolution, skeleton matching, and animation parameters.

### Budget Configuration Format

```yaml
# project/example-project/asset-budget.yaml
budget:
  poly_count:
    max_triangles: 100000
    max_vertices: 200000
  texture:
    max_resolution: 4096
    require_power_of_two: true
    max_file_size_mb: 50
  skeleton:
    max_bones: 64
    require_hierarchy_match: true
  animation:
    max_duration_seconds: 30
    min_keyframes: 2
```

### Requirement: Budget Configuration Loading

The system SHALL load budget configuration from an `asset-budget.yaml` file at the project root, with fallback to sensible defaults when the file is missing or partially configured.

#### Scenario: Budget config loaded from project root
- GIVEN an `asset-budget.yaml` file exists at the project root
- WHEN the validation pipeline starts
- THEN the system SHALL load the budget configuration from the file
- AND apply the defined thresholds to all validation rules

#### Scenario: Missing budget config uses defaults
- GIVEN no `asset-budget.yaml` exists at the project root
- WHEN the validation pipeline starts
- THEN the system SHALL use default budget thresholds
- AND SHALL NOT return an error for the missing file

#### Scenario: Partial budget config merges with defaults
- GIVEN `asset-budget.yaml` defines only `poly_count.max_triangles`
- WHEN the validation pipeline starts
- THEN the system SHALL use the specified poly count limit
- AND SHALL use default values for all other budget categories

### User Stories

- **US-06:** As a producer, I want the pipeline to check asset budgets (GPU, memory, disk) against project targets so we stay within performance constraints.

---

## 03 Validation Report Output

The validation pipeline produces a structured validation report (JSON/YAML) with passed/failed rules, severity, actionable messages, and details per rule. Exit codes communicate overall results: 0 (all pass), 1 (warnings only), 2 (errors).

### Report Structure

```json
{
  "asset": "character-01.fbx",
  "status": "failed",
  "rules_passed": 14,
  "rules_failed": 2,
  "rules": [
    {
      "name": "PolyCount",
      "category": "size",
      "passed": false,
      "severity": "error",
      "message": "Poly count 120,000 exceeds budget of 100,000 triangles",
      "details": { "actual": 120000, "limit": 100000 }
    },
    {
      "name": "BoneCount",
      "category": "skeleton",
      "passed": false,
      "severity": "error",
      "message": "Bone count 58 does not match expected 64",
      "details": { "actual": 58, "expected": 64 }
    }
  ]
}
```

### Requirement: Structured Validation Report

The system SHALL produce a structured validation report in JSON or YAML format containing per-rule results with `name`, `category`, `passed`, `severity`, `message`, and `details` fields.

#### Scenario: Validation report output
- GIVEN an asset has been validated through the rules engine
- WHEN the validation completes
- THEN the system SHALL output a structured report with all rule results
- AND each rule result SHALL include `name`, `category`, `passed`, `severity`, `message`, and `details` fields

### Requirement: Exit Codes

The system SHALL use exit codes to communicate validation results: 0 for all passed, 1 for warnings only (no errors), 2 for errors present.

#### Scenario: All rules pass
- GIVEN an asset passes all validation rules
- WHEN the validation pipeline exits
- THEN the exit code SHALL be 0

#### Scenario: Warnings only
- GIVEN an asset passes all error-level rules but has warnings
- WHEN the validation pipeline exits
- THEN the exit code SHALL be 1

#### Scenario: Errors present
- GIVEN an asset fails one or more error-level rules
- WHEN the validation pipeline exits
- THEN the exit code SHALL be 2

---

## 04 Metadata File Format

Assets are submitted with an optional metadata file that specifies asset type, category, skeleton reference, budget profile, and author information. The metadata is validated for completeness before the validation pipeline runs.

### Metadata Format

```yaml
# Example: character-01-metadata.yaml
asset:
  type: mesh
  category: character
  name: "character-01"
  skeleton: "humanoid-base"
  budget_profile: "high-poly-character"
  author: "artist-01"
  notes: "Main player character, v2"
```

### Requirement: Metadata Parsing and Validation

The system SHALL parse the metadata file (YAML or JSON) and validate its completeness before proceeding with the validation pipeline. Required fields depend on the asset type.

#### Scenario: Complete metadata is accepted
- GIVEN a metadata file with all required fields for the asset type
- WHEN the metadata is parsed
- THEN the system SHALL accept the metadata
- AND proceed to the validation pipeline

#### Scenario: Incomplete metadata is rejected
- GIVEN a metadata file missing required fields for the asset type
- WHEN the metadata is parsed
- THEN the system SHALL return an error describing the missing fields
- AND exit with a non-zero code before validation begins

---

## 05 Out of Scope

The following are explicitly out of scope for this feature:

- **Agents never create art** — 3D modeling, sculpting, UV mapping, audio composition, animation keyframing, texture painting, material authoring, VFX authoring, shader graph editing, level design, and world building are human-only activities
- Asset version control (handled by Git)
- Game engine runtime asset loading (handled by the engine)
- Procedural generation or auto-composition of any kind

---

## Implementation Notes

### Phase 1: Asset Validation + Submission CLI

**Goal:** Artists can submit assets and receive validation results. No integration or preview yet.

**Deliverables:**
- [ ] `opennavi asset submit` command with metadata parsing
- [ ] Validation rules engine with all built-in rules
- [ ] Budget configuration loading from `asset-budget.yaml`
- [ ] Structured validation report output
- [ ] Integration tests for each validation rule

**Implementation details:**
- Use Go standard library for CLI (cobra/pflag)
- Define `ValidationRule` interface and `ValidationResult` struct
- Implement rule registry with category grouping
- Build all rules: FileExtension, FormatVersion, InternalConsistency, PolyCount, TextureResolution, FileSize, ConventionMatch, NoSpecialChars, UniqueName, BoneCount, BoneNames, Hierarchy, Dimension, ChannelCount, MipChain, KeyframeCount, Duration
- Load budget configuration from `asset-budget.yaml` at project root
- Return structured validation report (JSON/YAML) with passed/failed rules
- Exit codes: 0 (all pass), 1 (warnings only), 2 (errors)

---

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-14 | S110000 | 0.1.0 | Migrated from deprecated product-spec files |