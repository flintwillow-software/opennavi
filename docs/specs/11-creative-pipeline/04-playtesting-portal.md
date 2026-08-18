---
status: draft
created: '2026-08-14'
updated: '2026-08-14'
tags:
  - spec-tree
  - feature
  - creative-pipeline
priority: high
spec-refs: [110400, 110401, 110402, 110403, 110404, 110405, 110406]
---

# 04 Playtesting Portal

> Web-based playtesting portal with build assignment, session tracking, structured feedback forms, aggregation dashboard, and data export — operating as a parallel track to the creative review workflow.

**Status:** Draft  
**Last updated:** 2026-08-14  
**Spec code:** S110400

---

## 00 Build Assignment

The playtesting portal enables producers to assign playtesting builds to specific testers. Each build is versioned and tracked through the assignment lifecycle.

### Data Models

```go
type PlaytestBuild struct {
    ID          string
    BuildURL    string
    AssignedTo  []string
    Status      string  // "assigned", "in_progress", "completed"
    Feedback    []PlaytestFeedback
}

type PlaytestFeedback struct {
    TesterID    string
    SessionID   string
    Category    string  // "bug", "balance", "usability", "performance"
    Severity    string  // "critical", "major", "minor", "suggestion"
    Description string
    Timestamp   time.Time
}
```

### Requirement: Build Assignment

The system SHALL allow producers to assign a playtesting build to specific testers, creating a `PlaytestBuild` record with `assigned` status and tracking the build through the lifecycle.

#### Scenario: Producer assigns build to testers
- GIVEN a producer has a build ready for playtesting
- WHEN the producer assigns the build to a list of testers
- THEN the system SHALL create a `PlaytestBuild` record
- AND the status SHALL be "assigned"
- AND each tester SHALL be notified with the build link

#### Scenario: Tester receives build link
- GIVEN a build is assigned to a tester
- WHEN the assignment is complete
- THEN the system SHALL provide the tester with a build URL
- AND the build SHALL be accessible at the provided URL

### User Stories

- **US-18:** As a producer, I want to assign playtesting builds to specific testers so sessions are tracked and structured.

---

## 01 Session Tracking

The playtesting portal tracks playtesting sessions with start/end timestamps, build version, and tester identity. Sessions are created when a tester begins playing and closed when they submit feedback or end the session.

### Requirement: Session Tracking

The system SHALL track playtesting sessions with start and end timestamps, build version, and tester identity, creating a session record when a tester begins a playtesting session.

#### Scenario: Session created on start
- GIVEN a tester opens a playtesting build
- WHEN the tester begins the session
- THEN the system SHALL create a session record with:
  - Tester identity
  - Build version
  - Session start timestamp

#### Scenario: Session closed on feedback submission
- GIVEN a tester has an active session
- WHEN the tester submits feedback
- THEN the system SHALL close the session
- AND record the end timestamp
- AND associate the feedback with the session

---

## 02 Structured Feedback Forms

The playtesting portal provides structured feedback forms with category, severity, description, and optional screenshot upload. Feedback is associated with the tester's session and the build version.

### Feedback Form Fields

| Field | Type | Options |
|-------|------|---------|
| Category | Select | "bug", "balance", "usability", "performance" |
| Severity | Select | "critical", "major", "minor", "suggestion" |
| Description | Text | Free-form text |
| Screenshot | File upload | Optional image |

### Requirement: Structured Feedback Submission

The system SHALL provide structured feedback forms with category, severity, description, and optional screenshot upload, associating each submission with the tester's session and build version.

#### Scenario: Tester submits structured feedback
- GIVEN a tester has an active playtesting session
- WHEN the tester submits feedback through the form
- THEN the system SHALL record the feedback with:
  - Category (bug, balance, usability, performance)
  - Severity (critical, major, minor, suggestion)
  - Description (free-form text)
  - Optional screenshot
  - Tester ID and session ID
  - Timestamp

#### Scenario: Feedback without screenshot
- GIVEN a tester submits feedback without a screenshot
- WHEN the feedback is recorded
- THEN the system SHALL accept the feedback
- AND the screenshot field SHALL be null

### User Stories

