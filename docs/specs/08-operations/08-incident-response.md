---
status: draft
created: '2026-08-11'
updated: '2026-08-11'
tags:
  - spec
  - operations
  - incident-response
priority: high
spec-refs: [080800, 080801, 080802, 080803, 080804, 080805]
---

# Incident Response Protocol

> **Status:** Draft · **Priority:** High  
> **Scope:** Structured incident response flow for P0/P1/P2 incidents, runbook system, escalation rules, and post-mortem automation.

---

## 00 Overview

The incident response protocol defines how a navi team detects, triages, investigates, fixes, reviews, verifies, and learns from incidents. It's a reusable 8-step flow that can be triggered by monitoring alerts, user reports, or automated health checks.

### Severity Levels

| Severity | Description | SLA | Bypass Rules |
|----------|-------------|-----|--------------|
| **P0** | Service down, data loss risk, security breach | Immediate response | Bypasses all WIP limits, emergency review gate, auto-escalation |
| **P1** | Degraded performance, no data loss, partial outage | 15 min response | Expedite class of service, fast-track review |
| **P2** | Minor issues, no user impact | Next business day | Standard flow |

### Requirement: Severity Classification

The system SHALL classify each incident as P0, P1, or P2 with the documented SLA and bypass rules per severity: P0 requires immediate response with all gates bypassed, P1 requires a 15-minute response with expedited review, and P2 follows the standard flow on the next business day.

#### Scenario: P0 incident bypass
- **GIVEN** an incident is classified as P0 (service down, data loss risk, or security breach)
- **WHEN** the incident enters the response flow
- **THEN** the system SHALL bypass all WIP limits, the emergency review gate, and auto-escalation rules

#### Scenario: P2 standard flow
- **GIVEN** an incident is classified as P2 with no user impact
- **WHEN** the incident enters the response flow
- **THEN** the system SHALL process it through the standard queue on the next business day

### Requirement: Incident Representation

The system SHALL represent each incident with a unique ID, severity, status, affected service, summary, alert payload, detection timestamp, timeline of events, optional runbook ID, and artifacts.

#### Scenario: Incident card creation
- **GIVEN** an incident is detected
- **WHEN** the system creates the incident record
- **THEN** the system SHALL log it with a unique ID, timestamp, severity, affected service, and alert payload

---

## 01 The 8-Step Protocol

### Step 1: DETECT

An incident is detected by any of these triggers:

- **Monitoring alert** — webhook from Grafana, Datadog, Prometheus, BetterStack
- **Health check failure** — cron job health check detects a service down
- **User report** — support ticket, chat message, or bug report
- **CVE scan** — automated vulnerability scanner finds a critical CVE
- **Pattern detection** — agent detects a recurring issue (e.g., error rate spike)

**Action:**
- Create an incident card in Triage with severity, affected service, and alert payload
- P0 incidents bypass the queue entirely — go directly to Ready
- Log the incident with a unique ID and timestamp

```go
type Incident struct {
    ID          string    `json:"id"`
    Severity    string    `json:"severity"` // "P0", "P1", "P2"
    Status      string    `json:"status"`   // "detected", "triaged", "investigating", "fixing", "reviewing", "verifying", "deploying", "resolved", "post-mortem"
    Service     string    `json:"service"`
    Summary     string    `json:"summary"`
    AlertPayload json.RawMessage `json:"alert_payload,omitempty"`
    DetectedAt  time.Time `json:"detected_at"`
    Timeline    []IncidentEvent `json:"timeline"`
    RunbookID   string    `json:"runbook_id,omitempty"`
    Artifacts   []string  `json:"artifacts,omitempty"` // PRs, dashboards, logs
}
```

### Step 2: TRIAGE

The PM/Lead agent classifies the incident:

- **Severity** — confirm or adjust the severity level
- **Affected service** — identify which service(s) are impacted
- **Labels** — apply relevant labels (e.g., `backend`, `security`, `database`)
- **Known issue?** — check the knowledge base (see memory spec) for similar resolved incidents
- **Runbook?** — check if a runbook exists for this incident type

