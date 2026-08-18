---
status: draft
created: '2026-08-14'
updated: '2026-08-14'
tags:
  - spec-tree
  - feature
  - creative-pipeline
priority: high
spec-refs: [110100, 110101, 110102, 110103, 110104, 110105, 110106, 110107]
---

# 01 Integration Pipeline

> DAG-based automated integration pipeline with LOD generation, collision mesh, texture compression, material instantiation, animation retargeting, and SoundBank build stages.

**Status:** Draft  
**Last updated:** 2026-08-14  
**Spec code:** S110100

---

## 00 DAG Execution Engine

The integration pipeline runs as a DAG (Directed Acyclic Graph) of processing stages, each producing artifacts. Stages execute in dependency order, enabling parallel execution of independent stages and graceful addition of new stages without reordering.

### Stage Interface

```go
type IntegrationStage interface {
    Name() string
    InputTypes() []string
    OutputTypes() []string
    Execute(input Asset, config StageConfig) ([]Artifact, error)
}
```

### Artifact Manifest

Integration produces a manifest file alongside the processed assets:

```json
{
  "asset_id": "character-01",
  "source": "artists/character-01.fbx",
  "stages": [
    { "stage": "lod_generation", "artifacts": ["lod0.glb", "lod1.glb", "lod2.glb"] },
    { "stage": "collision_mesh", "artifacts": ["collision.glb"] },
    { "stage": "texture_compression", "artifacts": ["albedo.ktx2", "normal.ktx2"] }
  ],
  "budget_report": { "poly_count": 85000, "texture_memory_mb": 24 }
}
```

### Integration Config

```yaml
# project/example-project/integration-config.yaml
integration:
  lod:
    levels: [0, 1, 2, 3]
    reduction: [0.0, 0.5, 0.25, 0.1]  # triangle ratio per level
  collision:
    type: "convex_hull"  # or "triangle_mesh"
    max_vertices: 64
  texture:
    formats: ["bc7", "astc_4x4"]
    mipmap: true
  animation:
    retarget_method: "ik"  # or "bone_mapping"
    compression: 0.9  # keyframe reduction ratio
```

### Non-Functional Targets

| Category | Requirement | Target |
|----------|-------------|--------|
| **Performance** | Integration time | < 60s per asset (LOD + collision + compression) |
| **Concurrency** | Parallel integration | Up to 4 concurrent integration pipelines |
| **Reliability** | Integration failure rate | < 2% on valid inputs |

### Requirement: DAG-Based Stage Execution

The integration pipeline SHALL execute stages as a DAG (Directed Acyclic Graph), where each stage declares its input and output types and stages run in dependency order.

#### Scenario: Stages execute in dependency order
- GIVEN a DAG with stages A → B → C where B depends on A and C depends on B
- WHEN the integration pipeline runs
- THEN stage A SHALL execute first
- AND stage B SHALL execute after A completes
- AND stage C SHALL execute after B completes

#### Scenario: Independent stages run in parallel
- GIVEN two stages that do not depend on each other
- WHEN the integration pipeline runs
- THEN the stages SHALL execute concurrently
- AND the pipeline SHALL limit concurrency to the configured maximum

#### Scenario: New stage added without reordering
- GIVEN a new stage D that depends on A but not on B or C
- WHEN the stage is added to the DAG
- THEN the pipeline SHALL execute D in parallel with B and C
- AND no existing stage ordering SHALL change

### Requirement: Stage Failure Handling

The system SHALL handle stage failures gracefully: when a stage fails, the pipeline SHALL stop, report the error, and SHALL NOT run dependent stages.

#### Scenario: Stage failure stops pipeline
- GIVEN stage B fails during execution
- WHEN the failure is detected
- THEN the pipeline SHALL stop processing
- AND stage C (which depends on B) SHALL NOT execute
- AND the error SHALL be reported in the manifest

### Requirement: Artifact Manifest

The system SHALL produce an integration manifest JSON file alongside the processed assets, listing all stages and their output artifacts.

