---
status: draft
created: '2026-08-11'
updated: '2026-08-11'
tags:
  - spec
  - multi-agent
  - team
priority: high
spec-refs: [040400, 040401, 040402, 040403, 040404, 040405]
---

# Plan: Team-of-Teams Orchestration

**Scope:** Team-level IAM hierarchy, team registry, observability pipeline, cost attribution, and governance for multi-team enterprise deployments.

> **Part of:** [Multi-Agent Collaboration](./README.md) — addresses Gap 7 in [GAPS.md](../../gaps/GAPS.md).

## 00 IAM Hierarchy for Teams

### 1.1 The Team Principal Model

Teams are first-class IAM principals. Each team owns a keypair, and its identity is verified via signed messages, just like individual agents. The hierarchy mirrors AWS IAM:

```
Team "corporate-it" (IAM principal, signed keypair)
  └── Team Lead "it-leader" (navi with role: team-lead)
        ├── Agent "user-lifecycle-agent"
        ├── Agent "patch-agent"
        ├── Agent "backup-agent"
        └── Agent "monitoring-agent"

Team "product-lifecycle" (IAM principal, signed keypair)
  └── Team Lead "pl-leader" (navi with role: team-lead)
        ├── Agent "spec-agent"
        ├── Agent "detail-agent"
        └── Agent "issue-agent"
              │
              ▼
Team Manager "datacenter-team-manager" (navi or human)
  communicates with Team Leads via P2P
```

### 1.2 Data Structures

```go
// TeamPrincipal is the identity for an entire team.
type TeamPrincipal struct {
    Type      PrincipalType      // PrincipalTeam
    TeamID   string             // e.g. "corporate-it"
    PublicKey crypto.PublicKey   // team-level signing key
    LeadID    string             // name of the Team Lead navi
}

// RegionalPrincipal is a principal that can be a single agent, team, or role.
type RegionalPrincipal struct {
    Type    PrincipalType // "agent", "team", "role"
    ID      string        // agent name, team name, or role name
    TeamID string        // team this principal belongs to (optional)
}

const (
    PrincipalAgent PrincipalType = iota
    PrincipalTeam
    PrincipalRole
)

// TeamLead represents a navi designated as the team lead.
type TeamLead struct {
    Name       string            // navi name
    TeamID    string            // owning team
    Role       string            // "team-lead"
    PublicKey  crypto.PublicKey  // agent-level key
    Endpoint   string            // P2P address
    BackupIDs  []string          // backup leads (failover order)
}
```

### 1.3 Configuration

```yaml
# teams/corporate-it/team.yaml
team:
  name: "corporate-it"
  principal:
    id: "corporate-it"
    public_key_file: "keys/team-public.pem"
    private_key_file: "keys/team-private.pem"

  lead:
    name: "it-leader"
    backup:
      - "it-leader-2"
      - "it-leader-3"

  members:
    - "user-lifecycle-agent"
    - "patch-agent"
    - "backup-agent"
    - "monitoring-agent"
    - "compliance-agent"
    - "security-incident-agent"
    - "asset-agent"
    - "helpdesk-triage-agent"

  # Team-level IAM permissions
  iam:
    trust_policy: "require-trusted"
    inbound:
      - source: "team:product-lifecycle"
        actions: ["start", "send"]
        role: "team-lead"
    outbound:
      - target: "team:product-lifecycle"
        actions: ["start", "send"]
        role: "team-lead"

# teams/corporate-it/agents/it-leader/navi.yaml
navi:
  name: "it-leader"
  role: "team-lead"
  team: "corporate-it"
  identity:
    private_key_path: "/etc/opennavi/keys/it-leader"
    public_key_path: "/etc/opennavi/keys/it-leader.pub"
    trust_policy: "require-trusted"
```

### Requirement: Team Principal as IAM Identity
The system SHALL treat teams as first-class IAM principals, each owning its own keypair and verifying its identity via signed messages.

#### Scenario: Registering a team principal
- **GIVEN** a team is defined with a principal keypair and lead
- **WHEN** the team communicates with other teams
- **THEN** the system SHALL authenticate the team via signed messages
- **AND** the system SHALL use the team principal in IAM permission rules

### Requirement: Team Hierarchy with Lead and Members
The system SHALL support a team hierarchy where each team has a designated Team Lead navi, member agents, and backup leads for failover.

#### Scenario: Configuring team hierarchy
- **GIVEN** a team is configured with a lead, members, and backup leads
- **WHEN** the team is active
- **THEN** the system SHALL designate the lead as the single point of contact for the team
- **AND** the system SHALL track the backup leads in failover order