**P0 behavior:**
- Immediate P2P broadcast to all relevant agents: "P0 incident on Payment API. Pull immediately."
- Notify human on-call (if configured)
- Create a dedicated incident channel (e.g., Discord channel, Slack channel)

**P1/P2 behavior:**
- Normal queue, expedite class of service for P1
- Standard queue for P2

### Step 3: INVESTIGATE

The assigned agent pulls the incident and follows the runbook (if one exists):

1. Check monitoring dashboards — error rate, latency, CPU, memory, disk
2. Check recent deploys — was there a recent change?
3. Check logs — search for error patterns
4. Check dependencies — are upstream/downstream services healthy?
5. Identify root cause

**Runbook system:**

```yaml
# runbooks/payment-api-down.md
runbook:
  incident_type: "payment-api-down"
  severity: ["P0", "P1"]
  steps:
    - step: 1
      action: "Check payment API error rate dashboard"
      url: "https://grafana.internal/d/payment-api"
    - step: 2
      action: "Check recent deploys to payment API"
      command: "gh release list --repo payment-api"
    - step: 3
      action: "Check database connection pool"
      command: "kubectl exec payment-api-0 -- psql -c 'SHOW max_connections'"
    - step: 4
      action: "Check payment provider status"
      url: "https://status.stripe.com"
    - step: 5
      action: "If all above normal, check application logs for errors"
      command: "kubectl logs -l app=payment-api --tail=100 | grep ERROR"
```

### Step 4: FIX

The agent creates a fix:

- **P0/P1:** Open a hotfix PR with the minimal change needed
- **P2:** Standard PR with full test coverage

**Fix types:**
- **Hotfix** — direct config change, dependency bump, rollback
- **Code change** — PR with fix implementation
- **Infrastructure change** — Terraform, Kubernetes config, or monitoring config update

### Step 5: REVIEW

The reviewer reviews the fix:

- **P0:** Emergency review — single reviewer, no QA in staging. The reviewer is P2P-triggered. Review focuses on: "Does this fix the issue? Does it break anything else?"
- **P1:** Expedited review — standard review, fast-track QA
- **P2:** Standard review flow

### Step 6: VERIFY

QA validates the fix:

- **P0:** Verify against the incident reproduction case. If the reproduction is confirmed fixed, approve.
- **P1:** Standard staging verification
- **P2:** Standard staging verification

### Step 7: DEPLOY

The agent deploys the fix to production:

1. Apply the fix (merge PR, run deployment pipeline)
2. Monitor for N minutes (P0: 30 min, P1: 15 min, P2: standard)
3. Confirm metrics normalized (error rate, latency, etc.)
4. Close the incident

### Step 8: POST-MORTEM

Within SLA timeframe (P0: 24h, P1: 72h, P2: optional):

