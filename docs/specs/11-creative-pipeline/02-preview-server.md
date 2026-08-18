---
status: draft
created: '2026-08-14'
updated: '2026-08-14'
tags:
  - spec-tree
  - feature
  - creative-pipeline
priority: high
spec-refs: [110200, 110201, 110202, 110203, 110204, 110205, 110206]
---

# 02 Preview Server

> Lightweight web-based preview server for rendering submitted assets in a staging viewer, with WebGL 3D rendering, audio waveform display, context staging, and multiple preview modes.

**Status:** Draft  
**Last updated:** 2026-08-14  
**Spec code:** S110200

---

## 00 WebGL 3D Viewer

The preview server provides a WebGL-based 3D viewer (Three.js or Babylon.js) for rendering submitted 3D assets in a staging environment. The viewer loads glTF assets and renders them with lighting, a ground plane, and a reference grid.

### Server Model

```go
type PreviewServer struct {
    Port     int
    CacheDir string
    Viewer   string  // "three", "babylon"
    Mode     string  // "fast", "full"
}
```

### Non-Functional Targets

| Category | Requirement | Target |
|----------|-------------|--------|
| **Performance** | Preview load time | < 3s from submission to rendered preview |
| **Reliability** | Preview server uptime | 99.5% (during active review periods) |
| **Performance** | Memory usage | < 500MB per preview |

### Requirement: WebGL 3D Asset Rendering

The system SHALL render submitted 3D assets in a WebGL viewer (Three.js or Babylon.js), loading glTF format with proper lighting, materials, and camera controls.

#### Scenario: 3D asset renders in viewer
- GIVEN a submitted 3D asset in glTF format
- WHEN the preview URL is opened
- THEN the viewer SHALL load the asset
- AND render it with correct mesh geometry, materials, and textures
- AND provide orbit camera controls for inspection

#### Scenario: Missing texture fallback
- GIVEN a 3D asset with a missing texture reference
- WHEN the viewer renders the asset
- THEN the viewer SHALL display a placeholder material for the missing texture
- AND SHALL NOT crash or show a broken render

### User Stories

- **US-13:** As a game director, I want to preview submitted assets in-context on a web-based staging viewer so I can approve or request changes without loading the full engine.

---

## 01 Audio Preview

The preview server provides an audio waveform viewer for audio asset preview, with sample-accurate waveform display, transport controls, and zoom levels.

### Requirement: Audio Waveform Preview

The system SHALL display audio assets with a waveform viewer, providing sample-accurate waveform rendering, play/pause/seek controls, and configurable zoom levels.

#### Scenario: Audio waveform displayed
- GIVEN a submitted audio asset (WAV, FLAC, MP3, OGG)
- WHEN the preview URL is opened
- THEN the system SHALL display the audio waveform
- AND provide play, pause, and seek controls
- AND support zooming into the waveform

#### Scenario: Audio with metadata
- GIVEN an audio asset with loop points and attenuation metadata
- WHEN the preview loads
- THEN the system SHALL display the loop points on the waveform
- AND SHALL display the attenuation curve

---

## 02 Context Staging

The preview server places assets in a simplified staging scene with lighting, ground plane, and reference grid. Context staging enables reviewers to evaluate assets in a realistic but controlled environment.

### Requirement: Context Staging Scene

The system SHALL render submitted assets in a staging scene that includes configurable lighting, a ground plane, and a reference grid for spatial context.

#### Scenario: Staging scene loads with asset
- GIVEN a submitted 3D asset
- WHEN the preview URL is opened in fast mode
- THEN the system SHALL render the asset with:
  - A default lighting setup (ambient + directional)
  - A ground plane
  - A reference grid

#### Scenario: Full staging scene
- GIVEN multiple submitted assets
- WHEN the preview URL is opened in full mode
- THEN the system SHALL compose a complete staging scene with all assets positioned
- AND SHALL provide runtime controls for asset visibility and positioning

---

## 03 Preview Modes

The preview server supports multiple preview modes: fast mode for single asset preview (< 3s load), full mode for complete staging scenes with multiple assets, and comparison mode for side-by-side source vs integrated asset comparison.

### Preview API Endpoints

```
POST /preview/asset — upload asset, get preview URL
GET  /preview/:id — render preview page
GET  /preview/:id/compare?stage=lod1 — comparison view
```

### Requirement: Fast Mode

The system SHALL support a fast preview mode that renders a single asset with default staging in under 3 seconds from submission to rendered preview.

