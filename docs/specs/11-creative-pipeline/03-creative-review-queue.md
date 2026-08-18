---
status: draft
created: '2026-08-14'
updated: '2026-08-14'
tags:
  - spec-tree
  - feature
  - creative-pipeline
priority: high
spec-refs: [110300, 110301, 110302, 110303, 110304, 110305, 110306]
---

# 03 Creative Review Queue

> Review queue management, reviewer assignment, approve/changes/reject workflow, notification dispatch, and GitHub Issue integration for the creative review lifecycle.

**Status:** Draft  
**Last updated:** 2026-08-14  
**Spec code:** S110300

---

## 00 Review Queue Management

The creative review queue tracks all pending submissions in FIFO order (with priority overrides) and manages the review lifecycle. Each submission becomes a review item that tracks status, feedback, and reviewer assignment.

### Review Item Model

```go
type ReviewItem struct {
    ID          string
    AssetID     string
    Artist      string
    Reviewer    string
    Status      string  // "pending", "approved", "changes_requested", "rejected"
    PreviewURL  string
    ValidReport ValidationReport
    Feedback    string
    CreatedAt   time.Time
    ReviewedAt  *time.Time
}
```

### Review Workflow States

```
Submission → VALIDATION → INTEGRATION → PREVIEW → REVIEW QUEUE
                                                      |
                                          ┌───────────┼───────────┐
                                          ▼           ▼           ▼
                                      APPROVED    CHANGES     REJECTED
                                          │           │           │
                                          ▼           ▼           ▼
                                    Next Stage   Return to   Return to
                                                  Artist      Triage
```

### Requirement: Review Queue FIFO Ordering

The system SHALL maintain a review queue ordered by submission time (FIFO) within priority, where each item tracks its status through the review lifecycle.

#### Scenario: Items ordered by submission time
- GIVEN three submissions at times T1, T2, T3 (T1 earliest)
- WHEN the review queue is queried
- THEN the items SHALL be ordered T1, T2, T3 (FIFO)

#### Scenario: Priority override
- GIVEN a high-priority submission at time T3, with earlier submissions at T1 and T2
- WHEN the review queue is queried
- THEN the high-priority item SHALL appear before lower-priority items regardless of submission time

### Requirement: Status Tracking

The system SHALL track each review item through the status lifecycle: "pending", "approved", "changes_requested", and "rejected", with timestamps for creation and review.

#### Scenario: Item status transitions
- GIVEN a new review item is created
- WHEN the item is added to the queue
- THEN its status SHALL be "pending"
- AND `CreatedAt` SHALL be set to the current time

#### Scenario: Approved item updates timestamp
- GIVEN a review item with status "pending"
- WHEN the reviewer approves it
- THEN the status SHALL change to "approved"
- AND `ReviewedAt` SHALL be set to the current time

### User Stories

- **US-15:** As a game director, I want a review queue that tracks all pending submissions so I can review them in order.
- **US-16:** As an artist, I want to see the status of my submissions (pending, approved, changes requested, rejected) so I know the next action.

---

## 01 Reviewer Assignment

The Game Director agent assigns reviewers to queue items based on availability, tracked via the multi-agent presence system. Humans are notified when a review is assigned.

### Requirement: Reviewer Assignment

The system SHALL assign reviewers to review items based on availability, with the Game Director agent managing assignments through the multi-agent presence system.

#### Scenario: Reviewer assigned to item
- GIVEN a pending review item and an available reviewer
- WHEN the Game Director agent processes the queue
- THEN the reviewer SHALL be assigned to the item
- AND the item status SHALL remain "pending"

#### Scenario: No reviewer available
- GIVEN a pending review item and no available reviewers
- WHEN the Game Director agent processes the queue
- THEN the item SHALL remain in the queue with no reviewer assigned
- AND the system SHALL retry assignment when a reviewer becomes available

---

## 02 Review Actions

Reviewers can approve, request changes with specific feedback, or reject a submission with a rationale. Each action triggers the appropriate status propagation: approved items proceed to the next pipeline stage, changes requested items return to the artist with feedback, and rejected items return to triage.

### Requirement: Approve Action

The system SHALL allow reviewers to approve a submission, which SHALL change the item status to "approved" and trigger the next pipeline stage.

#### Scenario: Approved submission proceeds
- GIVEN a review item with a preview URL
- WHEN the reviewer approves the submission
- THEN the status SHALL change to "approved"
- AND the asset SHALL proceed to the next pipeline stage

### Requirement: Request Changes Action

The system SHALL allow reviewers to request changes with specific feedback, which SHALL change the item status to "changes_requested" and return the item to the artist.

#### Scenario: Changes requested with feedback
- GIVEN a review item requiring modifications
- WHEN the reviewer requests changes with specific feedback
- THEN the status SHALL change to "changes_requested"
- AND the feedback SHALL be stored on the review item
- AND the artist SHALL be notified with the feedback

#### Scenario: Resubmission creates new review item
- GIVEN an item with status "changes_requested" and the artist resubmits
- WHEN the resubmission is processed
- THEN a new review item SHALL be created
- AND the new item SHALL be linked to the original item

### Requirement: Reject Action

The system SHALL allow reviewers to reject a submission with a rationale, which SHALL change the item status to "rejected" and return the item to triage.