1. Create follow-up items:
   - Runbook update (if the incident type wasn't covered)
   - Test coverage for the fix
   - Monitoring improvements (if detection was delayed)
   - Alert tuning (if the alert was noisy)
2. Document the incident timeline, root cause, and resolution
3. Update the knowledge base (see [06-memory/00-memory-system.md](../06-memory/00-memory-system.md) §5b)

### Requirement: Incident Detection and Triage

The system SHALL detect incidents from monitoring alerts, health check failures, user reports, CVE scans, or pattern detection, creating an incident card in Triage with severity, affected service, and alert payload, and SHALL have the PM/Lead agent triage each incident by confirming severity, identifying affected services, applying labels, checking the knowledge base for similar resolved incidents, and checking for an applicable runbook.

#### Scenario: Detection from monitoring alert
- **GIVEN** a monitoring webhook (Grafana, Datadog, Prometheus, or BetterStack) fires
- **WHEN** the system receives the alert
- **THEN** the system SHALL create an incident card with severity, affected service, and the alert payload

#### Scenario: P0 queue bypass
- **GIVEN** a detected incident is classified as P0
- **WHEN** the incident enters the triage queue
- **THEN** the system SHALL bypass the queue and place the incident directly into the Ready state

#### Scenario: P0 triage broadcast
- **GIVEN** an incident is classified as P0 during triage
- **WHEN** triage completes
- **THEN** the system SHALL broadcast a P2P notification to all relevant agents, notify the human on-call if configured, and create a dedicated incident channel

#### Scenario: P1 expedited queue
- **GIVEN** an incident is classified as P1 during triage
- **WHEN** the incident enters the queue
- **THEN** the system SHALL process it with the expedite class of service

### Requirement: Runbook-Guided Investigation

The system SHALL guide the assigned agent through the incident's runbook steps — checking monitoring dashboards, recent deploys, logs, and dependencies — to identify the root cause.

#### Scenario: Investigation with runbook
- **GIVEN** a runbook exists for the incident type
- **WHEN** the assigned agent investigates
- **THEN** the system SHALL present the runbook steps in order, including dashboard URLs and diagnostic commands

### Requirement: Severity-Appropriate Fix Creation

The system SHALL create fixes appropriate to severity: hotfix PRs with minimal changes for P0/P1 incidents and standard PRs with full test coverage for P2 incidents.

#### Scenario: P0 hotfix
- **GIVEN** a P0 incident requires a fix
- **WHEN** the agent creates the fix
- **THEN** the system SHALL open a hotfix PR with the minimal change needed

#### Scenario: P2 standard PR
- **GIVEN** a P2 incident requires a fix
- **WHEN** the agent creates the fix
- **THEN** the system SHALL open a standard PR with full test coverage

### Requirement: Emergency Review and Verification

The system SHALL apply severity-based review gates: P0 fixes receive an emergency single-reviewer review verified against the incident reproduction case, while P1 and P2 fixes follow expedited and standard review flows respectively.

#### Scenario: P0 emergency review
- **GIVEN** a P0 fix is ready for review
- **WHEN** the reviewer is P2P-triggered
- **THEN** the system SHALL have a single reviewer check whether the fix resolves the issue and breaks nothing else, without QA in staging

#### Scenario: P0 verification against reproduction
- **GIVEN** a P0 fix has passed review
- **WHEN** QA verifies the fix
- **THEN** the system SHALL verify against the incident reproduction case and approve only if the reproduction is confirmed fixed

### Requirement: Deployment Monitoring and Incident Closure

The system SHALL deploy fixes to production and monitor metrics for the severity-specific window — 30 minutes for P0, 15 minutes for P1 — confirming metrics normalized before closing the incident.

#### Scenario: Post-deploy monitoring
- **GIVEN** a fix has been deployed to production
- **WHEN** the monitoring window elapses
- **THEN** the system SHALL confirm error rate and latency metrics have normalized before closing the incident

### Requirement: Automated Post-Mortem

The system SHALL conduct a post-mortem within the severity SLA — 24 hours for P0, 72 hours for P1, optional for P2 — creating follow-up items for runbook updates, test coverage, monitoring improvements, and alert tuning, documenting the timeline and root cause, and updating the knowledge base.

#### Scenario: Post-mortem follow-up items
- **GIVEN** an incident has been resolved
- **WHEN** the post-mortem runs within the severity SLA
- **THEN** the system SHALL create follow-up items automatically for runbook updates, test coverage, monitoring improvements, and alert tuning

---

## 02 Escalation Rules

If no agent responds within the SLA, the system escalates:

| Condition | Action |
|-----------|--------|
| P0: no response in 2 min | Broadcast to all agents in the team |
| P0: no response in 5 min | Page human on-call (SMS, phone call) |
| P1: no response in 10 min | Broadcast to all agents; page human on-call |
| P1: no response in 30 min | Escalate to human team lead |
| P2: no response in 4h | Escalate to human team lead |
| Runbook not found | Create a new runbook entry from the incident |

### Requirement: Escalation on SLA Miss

The system SHALL escalate incidents when no agent responds within the severity-specific SLA: P0 broadcasts to all agents after 2 minutes and pages the human on-call after 5 minutes; P1 broadcasts and pages after 10 minutes and escalates to the human team lead after 30 minutes; P2 escalates to the human team lead after 4 hours.

#### Scenario: P0 no response in 2 minutes
- **GIVEN** a P0 incident with no agent response within 2 minutes
- **WHEN** the escalation timer fires
- **THEN** the system SHALL broadcast the incident to all agents in the team

#### Scenario: P0 no response in 5 minutes
- **GIVEN** a P0 incident with no agent response within 5 minutes
- **WHEN** the escalation timer fires
- **THEN** the system SHALL page the human on-call via SMS or phone call

### Requirement: Runbook-Not-Found Escalation

The system SHALL create a new runbook entry from an incident when no runbook exists for its type.

#### Scenario: Missing runbook
- **GIVEN** an incident type with no matching runbook
- **WHEN** the incident is processed
- **THEN** the system SHALL create a new runbook entry from the incident

---

## 03 Runbook System

Runbooks are structured guides for handling specific incident types. They live in the navi's config and are compiled into the binary (like skills) or loaded from a runbook directory at runtime.

```yaml
# ~/.config/navi/<navi>/runbooks.yaml
runbooks:
  - incident_type: "high-error-rate"
    severity: ["P0", "P1"]
    steps:
      - step: "Check error rate dashboard"
        url: "https://grafana.internal/d/errors"
      - step: "Check recent deploy"
        command: "gh release list --repo {{.Service}}"
      - step: "Check logs"
        command: "kubectl logs -l app={{.Service}} --tail=500 | grep ERROR"
```

**Runbook resolution:**
1. Check if a runbook exists for the incident type (via `incident_type` field)
2. If no runbook, check if a similar incident type exists (tag matching)
3. If no runbook at all, the agent follows the standard investigation flow
4. After resolution, a runbook is created from the incident if one didn't exist

### Requirement: Runbook Resolution Order

The system SHALL resolve runbooks in order: exact match on incident type, then tag-based similarity matching for similar incident types, then fall back to the standard investigation flow when no runbook exists.

#### Scenario: Exact runbook match
- **GIVEN** an incident with a known incident type
- **WHEN** the system resolves a runbook
- **THEN** the system SHALL return the runbook whose `incident_type` matches

#### Scenario: No runbook found
- **GIVEN** no runbook matches the incident type or similar types
- **WHEN** the system resolves a runbook
- **THEN** the system SHALL guide the agent through the standard investigation flow

### Requirement: Runbook Creation After Resolution

The system SHALL create a runbook from a resolved incident when no runbook existed for its type.

#### Scenario: New runbook from resolved incident
- **GIVEN** an incident was resolved without a matching runbook
- **WHEN** the incident resolution completes
- **THEN** the system SHALL generate a new runbook entry from the incident's investigation steps

---

## 04 Key Design Decisions

1. **8-step protocol** — clear, repeatable flow. Every incident follows the same steps.
2. **Severity-based bypass** — P0 incidents skip non-critical gates. Speed over process.
3. **Runbooks as first-class artifacts** — every incident should produce or update a runbook.
4. **Escalation to humans** — the team is autonomous but not infinite. Humans are paged when the team can't resolve.
5. **Post-mortem automation** — follow-up items are created automatically, not manually tracked.
6. **Knowledge base integration** — resolved incidents feed the knowledge base for future similarity matching.

## 05 Gaps / Future Work

- [ ] Runbook versioning and review
- [ ] Incident simulation / game days
- [ ] Cross-team incident coordination (when an incident spans multiple teams)
- [ ] Automated rollback trigger (if metrics don't normalize after deploy)
- [ ] Incident dashboard (web UI for ongoing incidents)

## Related

- [Operations README](./README.md) — operations spec index
- [GAPS.md](../../gaps/GAPS.md) Gap 2 — incident response gap analysis
- [Memory System](../06-memory/00-memory-system.md) §5b — knowledge base for resolved incidents
- [Autonomous Mode Interaction](./07-autonomous-mode-interaction.md) — webhook processing for alerts

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080800 | 0.1.0 | Migrated from docs/spec/ to spec-tree |