### Requirement: Team IAM Permission Rules
The system SHALL support team-level IAM permissions with inbound and outbound rules referencing team principals, roles, and actions.

#### Scenario: Granting a team-level inbound permission
- **GIVEN** a team has a team-level iam configuration
- **WHEN** another team's lead attempts to communicate
- **THEN** the system SHALL evaluate the inbound rules against the team principal and role
- **AND** the system SHALL allow the communication only if a matching rule permits it

---

## 01 Team Registry

### 2.1 P2P-Accessible Directory

The team registry is a P2P-accessible directory where teams register their identity, lead navi, capabilities, and health endpoint. Discovery works via the same presence broadcast mechanism as individual agents (see [01-p2p-chat-presence.md](./00-p2p-chat-presence.md)), extended with team metadata.

### 2.2 Data Structures

```go
// TeamRegistration is broadcast via P2P presence.
type TeamRegistration struct {
    Name         string            // team name
    LeadID       string            // current Team Lead navi name
    LeadEndpoint string            // P2P address of the Team Lead
    Capabilities []string          // what this team can do
    Version      string            // team spec version
    MemberCount  int               // number of agents in team
    HealthTopic  string            // P2P channel for health metrics
    PublicKey    crypto.PublicKey  // team principal key
    Signature    []byte            // signed by team private key
    Timestamp    time.Time
}

// TeamRegistry is maintained by each Team Manager or orchestrator.
type TeamRegistry struct {
    Teams map[string]TeamRegistration // team name -> registration
    mu     sync.RWMutex
}

func (r *TeamRegistry) Register(reg TeamRegistration) error {
    // 1. Verify team signature against claimed public key
    if !verifyTeamSignature(reg) {
        return ErrInvalidSignature
    }
    // 2. Verify the Team Lead's endorsement (signed by lead's key)
    if !verifyLeadEndorsement(reg) {
        return ErrUnauthorizedLead
    }
    // 3. Store or update
    r.mu.Lock()
    defer r.mu.Unlock()
    r.Teams[reg.Name] = reg
    return nil
}

func (r *TeamRegistry) LookupByCapability(cap string) []TeamRegistration {
    r.mu.RLock()
    defer r.mu.RUnlock()
    var result []TeamRegistration
    for _, reg := range r.Teams {
        for _, c := range reg.Capabilities {
            if c == cap {
                result = append(result, reg)
            }
        }
    }
    return result
}
```

### 2.3 Configuration

```yaml
# team-of-teams/orchestrator/navi.yaml
navi:
  name: "team-orchestrator"
  role: "team-manager"
  team_registry:
    enabled: true
    sync_interval: "60s"
    health_check_interval: "120s"
    stale_timeout: "300s"

    # What teams this orchestrator is responsible for
    managed_teams:
      - "corporate-it"
      - "product-lifecycle"
      - "e-commerce"
```

### Requirement: Team Registration via P2P Presence
The system SHALL allow teams to register in a P2P-accessible team registry via the same presence broadcast mechanism used by individual agents, extended with team metadata.

#### Scenario: Broadcasting a team registration
- **GIVEN** a team is configured with a registration
- **WHEN** the team broadcasts its presence
- **THEN** the broadcast SHALL include the team name, lead ID, lead endpoint, capabilities, version, member count, health topic, and public key
- **AND** the broadcast SHALL be signed by the team private key

### Requirement: Team Registration Verification
The system SHALL verify a team registration's signature and the Team Lead's endorsement before storing it in the registry.

#### Scenario: Verifying a team registration
- **GIVEN** a team registry receives a registration broadcast
- **WHEN** the registry processes the registration
- **THEN** the system SHALL verify the team signature against the claimed public key
- **AND** the system SHALL verify the Team Lead's endorsement signed by the lead's key
- **AND** the system SHALL store the registration only if both checks pass

### Requirement: Capability-Based Team Lookup
The system SHALL support looking up teams by capability from the registry.

#### Scenario: Finding a team by capability
- **GIVEN** a team registry contains registered teams with capabilities
- **WHEN** a caller looks up teams by a capability
- **THEN** the system SHALL return all teams whose capabilities include the requested capability

---

## 02 Observability Pipeline

### 3.1 Health Metrics Broadcast

Each team publishes health metrics to a shared P2P channel. The Team Lead aggregates per-agent metrics and broadcasts the team-level summary. The team-of-teams orchestrator subscribes to all team health channels.

### 3.2 Data Structures