#### Scenario: Manifest produced after integration
- GIVEN all integration stages complete successfully
- WHEN the pipeline finishes
- THEN the system SHALL output a manifest JSON file
- AND the manifest SHALL list each stage and its artifacts
- AND the manifest SHALL include the budget report

---

## 01 LOD Generation

LOD (Level of Detail) generation produces multiple mesh variants with decreasing triangle counts from a high-poly source mesh. The stage uses mesh simplification to produce LOD0 (full detail) through LODn (lowest detail) meshes.

### LOD Reduction Ratios

| LOD Level | Triangle Ratio | Use Case |
|-----------|---------------|----------|
| LOD0 | 1.0 (100%) | Close-up / hero view |
| LOD1 | 0.5 (50%) | Medium distance |
| LOD2 | 0.25 (25%) | Far distance |
| LOD3 | 0.1 (10%) | Culling threshold |

### Requirement: LOD Mesh Generation

The system SHALL generate LOD meshes from a high-poly source mesh by reducing triangle count according to configurable reduction ratios per level, preserving UVs and normals.

#### Scenario: LOD levels generated
- GIVEN a high-poly mesh with 100,000 triangles
- WHEN the LOD generation stage runs with reduction ratios [1.0, 0.5, 0.25, 0.1]
- THEN LOD0 SHALL have 100,000 triangles
- AND LOD1 SHALL have approximately 50,000 triangles
- AND LOD2 SHALL have approximately 25,000 triangles
- AND LOD3 SHALL have approximately 10,000 triangles

#### Scenario: UVs and normals preserved
- GIVEN a mesh with UV coordinates and vertex normals
- WHEN LOD generation runs
- THEN each LOD level SHALL preserve the UV layout
- AND each LOD level SHALL preserve vertex normals

### User Stories

- **US-08:** As a technical artist, I want the pipeline to auto-generate LOD levels so the engine can render at appropriate detail levels.

---

## 02 Collision Mesh

The collision mesh stage generates convex hull or triangle mesh collision geometry from the source mesh. This is used for physics collision detection in the game engine.

### Requirement: Collision Mesh Generation

The system SHALL generate collision meshes (convex hull or triangle mesh) from the source geometry, with configurable vertex limits and collision type.

#### Scenario: Convex hull generation
- GIVEN a source mesh and config `type: "convex_hull"` with `max_vertices: 64`
- WHEN the collision mesh stage runs
- THEN the system SHALL generate a convex hull that wraps the source mesh
- AND the hull vertex count SHALL not exceed the configured maximum

#### Scenario: Triangle mesh collision
- GIVEN a source mesh and config `type: "triangle_mesh"`
- WHEN the collision mesh stage runs
- THEN the system SHALL generate a simplified triangle mesh collision
- AND the output SHALL have no self-intersections

### User Stories

- **US-09:** As a technical artist, I want the pipeline to generate collision meshes from the source geometry.

---

## 03 Texture Compression

The texture compression stage converts source textures to target GPU formats (BC7, ASTC, ETC2) with mip chain generation, reducing file size and enabling faster loading in the game engine.

### Requirement: Texture Compression

The system SHALL compress source textures to target GPU formats (BC7, ASTC, ETC2) with mip chain generation, using Basis Universal, NVTT, or PVRTexTool.

#### Scenario: Texture compressed to target format
- GIVEN a source PNG texture and config `formats: ["bc7", "astc_4x4"]` with `mipmap: true`
- WHEN the texture compression stage runs
- THEN the system SHALL produce a BC7 compressed texture
- AND an ASTC 4x4 compressed texture
- AND each output SHALL include a full mip chain
- AND the file size SHALL be reduced compared to the source

#### Scenario: Unsupported format
- GIVEN a source texture in an unsupported format
- WHEN the texture compression stage runs
- THEN the system SHALL return an error with a clear message
- AND SHALL NOT produce any output

### User Stories

- **US-10:** As a rendering engineer, I want the pipeline to compress textures to target formats (BC7, ASTC, ETC2) so the game loads faster.

---

## 04 Material Instantiation

