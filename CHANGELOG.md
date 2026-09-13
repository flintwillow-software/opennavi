# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- Migrated `docs/spec/` to the spec-tree standard (`docs/specs/`) — 13 numbered components, spec codes (6-digit `component.feature.subfeature`), `spec-refs` frontmatter, SHALL + GIVEN/WHEN/THEN requirement grammar, and Status History on every feature
- Added `docs/changes/` versioned change tracker, shipped `docs/templates/` skeletons, and synced the `specs-*` skills into `.pi/skills/`
- Deprecated product-spec files (REQUIREMENTS/DESIGN/IMPLEMENTATION/TESTING) folded into numbered feature files
- Renamed catalog tools to namespaced names per S130400 §04 across the spec tree (`file_read`, `shell_exec`, `file_grep`, `code_ast_parse`, …) and promoted the tool-catalog specs from draft to active

### Added
- Feature README entry points with ownership maps, success criteria, and status history
- CONTEXT.md, SOLUTION.md, RESULTS.md for product-lifecycle case study
- Out of Scope sections to all spec and case study documents
- RESULTS.md with measurable outcomes and lessons learned
- S130400 tool registry & discovery spec and S000800 SDK surface spec; 0.1.0 change-log entry derived for the tool catalog
- S130500 agent utility tools spec: `todo_read`/`todo_write`/`todo_update`, `time_read`, `note_read`/`note_write`, and `http_request` (CRUD with SSRF guards and credential-reference auth), plus the stateful tool storage seam; case study captured in `docs/case-study/agent-utility-tools/`
- Agent utility tools change-log entry (`docs/changes/0.1.0/feat/agent-utility-tools/`) tracked as Huly epic NAVI-266 with six sub-issues and dependency relations

- GitHub Action workflow enforcing changelog entries in PRs (#255)
## [1.0.0] - YYYY-MM-DD

### Added
- Initial release

### Changed
- 

### Fixed
- 

[Unreleased]: 
[1.0.0]: 