```go
// TeamHealthMetrics is published periodically by the Team Lead.
type TeamHealthMetrics struct {
    TeamName      string
    LeadID         string
    AgentCount     int
    OnlineCount    int
    BusyCount      int
    IncidentCount  int            // active incidents
    UptimeSeconds  int64
    TaskThroughput int            // tasks completed in last interval
    AvgLatencyMs   float64
    MemoryUsedMB   int64
    CPUPercent     float64
    Signature      []byte
    Timestamp      time.Time
}

// TeamHealthAggregator collects metrics from all teams.
type TeamHealthAggregator struct {
    TeamMetrics map[string][]TeamHealthMetrics // last N samples per team
    AlertThresholds map[string]float64
}

func (a *TeamHealthAggregator) Aggregate() TeamOfTeamsHealth {
    var total TeamOfTeamsHealth
    for name, samples := range a.TeamMetrics {
        if len(samples) == 0 {
            continue
        }
        latest := samples[len(samples)-1]
        total.Teams = append(total.Teams, latest)
        total.TotalAgents += latest.AgentCount
        total.TotalIncidents += latest.IncidentCount
        total.TasksCompleted += latest.TaskThroughput
    }
    total.Timestamp = time.Now()
    return total
}

type TeamOfTeamsHealth struct {
    Teams         []TeamHealthMetrics
    TotalAgents    int
    TotalIncidents int
    TasksCompleted int
    Timestamp      time.Time
}
```

### 3.3 Configuration

```yaml
# teams/corporate-it/agents/it-leader/navi.yaml (extended)
navi:
  name: "it-leader"
  team: "corporate-it"
  observability:
    enabled: true
    health_topic: "team-health/corporate-it"
    publish_interval: "30s"
    metrics:
      - agent_count
      - incident_count
      - uptime
      - task_throughput
      - avg_latency_ms
      - memory_used_mb
      - cpu_percent
```

### Requirement: Team Health Metrics Broadcast
The system SHALL require each Team Lead to aggregate per-agent metrics and publish team-level health metrics to a shared P2P channel at a configured interval.

#### Scenario: Publishing team health metrics
- **GIVEN** a Team Lead has observability enabled
- **WHEN** the publish_interval elapses
- **THEN** the system SHALL publish signed health metrics to the configured health topic
- **AND** the metrics SHALL include agent count, online count, busy count, incident count, uptime, task throughput, latency, memory, and CPU usage

### Requirement: Health Aggregation Across Teams
The system SHALL aggregate health metrics from all teams into a team-of-teams summary for the orchestrator.

#### Scenario: Aggregating health from multiple teams
- **GIVEN** a team-of-teams orchestrator subscribes to team health channels
- **WHEN** health samples arrive from multiple teams
- **THEN** the system SHALL collect the latest sample per team
- **AND** the system SHALL compute totals for agents, incidents, and tasks completed across all teams

---

## 03 Cost Attribution

### 4.1 Per-Team Usage Tracking

Each team tracks resource consumption and reports to a central cost aggregation service. The Team Lead is responsible for collecting and forwarding usage data from its agents.

### 4.2 Data Structures

```go
// TeamUsageReport is sent by the Team Lead to the cost aggregator.
type TeamUsageReport struct {
    TeamName   string
    PeriodStart time.Time
    PeriodEnd   time.Time
    Agents      []AgentUsage
    Totals      ResourceTotals
    Signature   []byte
}

type AgentUsage struct {
    AgentName     string
    APICalls      int64
    LLMTokensIn   int64
    LLMTokensOut  int64
    ComputeTimeMs int64
    MemoryMbAvg   float64
}

type ResourceTotals struct {
    TotalAPICalls      int64
    TotalLLMTokensIn   int64
    TotalLLMTokensOut  int64
    TotalComputeTimeMs int64
    CostEstimateUSD    float64
}

// CostAggregator receives reports from all Team Leads.
type CostAggregator struct {
    Reports     map[string][]TeamUsageReport
    Budgets     map[string]float64 // team name -> budget limit
    AlertOnOver bool
}

func (a *CostAggregator) IngestReport(report TeamUsageReport) error {
    if !verifyTeamSignature(report) {
        return ErrInvalidSignature
    }
    a.Reports[report.TeamName] = append(
        a.Reports[report.TeamName], report,
    )
    // Check budget threshold
    teamBudget := a.Budgets[report.TeamName]
    if teamBudget > 0 && report.Totals.CostEstimateUSD > teamBudget {
        return ErrBudgetExceeded
    }
    return nil
}
```