The material instantiation stage creates engine material assets from material JSON definitions, resolving texture references and binding shaders.

### Requirement: Material Instantiation

The system SHALL instantiate engine material assets from material JSON definitions, resolving texture references and binding the correct shader.

#### Scenario: Material created from JSON
- GIVEN a material JSON with shader binding and texture references
- WHEN the material instantiation stage runs
- THEN the system SHALL create an engine material asset
- AND the material SHALL reference the correct compiled textures
- AND the material SHALL bind the correct shader

---

## 05 Animation Retargeting

The animation retargeting stage maps animation data from a source skeleton to a target skeleton with different proportions, using bone mapping and inverse kinematics (IK) to preserve motion quality.

### Requirement: Animation Retargeting

The system SHALL retarget animations between skeletons with different proportions, using bone mapping and IK to preserve motion quality, and SHALL support configurable retarget methods.

#### Scenario: Animation retargeted to different skeleton
- GIVEN a source animation on skeleton A and a target skeleton B with different proportions
- WHEN the animation retargeting stage runs
- THEN the system SHALL map bones from skeleton A to skeleton B
- AND produce retargeted animation tracks with preserved duration
- AND root motion SHALL be correctly transferred

#### Scenario: Keyframe reduction
- GIVEN config `compression: 0.9` (90% keyframe reduction)
- WHEN animation retargeting completes
- THEN the output SHALL have a reduced number of keyframes
- AND the motion quality SHALL be preserved within acceptable bounds

### User Stories

- **US-11:** As a technical artist, I want the pipeline to retarget animations between skeletons with different proportions.

---

## 06 SoundBank Build

The SoundBank build stage processes audio assets with metadata (loop points, attenuation, category) into a platform SoundBank (FMOD Studio or Wwise) for use in the game engine.

### Requirement: SoundBank Build

The system SHALL build a platform SoundBank (FMOD .bank or Wwise SoundBank) from submitted audio assets and metadata, preserving loop points, attenuation settings, and categorization.

#### Scenario: SoundBank built from audio assets
- GIVEN audio assets with metadata including loop points, attenuation, and category
- WHEN the SoundBank build stage runs
- THEN the system SHALL produce a SoundBank file
- AND all audio files SHALL be included
- AND loop points and attenuation metadata SHALL be preserved
- AND compression SHALL be applied per platform target

#### Scenario: Missing audio metadata
- GIVEN an audio asset without loop points metadata
- WHEN the SoundBank build stage runs
- THEN the system SHALL include the audio with default settings
- AND SHALL NOT fail the pipeline

### User Stories

- **US-12:** As an audio designer, I want the pipeline to build an FMOD/Wwise SoundBank from submitted audio assets.

---

## 07 Out of Scope

- **Agents never create art** — no procedural or auto-composition of any kind
- Audio composition, recording, or mixing (human-only)
- Game engine runtime asset loading (handled by the engine)
- CI/CD pipeline for full game builds (see `../operations/README.md`)

---

## Implementation Notes

### Phase 2: Automated Integration + Preview Server

**Goal:** Validated assets are automatically integrated and previewable in a web viewer.

**Dependencies:** Phase 1 (Asset Validation + Submission CLI)

**Deliverables:**
- [ ] DAG-based integration stage execution engine
- [ ] LOD generation stage (mesh simplification)
- [ ] Collision mesh stage (convex hull decomposition)
- [ ] Texture compression stage (Basis Universal / NVTT wrapper)
- [ ] Material instantiation stage (template engine)
- [ ] Animation retargeting stage (skeleton mapping + IK)
- [ ] SoundBank build stage (FMOD Studio API or Wwise CLI wrapper)
- [ ] Integration manifest output

**Implementation details:**
- Implement DAG-based stage execution engine with dependency resolution
- Use established libraries (Open3D, Assimp simplification) for mesh processing
- Make SoundBank stage pluggable; support both FMOD and Wwise with fallback to raw file output
- Validate output visually for mesh simplification quality
- Cache integration results where possible

---

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-14 | S110100 | 0.1.0 | Migrated from deprecated product-spec files |