#### Scenario: Rejected submission returns to triage
- GIVEN a review item that cannot be accepted
- WHEN the reviewer rejects the submission with a rationale
- THEN the status SHALL change to "rejected"
- AND the item SHALL return to triage
- AND the artist SHALL be notified with the rationale

### User Stories

- **US-17:** As a reviewer, I want to approve, request changes with specific feedback, or reject a submission so the pipeline can proceed or redirect.

---

## 03 Notification Dispatch

The system dispatches notifications to reviewers and artists via their preferred channels (Telegram, email, etc.) when reviews are assigned, feedback is provided, or status changes occur.

### Requirement: Notification Dispatch

The system SHALL dispatch notifications to reviewers and artists via their preferred channels when review events occur, including assignment, feedback, status changes, and resubmission.

#### Scenario: Reviewer notified on assignment
- GIVEN a reviewer is assigned to a review item
- WHEN the assignment is complete
- THEN the system SHALL send a notification to the reviewer via their preferred channel
- AND the notification SHALL include the preview link and asset context

#### Scenario: Artist notified on feedback
- GIVEN changes are requested or a submission is rejected
- WHEN the status changes
- THEN the system SHALL send a notification to the artist
- AND the notification SHALL include the feedback and next steps

---

## 04 GitHub Issue Integration

Each review item produces a GitHub Issue with `label: creative-review` that serves as the handoff document. The issue body contains the preview URL, validation report, artifact links, and feedback. This integrates with the multi-agent handoff protocol (Gap 8).

### Requirement: GitHub Issue as Handoff Document

The system SHALL create a GitHub Issue for each review item with `label: creative-review`, containing the preview URL, validation report, artifact links, and feedback in the issue body.

#### Scenario: Issue created for review item
- GIVEN a new review item is created
- WHEN the item enters the review queue
- THEN the system SHALL create a GitHub Issue with `label: creative-review`
- AND the issue body SHALL contain the preview URL, validation report, and artifact links

#### Scenario: Issue updated on status change
- GIVEN a review item with an existing GitHub Issue
- WHEN the status changes (approved, changes_requested, rejected)
- THEN the system SHALL update the issue with the new status and feedback
- AND the issue SHALL remain the single source of truth for the review

---

## 05 Status Tracking

Artists and producers can view the status of all submissions at any time. The review queue provides a dashboard-style view of pending, in-review, approved, and rejected items.

### Pipeline Flow

```
HUMAN CREATIVE SPACE
  ┌──────────────────────────────────────────────┐
  │  Artist: Blender, Maya, Substance, Pro Tools │
  │  Designer: Markdown specs, JSON tables       │
  │  Submission: opennavi asset submit <path>    │
  └──────────────────┬───────────────────────────┘
                     │
                     ▼
AGENT TECHNICAL SPACE
  ┌──────────────────────────────────────────────┐
  │  1. VALIDATE                                  │
  │  2. INTEGRATE                                 │
  │  3. PREVIEW                                   │
  │  4. HANDOFF → Creative Review Queue           │
  │     Approve / Changes / Reject                │
  └──────────────────┬───────────────────────────┘
                     │
                     ▼
HUMAN CREATIVE REVIEW
  ┌──────────────────────────────────────────────┐
  │  Approve → agent proceeds to integration     │
  │  Changes  → agent returns to artist          │
  │  Reject   → agent returns to triage          │
  └──────────────────────────────────────────────┘
```

### Requirement: Submission Status Visibility

The system SHALL provide status visibility for all submissions, allowing artists and producers to see the current status (pending, approved, changes_requested, rejected) of each submission.

#### Scenario: Artist views submission status
- GIVEN an artist has submitted multiple assets
- WHEN the artist queries the status of their submissions
- THEN the system SHALL display each submission with its current status, reviewer, and feedback (if any)

#### Scenario: Producer views queue overview
- GIVEN a producer wants to see the overall review queue
- WHEN the producer queries the queue
- THEN the system SHALL display all items grouped by status (pending, in-review, approved, rejected)
- AND SHALL show submission time, asset name, and assigned reviewer

---

## 06 Out of Scope

- **Agents never create art** — the review queue is a workflow tool, not a creation tool
- The review queue is an issue tracker — the GitHub Issue is the handoff document, no separate review database
- 3D modeling, sculpting, or UV mapping (human-only)
- Game design or narrative writing (human-only)

---

## Implementation Notes

### Phase 3: Creative Review Queue + Playtesting Portal

**Goal:** Full workflow from submission to creative review, with playtesting as a parallel track.

**Dependencies:** Phase 2 (Integration + Preview)

**Deliverables:**
- [ ] Creative review queue with `ReviewItem` management
- [ ] GitHub Issue integration for handoff
- [ ] Approve/Changes/Reject workflow
- [ ] Notification dispatch
- [ ] FIFO ordering with priority overrides
- [ ] Status propagation: approved → next stage, changes → artist, rejected → triage

**Implementation details:**
- Implement `ReviewItem` struct and queue management
- Game Director agent assigns reviewers based on availability (presence system)
- FIFO ordering with priority overrides
- GitHub Issue integration: create issue with `label: creative-review` per review item
- Structured handoff payload: preview URL, validation report, artifact links
- Human actions: Approve, Request Changes (with feedback), Reject (with rationale)
- Notification dispatch via preferred channel (Telegram, email, etc.)
- Use GitHub Issues API for handoff document management

---

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-14 | S110300 | 0.1.0 | Migrated from deprecated product-spec files |