### 4.3 Configuration

```yaml
# team-of-teams/cost-aggregator/navi.yaml
navi:
  name: "cost-aggregator"
  cost_attribution:
    enabled: true
    report_interval: "1h"
    currencies: ["USD"]
    team_budgets:
      corporate-it: 500.00
      product-lifecycle: 750.00
      e-commerce: 1200.00
    alert_on_over: true
    alert_channel: "slack:#finops"

# teams/corporate-it/agents/it-leader/navi.yaml (extended)
navi:
  name: "it-leader"
  team: "corporate-it"
  cost_attribution:
    enabled: true
    report_endpoint: "p2p://cost-aggregator/reports"
    report_interval: "1h"
    track:
      - api_calls
      - llm_tokens
      - compute_time
```

### Requirement: Per-Team Usage Tracking
The system SHALL require each Team Lead to collect resource usage data from its agents and send a signed usage report to the cost aggregator at a configured interval.

#### Scenario: Sending a team usage report
- **GIVEN** a Team Lead has cost attribution enabled
- **WHEN** the report_interval elapses
- **THEN** the system SHALL collect API calls, LLM tokens, compute time, and memory usage per agent
- **AND** the system SHALL send a signed usage report to the configured cost aggregator endpoint

### Requirement: Cost Report Verification and Budget Enforcement
The system SHALL verify the signature of incoming team usage reports and enforce per-team budgets, alerting when a budget is exceeded.

#### Scenario: Rejecting an unsigned usage report
- **GIVEN** a cost aggregator receives a usage report
- **WHEN** the report signature verification fails
- **THEN** the system SHALL reject the report

#### Scenario: Exceeding a team budget
- **GIVEN** a team has a configured budget
- **WHEN** the team's cost estimate exceeds the budget
- **THEN** the system SHALL flag the budget as exceeded
- **AND** the system SHALL trigger the configured alert when alert_on_over is enabled

---

## 04 Governance

### 5.1 Team-Scoped IAM Policies

Policies define which teams can communicate, what data they share, and what actions they can perform. Policies are evaluated at the team boundary by the Team Lead.

### 5.2 Data Structures

```go
// TeamPolicy defines governance rules for a team.
type TeamPolicy struct {
    TeamName    string
    Version      string
    Statements   []PolicyStatement
    Signature    []byte
}

type PolicyStatement struct {
    Effect   string   // "allow" | "deny"
    Principal string   // "team:*" | "team:corporate-it" | "role:team-lead"
    Action   string   // "team:communicate" | "team:share-data" | "team:delegate"
    Resource string   // "team:product-lifecycle" | "topic:health/*"
    Condition PolicyCondition // optional
}

type PolicyCondition struct {
    SourceTeam    string
    MinTrustLevel  int
    MaxAgents      int
    RequiredCapabilities []string
}

// TeamPolicyEnforcer evaluates policies at the team boundary.
type TeamPolicyEnforcer struct {
    Policies map[string]TeamPolicy
}

func (e *TeamPolicyEnforcer) Check(principal, action, resource string) error {
    // 1. Deny rules take precedence
    for _, stmt := range e.matchStatements(principal, action, resource) {
        if stmt.Effect == "deny" {
            return ErrPolicyDenied
        }
    }
    // 2. Allow rules must match
    for _, stmt := range e.matchStatements(principal, action, resource) {
        if stmt.Effect == "allow" {
            return nil
        }
    }
    return ErrPolicyDenied
}
```

### 5.3 Configuration

```yaml
# teams/corporate-it/policies.yaml
team_policy:
  team: "corporate-it"
  version: "1.0"
  statements:
    # Allow communication with product-lifecycle team
    - effect: "allow"
      principal: "team:corporate-it"
      action: "team:communicate"
      resource: "team:product-lifecycle"
      condition:
        min_trust_level: 3
        required_capabilities: ["incident-response"]

    # Allow sharing health metrics with the orchestrator
    - effect: "allow"
      principal: "team:corporate-it"
      action: "team:share-data"
      resource: "topic:team-health/*"

    # Deny sharing sensitive agent data
    - effect: "deny"
      principal: "team:corporate-it"
      action: "team:share-data"
      resource: "topic:agent-internal/*"

    # Allow delegation to backup leads
    - effect: "allow"
      principal: "role:team-lead"
      action: "team:delegate"
      resource: "team:corporate-it/*"

# team-of-teams/orchestrator/governance.yaml
cross_team_policies:
  - source: "corporate-it"
    target: "product-lifecycle"
    allowed_actions:
      - "communicate"
      - "share-health"
    denied_actions:
      - "share-agent-data"
      - "delegate"
  - source: "corporate-it"
    target: "e-commerce"
    allowed_actions: []  # no cross-team communication allowed
```