- **US-19:** As a tester, I want to submit feedback through a structured form so the team can act on specific issues.

---

## 03 Aggregation Dashboard

The playtesting portal provides an aggregation dashboard that displays top issues, per-tester metrics, and build comparison. The dashboard enables producers and game directors to track the most common issues across sessions.

### Requirement: Aggregation Dashboard

The system SHALL provide an aggregation dashboard that displays top issues by frequency, per-tester feedback metrics, and build comparison views.

#### Scenario: Dashboard shows top issues
- GIVEN multiple feedback submissions across sessions
- WHEN the producer views the aggregation dashboard
- THEN the dashboard SHALL display:
  - Top issues ranked by frequency
  - Issues grouped by category and severity
  - Per-tester feedback count and metrics

#### Scenario: Build comparison
- GIVEN feedback from two different build versions
- WHEN the producer views the build comparison
- THEN the dashboard SHALL display feedback side-by-side
- AND SHALL highlight issues unique to each build version

### User Stories

- **US-20:** As a game director, I want to view aggregated playtest feedback so I can track the most common issues.

---

## 04 Data Export

The playtesting portal supports exporting feedback data as structured data (JSON/CSV) for external analysis and integration with project management tools.

### Requirement: Feedback Export

The system SHALL export playtesting feedback as structured data in JSON or CSV format, including all feedback fields, tester identity, session timestamps, and build version.

#### Scenario: Export feedback as JSON
- GIVEN a producer wants to export feedback data
- WHEN the producer requests a JSON export
- THEN the system SHALL return all feedback records as structured JSON
- AND each record SHALL include category, severity, description, tester ID, session timestamps, and build version

#### Scenario: Export feedback as CSV
- GIVEN a producer wants to export feedback data
- WHEN the producer requests a CSV export
- THEN the system SHALL return all feedback records as a CSV file
- AND the CSV SHALL have headers for all fields

---

## 05 Notification Integration

The playtesting portal notifies producers when playtest sessions complete and aggregates feedback for review. Notifications are dispatched via the preferred channel (Telegram, email, etc.).

### Requirement: Notification on Session Completion

The system SHALL notify producers when a playtesting session completes with aggregated feedback, including the number of feedback items, categories, and severity distribution.

#### Scenario: Producer notified on session completion
- GIVEN a tester completes a playtesting session
- WHEN the session ends
- THEN the system SHALL send a notification to the producer
- AND the notification SHALL include the number of feedback items, categories, and severity distribution

#### Scenario: Daily feedback digest
- GIVEN multiple playtesting sessions in a day
- WHEN the end of the day is reached
- THEN the system SHALL send a daily digest to the producer
- AND the digest SHALL aggregate all feedback from the day

---

## 06 Out of Scope

- **Agents never create art** — the playtesting portal is a feedback collection tool, not a creation tool
- Playtesting is a parallel track — not gated on creative review. Builds can be distributed for playtesting independently of asset approval
- Game design or narrative writing (human-only)
- Level design or world building (human-only)
- CI/CD pipeline for full game builds (see `../operations/README.md`)

---

## Implementation Notes

### Phase 3: Creative Review Queue + Playtesting Portal

**Goal:** Full workflow from submission to creative review, with playtesting as a parallel track.

**Dependencies:** Phase 2 (Integration + Preview)

**Deliverables:**
- [ ] Playtesting portal web UI
- [ ] Build assignment and session tracking
- [ ] Structured feedback forms
- [ ] Feedback aggregation dashboard
- [ ] Data export (JSON/CSV)
- [ ] Notification dispatch

**Implementation details:**
- Web-based portal for build assignment and session tracking
- `PlaytestBuild` and `PlaytestFeedback` data models
- Build assignment: producer assigns build to specific testers
- Session tracking: start/end timestamps, build version, tester identity
- Structured feedback forms: category, severity, description, optional screenshot upload
- Aggregation dashboard: top issues, per-tester metrics, build comparison
- Export feedback as structured data (JSON/CSV)
- Use web UI framework for portal implementation
- Integrate with notification channels for producer alerts

---

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-14 | S110400 | 0.1.0 | Migrated from deprecated product-spec files |