#### Scenario: Fast mode loads quickly
- GIVEN a single asset submission
- WHEN the fast mode preview is requested
- THEN the system SHALL render the preview in under 3 seconds
- AND the viewer SHALL display the asset with default staging

### Requirement: Full Mode

The system SHALL support a full preview mode that builds a complete staging scene with multiple assets for comprehensive review.

#### Scenario: Full mode with multiple assets
- GIVEN multiple submitted assets
- WHEN the full mode preview is requested
- THEN the system SHALL compose a scene with all assets
- AND load time SHALL be under 10 seconds

### Requirement: Comparison Mode

The system SHALL support a comparison mode that shows source and integrated assets side-by-side, enabling reviewers to evaluate the impact of integration stages.

#### Scenario: Source vs integrated comparison
- GIVEN an asset has been through integration (e.g., LOD generation)
- WHEN the comparison mode is requested with `stage=lod1`
- THEN the system SHALL display the source asset on the left
- AND the integrated asset (LOD1) on the right
- AND both viewers SHALL share the same camera controls for synchronized inspection

---

## 04 Caching and Performance

The preview server caches rendered previews to improve load times and reduce compute load. Cache hit rate targets > 80% during active review periods.

### Requirement: Preview Caching

The system SHALL cache rendered previews on disk, invalidating the cache when the source asset changes, and target a cache hit rate of > 80%.

#### Scenario: Cached preview served
- GIVEN an asset has been previewed before and the source has not changed
- WHEN the preview URL is requested again
- THEN the system SHALL serve the cached preview
- AND load time SHALL be under 1 second

#### Scenario: Cache invalidated on source change
- GIVEN an asset has been re-submitted with changes
- WHEN the preview is requested
- THEN the system SHALL invalidate the old cache entry
- AND regenerate the preview from the new source

### Requirement: Performance Under Load

The system SHALL handle concurrent preview requests with bounded memory usage and return HTTP 503 with retry-after header when overloaded.

#### Scenario: Concurrent requests handled
- GIVEN 50 simultaneous preview requests
- WHEN the server processes them
- THEN the system SHALL process all requests without exceeding 500MB memory per preview
- AND SHALL NOT crash under load

#### Scenario: Server overload response
- GIVEN the server is at capacity
- WHEN a new preview request arrives
- THEN the system SHALL return HTTP 503
- AND include a retry-after header

---

## 05 Public API Endpoints

The preview server exposes HTTP endpoints for asset upload, preview rendering, and comparison views.

### Requirement: Preview Server Endpoints

The system SHALL expose RESTful HTTP endpoints for preview operations, including asset upload, preview page rendering, and comparison views.

#### Scenario: Upload asset and get preview URL
- GIVEN a POST request to `/preview/asset` with an asset file
- WHEN the server processes the upload
- THEN the system SHALL return a preview URL
- AND the preview SHALL be accessible at `GET /preview/:id`

#### Scenario: Invalid asset returns error
- GIVEN a POST request to `/preview/asset` with an invalid file
- WHEN the server attempts to process it
- THEN the system SHALL return an HTTP 400 error
- AND the error message SHALL describe the problem

#### Scenario: Comparison endpoint
- GIVEN a GET request to `/preview/:id/compare?stage=lod1`
- WHEN the server processes the request
- THEN the system SHALL render a side-by-side comparison view
- AND the source and integrated asset SHALL be displayed together

---

## 06 Out of Scope

- **Agents never create art** — the preview server is a viewing tool, not a creation tool
- Game engine runtime asset loading (handled by the engine)
- CI/CD pipeline for full game builds (see `../operations/README.md`)
- Asset version control (handled by Git)

---

## Implementation Notes

### Phase 2: Automated Integration + Preview Server

**Goal:** Validated assets are automatically integrated and previewable in a web viewer.

**Dependencies:** Phase 1 (Asset Validation + Submission CLI)

**Deliverables:**
- [ ] Preview server with WebGL viewer (Three.js or Babylon.js)
- [ ] Audio waveform viewer for audio assets
- [ ] Context staging scene (lighting, ground plane, reference grid)
- [ ] Fast mode: single asset preview in < 3s
- [ ] Full mode: complete staging scene with multiple assets
- [ ] Comparison mode: side-by-side source vs integrated
- [ ] Preview caching

**Implementation details:**
- Implement lightweight HTTP server for preview rendering
- Use glTF for fast WebGL loading
- Cache rendered previews on disk
- Use established libraries (Three.js, Babylon.js) for WebGL rendering
- Support audio waveform display with sample-accurate rendering

---

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-14 | S110200 | 0.1.0 | Migrated from deprecated product-spec files |