### Requirement: Team-Scoped IAM Policies
The system SHALL support team policies that define which teams can communicate, what data they share, and what actions they perform, evaluated at the team boundary.

#### Scenario: Defining a team policy with statements
- **GIVEN** a team has a policy file with allow and deny statements
- **WHEN** a team boundary action is evaluated
- **THEN** the system SHALL match the action against the policy statements
- **AND** the system SHALL evaluate any conditions such as trust level or required capabilities

### Requirement: Policy Enforcement with Deny Precedence
The system SHALL enforce team policies with deny rules taking precedence over allow rules.

#### Scenario: Denying sensitive data sharing
- **GIVEN** a team policy contains a deny statement for sharing agent-internal data
- **WHEN** an agent attempts to share agent-internal data
- **THEN** the system SHALL deny the action regardless of any matching allow statements

#### Scenario: Allowing a permitted cross-team action
- **GIVEN** a team policy contains an allow statement for a resource
- **WHEN** an agent attempts the allowed action
- **THEN** the system SHALL permit the action
- **AND** the system SHALL deny the action if no allow statement matches

### Requirement: Cross-Team Policy Configuration
The system SHALL support cross-team policy configuration defining allowed and denied actions between specific teams.

#### Scenario: Restricting communication between teams
- **GIVEN** a cross-team policy defines allowed and denied actions between two teams
- **WHEN** a team attempts an action toward another team
- **THEN** the system SHALL permit only the listed allowed_actions
- **AND** the system SHALL deny any listed denied_actions

---

## 05 Lifecycle: Team Manager → Team Lead

```
Team Manager                     Team Lead "it-leader"
     │                                   │
     │  1. Discovers team via P2P       │
     │  presence broadcast               │
     │  ◄────────────────────────────── │
     │                                   │
     │  2. Verifies team signature      │
     │  against trusted key store        │
     │                                   │
     │  3. Opens P2P chat session        │
     │  ──────────────────────────────► │
     │                                   │
     │  4. Sends instruction:            │
     │  "Deploy patch v2.1 to all        │
     │   agents in corporate-it"         │
     │  ──────────────────────────────► │
     │                                   │
     │  5. Team Lead delegates          │
     │  internally to agents             │
     │                                   │
     │  6. Team Lead returns            │
     │  signed acknowledgment            │
     │  ◄────────────────────────────── │
     │                                   │
     │  7. Health metrics published      │
     │  on P2P topic every 30s           │
     │  ◄────────────────────────────── │
     │                                   │
     │  8. Cost report sent              │
     │  every 1h (signed)               │
     │  ──────────────────────────────► │
```

### Requirement: Team Manager to Team Lead Communication
The system SHALL support a lifecycle where a Team Manager discovers a team via P2P presence, verifies the team signature, opens a chat session with the Team Lead, sends instructions, and receives signed acknowledgments.

#### Scenario: Managing a team through its lead
- **GIVEN** a Team Manager has verified a team's registration
- **WHEN** the Team Manager sends an instruction to the team
- **THEN** the system SHALL route the instruction through a P2P chat session to the Team Lead
- **AND** the Team Lead SHALL delegate the instruction internally to team agents
- **AND** the Team Lead SHALL return a signed acknowledgment
- **AND** the team SHALL publish health metrics and signed cost reports on their configured intervals

### Requirement: Team Reporting Loop
The system SHALL maintain a periodic reporting loop between the Team Lead and the Team Manager, publishing signed health metrics and cost reports on configured intervals.

#### Scenario: Receiving periodic team reports
- **GIVEN** a team is managed by a Team Manager
- **WHEN** the configured reporting interval elapses
- **THEN** the Team Lead SHALL publish health metrics on the P2P health topic
- **AND** the Team Lead SHALL send signed cost reports to the cost aggregator

---

## Related

### Other Parts

- [P2P Chat & Presence](./00-p2p-chat-presence.md) — presence discovery that team registration extends
- [Trust, Identity & Sessions](./01-trust-identity-sessions.md) — key verification for team principals
- [IAM & Idle Scheduling](./02-iam-idle-scheduling.md) — base IAM model that team principals extend
- [GAPS.md](../../gaps/GAPS.md) — Gap 7 source of this spec

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S040400 | 0.1.0 | Migrated from docs/spec/ to spec-tree |