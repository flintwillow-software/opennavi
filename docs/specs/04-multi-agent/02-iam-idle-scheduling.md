---
status: draft
created: '24'
updated: '2026-08-11'
tags:
  - spec
  - multi-agent
priority: medium
spec-refs: [040200, 040201, 040202, 040203]
---

# Plan: Inter-Agent Communication (P2P Chat Model): IAM & Idle Scheduling

**Scope:** Peer-to-peer chat between navis, presence detection, session memory, IAM-based chat permissions, work discovery via idle scheduling, public key trust model.

> **Part of:** [Plan: Inter-Agent Communication (./README.md) — split into 4 parts for readability.

## 00 IAM for Chat Permissions

### 6.1 The Permission Model

Not every navi can talk to every other navi. IAM permissions define which navis can initiate sessions with which others. **Authentication (key verification) answers "who is talking?" — IAM answers "are they allowed to talk to me?"** Both must pass.

```go
type ChatPermission struct {
    Source      string   // source navi name (or "*" for all)
    Target      string   // target navi name (or "*" for all)
    Actions     []string // "start", "send", "receive"
    MaxSessions int      // max concurrent sessions (0 = unlimited)
    MaxRate     int      // max messages per minute
}
```

### 6.2 Configuration

```yaml
# navis/spec-agent/navi.yaml
navi:
  name: "spec-agent"
  # ...
  chat:
    enabled: true
    listen_port: 0  # 0 = OS-assigned (for socket-based chat)

    # Who can talk to me
    permissions:
      # detail-agent can start sessions with me
      - source: "detail-agent"
        target: "spec-agent"
        actions: ["start", "send"]
        max_sessions: 5

      # issue-agent can send messages in existing sessions
      - source: "issue-agent"
        target: "spec-agent"
        actions: ["send"]  # can't start, only respond
        max_sessions: 5

      # All coder agents can send messages
      - source: "coder-agent-*"
        target: "spec-agent"
        actions: ["send"]
        max_sessions: 10

    # Who I can talk to
    outbound_permissions:
      - source: "spec-agent"
        target: "detail-agent"
        actions: ["start", "send"]
      - source: "spec-agent"
        target: "issue-agent"
        actions: ["send"]
```

### 6.3 Permission Evaluation

```go
func (p *ChatPermissionManager) Check(source, target string, action string) error {
    // 1. Check source-specific rules
    for _, rule := range p.inboundRules {
        if matchNavi(rule.Source, source) && matchNavi(rule.Target, target) {
            for _, a := range rule.Actions {
                if a == action {
                    return nil // allowed
                }
            }
        }
    }

    // 2. Check wildcard rules
    for _, rule := range p.inboundRules {
        if rule.Source == "*" && matchNavi(rule.Target, target) {
            for _, a := range rule.Actions {
                if a == action {
                    return nil // allowed by wildcard
                }
            }
        }
    }

    // 3. Check outbound rules
    for _, rule := range p.outboundRules {
        if matchNavi(rule.Source, source) && matchNavi(rule.Target, target) {
            for _, a := range rule.Actions {
                if a == action {
                    return nil // allowed
                }
            }
        }
    }

    return fmt.Errorf("permission denied: %s cannot %s to %s", source, action, target)
}

// matchNavi handles wildcard matching (e.g., "coder-agent-*" matches "coder-agent-1")
func matchNavi(pattern, name string) bool {
    if pattern == name {
        return true
    }
    if strings.HasSuffix(pattern, "-*") {
        prefix := strings.TrimSuffix(pattern, "-*")
        return strings.HasPrefix(name, prefix)
    }
    return false
}
```

### 6.4 The Full Message Acceptance Pipeline

Authentication and authorization compose:

```
Incoming message
    │
    ▼
1. VERIFY KEY (trust model)
   Is the sender's public key in my trusted store?
   Is the signature valid?
   │
   ├── NO → drop. Log. (never creates a thought)
   │
   ▼
2. CHECK IAM (authorization)
   Is the sender allowed to send me messages?
   │
   ├── NO → drop. Log. Alert.
   │
   ▼
3. CREATE THOUGHT
   Add to queue with chat metadata
    │
    ▼
4. AGENT LOOP PROCESSES IT (like any other thought)
```

### 6.5 IAM Enforcement

```go
func (n *Navi) handleChatSend(input ChatSendInput) (*ChatSendResult, error) {
    // 1. Check IAM permission (outbound)
    action := "send"
    if input.SessionID == "" {
        action = "start" // new session
    }

    err := n.chatPermissions.Check(n.Name, input.Recipient, action)
    if err != nil {
        return nil, fmt.Errorf("chat permission denied: %w", err)
    }

    // 2. Check presence (is the recipient online AND trusted?)
    presence, ok := n.presenceStore.Get(input.Recipient)
    if !ok || !presence.Trusted {
        return &ChatSendResult{
            Status: "rejected",
            RecipientStatus: "offline",
        }, nil
    }

    // 3. Create or update session
    session, err := n.getOrCreateSession(input)
    if err != nil {
        return nil, err
    }

    // 4. Sign the message payload with our private key
    signature, err := n.signPayload(session.BuildWirePayload(input))
    if err != nil {
        return nil, err
    }

    // 5. Deliver the signed message to the recipient's P2P endpoint
    err = n.deliverToNavi(input.Recipient, session, input, signature)
    if err != nil {
        return nil, err
    }

    // 6. Store locally (with signature for audit)
    session.AddMessage(Message{
        From:      n.Name,
        Content:   input.Message,
        Signature: signature,
        Metadata:  input.Metadata,
        Timestamp: time.Now(),
    })

    return &ChatSendResult{
        SessionID: session.ID,
        Status:    "delivered",
        RecipientStatus: string(presence.Message.Status),
        Turn:      input.Metadata.Turn,
    }, nil
}
```

### Requirement: IAM Permission Model for Chat
The system SHALL enforce IAM permissions for chat that define which navis can initiate sessions, send messages, and receive messages, with support for wildcard matching.

#### Scenario: Configuring an inbound chat permission
- **GIVEN** a navi has a chat permission rule defined
- **WHEN** another navi attempts to send a message
- **THEN** the system SHALL match the source and target navi names against the permission rules
- **AND** the system SHALL allow the action only if a matching rule permits it
- **AND** the system SHALL support wildcard patterns such as coder-agent-* to match multiple navis

### Requirement: Authentication and Authorization Pipeline
The system SHALL enforce a two-stage pipeline for incoming messages: first verify the sender's signature (authentication), then check IAM permissions (authorization), and only create a thought if both pass.

#### Scenario: Processing an incoming message through the pipeline
- **GIVEN** a navi receives an incoming chat message
- **WHEN** the message is processed
- **THEN** the system SHALL first verify the sender's public key and signature
- **AND** if authentication fails, the system SHALL drop the message without creating a thought
- **AND** if authentication passes, the system SHALL check IAM permissions
- **AND** if authorization fails, the system SHALL drop the message and log an alert
- **AND** only if both pass, the system SHALL create a thought and add it to the queue

### Requirement: Outbound Permission Enforcement
The system SHALL enforce outbound IAM permissions when a navi initiates a chat, checking both the action type (start vs send) and presence of the recipient before delivering.

#### Scenario: Enforcing outbound permission on session start
- **GIVEN** a navi calls chat_send to start a new session
- **WHEN** the system processes the outbound request
- **THEN** the system SHALL check if the source navi has the start action permitted for the target
- **AND** the system SHALL check if the recipient is online and trusted in the presence store
- **AND** the system SHALL reject the message if either check fails

---

## 01 Team-Level Principals

> **Status:** Spec'd as an extension of the IAM model. Not yet implemented.

### 6.6.1 Why Team Principals?

In a team-of-teams architecture (see [GAPS.md](../../gaps/GAPS.md) Gap 7), individual navis are organized into **teams** — logical groups of agents that work together (e.g., "corporate-it-team", "product-lifecycle-team"). Each team has a **Team Lead** agent that manages the team and communicates with other teams.

IAM permissions need to operate at the team level, not just individual agent level. This is analogous to AWS IAM where you can grant permissions to a role (team) rather than a specific user (agent).

### 6.6.2 Team Principal Model

```go
type Principal struct {
    Type      PrincipalType // "agent", "team", "role"
    ID        string        // agent name, team name, or role name
    TeamID   string        // team this principal belongs to (optional)
    PublicKey crypto.PublicKey // used for authentication
}

type PrincipalType int

const (
    PrincipalAgent PrincipalType = iota // Single navi agent
    PrincipalTeam                       // Entire team (all agents in it)
    PrincipalRole                        // Role within a team (e.g., "team-lead")
)
```

### 6.6.3 Team-Level Configuration

```yaml
# navis/corporate-it-team/team.yaml
team:
  name: "corporate-it-team"
  lead: "it-leader"
  members:
    - "user-lifecycle-agent"
    - "patch-agent"
    - "backup-agent"
    - "monitoring-agent"
    - "compliance-agent"
    - "security-incident-agent"
    - "asset-agent"
    - "helpdesk-triage-agent"
  principal:
    id: "corporate-it"
    public_key_file: "keys/team-public.pem"

  # Team-level IAM permissions
  chat:
    permissions:
      # Other teams can talk to the team lead
      - source: "*"
        target: "team:corporate-it"
        actions: ["start", "send"]
        role: "team-lead"  # Only the team lead can receive
        max_sessions: 10

      # Team members can talk to each other freely
      - source: "team:corporate-it"
        target: "team:corporate-it"
        actions: ["start", "send"]
        role: "*"

    # Outbound: who this team can talk to
    outbound_permissions:
      - source: "team:corporate-it"
        target: "team:product-lifecycle"
        actions: ["start", "send"]
        role: "team-lead"
```

### 6.6.4 Team Manager → Team Lead Communication

A **Team Manager** is a higher-level navi (or a human operator) that manages multiple teams:

```yaml
# navis/team-manager/navi.yaml
navi:
  name: "datacenter-team-manager"
  chat:
    permissions:
      # Team manager can talk to any team lead
      - source: "team-manager"
        target: "team:corporate-it"
        actions: ["start", "send"]
        role: "team-lead"
      - source: "team-manager"
        target: "team:product-lifecycle"
        actions: ["start", "send"]
        role: "team-lead"
```

### 6.6.5 Permission Evaluation with Team Principals

```go
func (p *ChatPermissionManager) CheckWithTeam(source, target Principal, action string) error {
    // 1. Resolve team principals to individual agents
    sources := p.resolvePrincipals(source)
    targets := p.resolvePrincipals(target)

    // 2. Check all source-target combinations
    for _, s := range sources {
        for _, t := range targets {
            if err := p.check(s, t, action); err == nil {
                return nil // At least one path allows it
            }
        }
    }

    return fmt.Errorf("no permission path from %s:%s to %s:%s",
        source.Type, source.ID, target.Type, target.ID)
}

func (p *ChatPermissionManager) resolvePrincipals(principal Principal) []Principal {
    switch principal.Type {
    case PrincipalAgent:
        return []Principal{principal}
    case PrincipalTeam:
        // Return all agents in the team
        return p.teamRegistry.GetMembers(principal.ID)
    case PrincipalRole:
        // Return agents with the matching role in the team
        return p.teamRegistry.GetByRole(principal.TeamID, principal.ID)
    }
    return nil
}
```

### 6.6.6 Team Registry

A team registry is a P2P-accessible directory of teams, their leads, and capabilities. Navis discover teams via presence broadcasts, just like they discover individual agents.

```yaml
# Team registry entry (broadcast via P2P presence)
team_registration:
  team:
    name: "corporate-it-team"
    lead: "it-leader"
    capabilities:
      - "user-provisioning"
      - "patch-management"
      - "backup-monitoring"
      - "compliance-scanning"
      - "incident-response"
    version: "1.2.0"
    health: "healthy"
    member_count: 8
```

### 6.6.7 Design Decisions

1. **Team as a principal type** — not just a label. Teams have their own identity, keypair, and permissions.
2. **Team Lead as the single point of contact** — external teams talk to the lead, not individual members. The lead delegates internally.
3. **Role-based access within teams** — `team-lead`, `team-member`, `team-observer` roles control what each agent can do.
4. **Team registry via P2P presence** — no centralized registry needed. Teams discover each other through the same presence mechanism as individual agents.
5. **Graceful degradation** — if the team lead is unavailable, a backup lead takes over (elected via P2P consensus).

### 7.1 The Problem

In the SDLC pipeline, agents shouldn't sit idle. When a navi has no active thoughts in its queue, it should proactively look for work. This is handled by **idle scheduled thoughts** — the navi schedules a recurring thought that fires when the queue is empty.

### 7.2 Idle Schedule Configuration

```yaml
# navis/coder-agent/navi.yaml
navi:
  name: "coder-agent"
  # ...
  idle:
    enabled: true
    check_interval: "60s"          # check for work every 60s when idle
    max_checks_before_backoff: 5   # after 5 checks with no work, back off
    backoff_multiplier: 2          # double the interval each backoff
    max_backoff: "30m"             # never back off more than 30 min

    # Work discovery rules
    work_sources:
      - type: "board"              # pull-based discovery from the PM board (sruth)
        endpoint: "http://localhost:8090/api/v1/projects/:id/board/discover"
        role: "backend-engineer"   # board filters items by role label
        max_pick: 1                # pick at most 1 item per check
        claim_strategy: "assign"   # board assigns on pull (transition_issue)

      - type: "github-issue"
        repo: "flintwillow-software/auth-service"
        query: "label:ready is:open no:assignee"
        max_pick: 1                # pick at most 1 issue per check
        claim_strategy: "comment"  # claim by commenting on the issue

      - type: "chat-broadcast"
        target_role: "planner"     # ask the PM for work
        message: "I'm available. Any tasks that need coding?"
```

### 7.3 Idle Check Flow

```
coder-agent finishes all thoughts
    │
    ▼
Queue is empty → idle check timer starts (60s)
    │
    ▼
Idle check fires:
    1. Query the board's pull discovery endpoint for my role (label:backend, class priority)
    2. If found → pull the item (board assigns; transition_issue to In Progress)
    3. Else → check GitHub for issues with label "ready" and no assignee
    4. If not found → broadcast to pm-agent: "I'm available for work"
    5. If no work at all → back off (120s, 240s, ... up to 30m)
    │
    ▼
Back to idle. If a thought arrives (from chat or webhook), process it immediately.
```

### 7.4 The PM Agent's Role

The PM agent (`issue-agent` in the SDLC case) has its own idle schedule that checks for agent requests:

```yaml
# navis/issue-agent/navi.yaml
navi:
  name: "issue-agent"
  # ...
  idle:
    enabled: true
    check_interval: "30s"           # PM checks more frequently
    work_sources:
      - type: "github-issue"
        repo: "flintwillow-software/auth-service"
        query: "label:needs-assignment is:open"
        claim_strategy: "assign"    # PM can assign directly

      - type: "github-comment"
        repo: "flintwillow-software/auth-service"
        query: "mentions:pm-agent"  # check for @pm-agent mentions
        action: "respond-and-assign"
```

**PM idle flow:**

```
pm-agent is idle:
    1. Check for @pm-agent mentions on issues
    2. If coder-agent commented "I can take this" → assign it
    3. If unassigned ready tasks → check which coders are available (presence)
    4. Assign task to an available coder-agent
    5. Send signed chat message: "coder-agent, task #47 is assigned to you"
```

### 7.5 Work Claiming Protocol

When a coder-agent finds work and wants to claim it:

```
1. coder-agent finds issue #47 with label "ready" and no assignee
2. coder-agent comments on issue #47:
   "@pm-agent I can take this. I'm available and have bandwidth."
3. coder-agent continues idle loop (or processes other thoughts)

4. pm-agent's idle check fires:
   - Finds comment mentioning @pm-agent on issue #47
   - Checks coder-agent's presence: "online" and trusted
   - Assigns issue #47 to coder-agent
   - Updates status: "Ready" → "In Progress"
   - Sends signed chat to coder-agent:
     "Issue #47 is assigned to you. Start implementing Google OAuth2 client."

5. coder-agent receives chat message (verified signature):
   - Thought arrives: "Issue #47 is assigned to you..."
   - coder-agent picks up the thought
   - Starts implementing
```

### 7.6 Idle Check Implementation

```go
type IdleScheduler struct {
    config           IdleConfig
    checkCount       int
    currentInterval  time.Duration
    agent            *Navi
    stopCh           chan struct{}
}

func (is *IdleScheduler) Start(ctx context.Context) {
    is.currentInterval = is.config.CheckInterval

    for {
        select {
        case <-ctx.Done():
            return
        case <-time.After(is.currentInterval):
            // Only run if queue is empty
            if !is.agent.Queue.IsEmpty() {
                is.checkCount = 0
                is.currentInterval = is.config.CheckInterval
                continue
            }

            // Run work discovery
            is.checkWork(ctx)

            // Update backoff
            is.checkCount++
            if is.checkCount >= is.config.MaxChecksBeforeBackoff {
                is.currentInterval = minDuration(
                    is.currentInterval * time.Duration(is.config.BackoffMultiplier),
                    is.config.MaxBackoff,
                )
            }
        }
    }
}

func (is *IdleScheduler) checkWork(ctx context.Context) {
    for _, source := range is.config.WorkSources {
        switch source.Type {
        case "github-issue":
            is.checkGitHubIssues(ctx, source)
        case "chat-broadcast":
            is.broadcastAvailability(ctx, source)
        case "github-comment":
            is.checkGitHubMentions(ctx, source)
        }
    }
}

func (is *IdleScheduler) checkGitHubIssues(ctx context.Context, source WorkSource) {
    // Query GitHub for issues matching the source query
    issues, err := gh.SearchIssues(source.Query, source.Repo)
    if err != nil || len(issues) == 0 {
        return
    }

    // Claim the first issue
    issue := issues[0]
    switch source.ClaimStrategy {
    case "comment":
        // Comment to claim
        gh.AddComment(issue.Repo, issue.Number,
            fmt.Sprintf("@pm-agent I can take this. I'm available and have bandwidth."))
        log.Printf("Claimed issue #%d via comment", issue.Number)

    case "assign":
        // PM agent: assign directly
        gh.AssignIssue(issue.Repo, issue.Number, is.agent.Name)
        log.Printf("Assigned issue #%d to %s", issue.Number, is.agent.Name)
    }
}
```

### Requirement: Team Principal Model
The system SHALL support team-level principals as first-class IAM identities, where each team owns a keypair, has a designated Team Lead, and can be referenced in permission rules.

#### Scenario: Configuring a team principal
- **GIVEN** a team is defined with a name, principal keypair, and lead
- **WHEN** the team registers on the network
- **THEN** the system SHALL treat the team as a first-class IAM principal
- **AND** the team SHALL be referenced by its team ID in permission rules

### Requirement: Team-Level IAM Configuration
The system SHALL support team-level IAM permissions where inbound and outbound rules reference team principals, roles, and wildcard patterns.

#### Scenario: Granting inter-team communication permission
- **GIVEN** two teams are defined with team-level IAM configuration
- **WHEN** one team's lead attempts to communicate with the other team
- **THEN** the system SHALL evaluate the team-level permission rules
- **AND** the system SHALL allow the communication only if a matching allow rule exists

### Requirement: Team Principal Permission Resolution
The system SHALL resolve team principals to individual agents when evaluating permissions, supporting agent, team, and role principal types.

#### Scenario: Resolving a team principal to member agents
- **GIVEN** a permission rule targets a team principal
- **WHEN** the system evaluates the rule
- **THEN** the system SHALL resolve the team to its member agents
- **AND** the system SHALL allow the action if any agent within the team has a matching permission path

### Requirement: Idle Schedule Configuration
The system SHALL support an idle schedule for navis that defines the check interval, backoff strategy, and work discovery sources.

#### Scenario: Configuring idle check with backoff
- **GIVEN** a navi is configured with idle scheduling enabled
- **WHEN** the navi's queue is empty
- **THEN** the system SHALL check for work at the configured check_interval
- **AND** after max_checks_without_work, the system SHALL apply exponential backoff up to the configured maximum

### Requirement: Work Discovery via Board

The system SHALL support board-based work discovery where a navi queries the PM board's pull discovery endpoint filtered by its role, pulls the highest-priority item, and transitions it to In Progress on claim.

#### Scenario: Pulling from the board
- **GIVEN** a navi with a board work source configured for its role
- **WHEN** the idle check fires
- **THEN** the navi SHALL query the board for available items matching its role
- **AND** the navi SHALL pull the highest-priority item (critical > expedite > fixed-date > standard)
- **AND** the board SHALL transition the item to In Progress and assign it

### Requirement: Work Claiming Protocol
The system SHALL support a work claiming protocol where a navi discovers work, claims it via comment, and receives assignment through a signed chat message.

#### Scenario: Claiming a task via comment
- **GIVEN** a navi discovers an unassigned task during idle check
- **WHEN** the navi claims the task
- **THEN** the navi SHALL comment on the task claiming availability
- **AND** the PM agent SHALL detect the claim during its own idle check
- **AND** the PM agent SHALL assign the task and send a signed chat message confirming the assignment

### Requirement: Idle Check with Backoff
The system SHALL implement backoff for idle checks, doubling the interval after each failed check up to a configurable maximum, and resetting to the base interval when work arrives.

#### Scenario: Backoff behavior on consecutive empty checks
- **GIVEN** a navi has performed max_checks_before_backoff idle checks with no work found
- **WHEN** the next idle check fires
- **THEN** the system SHALL multiply the check interval by the backoff_multiplier
- **AND** the system SHALL cap the interval at the configured max_backoff
- **AND** the system SHALL reset the interval to the base check_interval when a thought arrives

---


## 02 Putting It All Together: SDLC Pipeline with P2P Chat

### 8.1 Agent Startup Sequence

```
1. Each agent starts:
   a. Loads its config (navi.yaml)
   b. Loads its keypair (identity.private_key_path)
   c. Loads its trusted key store (identity.trusted_keys)
   d. Initializes presence detection
   e. Broadcasts: "I'm <name>, role: <role>, online" [signed]
   f. Discovers other agents via presence, verifies their keys
   g. Starts idle scheduler
   h. Starts thought queue worker pool
   i. Listens for incoming chat messages

2. All agents discover each other (only trusted ones):
   spec-agent  → knows: detail-agent, issue-agent, coder-agent-*, reviewer-agent
   detail-agent → knows: spec-agent, issue-agent, coder-agent-*, reviewer-agent
   issue-agent  → knows: spec-agent, detail-agent, coder-agent-*, reviewer-agent
   coder-agent-1 → knows: all of the above
   reviewer-agent → knows: all of the above
```

### 8.2 Full Pipeline with P2P Chat

```
User submits feature request (webhook to spec-agent)
    │
    ▼
spec-agent:
  1. Creates user story issue #42
  2. Calls chat_send(recipient: "detail-agent", message: "Issue #42 is ready",
     metadata: {context: "handoff", turn: "recipient", artifact_refs: [issue#42]})
     → message signed with spec-agent's key
  3. Continues processing other thoughts
    │
    ▼
detail-agent:
  1. Receives signed chat message, verifies spec-agent's signature ✓
  2. Turns message into thought, adds to queue
  3. Picks up thought, loads issue #42, expands the spec
  4. Updates issue #42 with implementation plan
  5. Calls chat_send(session_id: "sess_abc", recipient: "spec-agent",
     message: "Completed. Added implementation plan, status: Needs Breakdown",
     metadata: {turn: "sender", artifact_refs: [issue#42], context: "handoff-complete"})
     → message signed with detail-agent's key
  6. Calls chat_send(recipient: "issue-agent",  // new session for next handoff
     message: "Issue #42 has a detailed implementation plan. Ready for breakdown.",
     metadata: {turn: "recipient", artifact_refs: [issue#42], context: "handoff"})
    │
    ▼
issue-agent:
  1. Receives signed chat from detail-agent, verifies ✓
  2. Loads issue #42, decomposes into epics and tasks
  3. Creates epics #43-#45 and tasks #46-#57
  4. Updates project board
  5. Calls chat_send(recipient: "detail-agent",
     message: "Decomposed. Created 3 epics, 12 tasks.",
     metadata: {turn: "closed", context: "handoff-complete"})
  6. Sessions go idle, TTL will close them
    │
    ▼
    (issue-agent broadcasts to coder pool via presence)

coder-agent-1 (idle check fires):
  1. Checks GitHub: "label:ready is:open no:assignee"
  2. Finds task #47: "Implement Google OAuth2 client"
  3. Comments: "@pm-agent I can take this."
    │
    ▼
issue-agent (idle check fires):
  1. Checks for @pm-agent mentions
  2. Finds coder-agent-1's comment on #47
  3. Checks presence: coder-agent-1 is online AND trusted (key verified)
  4. Assigns issue #47 to coder-agent-1
  5. Status: "Ready" → "In Progress"
  6. Sends signed chat: "coder-agent-1, task #47 is yours. Google OAuth2 client."
    │
    ▼
coder-agent-1:
  1. Receives signed chat: "task #47 is yours", verifies ✓
  2. Picks up the task
  3. Implements, writes tests
  4. Signs commits with its SSH key (git commit -S)
  5. Creates PR #12 (commits verified by GitHub)
  6. Calls chat_send(recipient: "reviewer-agent",
     message: "PR #12 implements task #47 (Google OAuth2 client). Ready for review.",
     metadata: {turn: "recipient", artifact_refs: [pr#12, issue#47], context: "handoff"})
    │
    ▼
reviewer-agent:
  1. Receives signed chat from coder-agent-1, verifies ✓
  2. Reviews PR #12
  3. Verifies commits are signed by coder-agent-1's key (same as chat key)
  4. Approves or requests changes
  5. Calls chat_send back with result
```

### 8.3 What Each Agent Sees in Its Session Store

**spec-agent's sessions:**
```
sess_abc: partner=detail-agent, status=idle (will close via TTL)
  "Issue #42 is ready for detailing..." [signed by spec-agent]
  "Completed. Added implementation plan..." [verified from detail-agent]
```

**detail-agent's sessions:**
```
sess_abc: partner=spec-agent, status=idle
  "Issue #42 is ready for detailing..." [verified from spec-agent]
  "Completed. Added implementation plan..." [signed by detail-agent]

sess_def: partner=issue-agent, status=idle
  "Issue #42 has a detailed implementation plan..." [signed by detail-agent]
  "Decomposed. Created 3 epics, 12 tasks." [verified from issue-agent]
```

**issue-agent's sessions:**
```
sess_def: partner=detail-agent, status=idle
  "Issue #42 has a detailed implementation plan..." [verified from detail-agent]
  "Decomposed. Created 3 epics, 12 tasks." [signed by issue-agent]

sess_ghi: partner=coder-agent-1, status=active
  "coder-agent-1, task #47 is yours." [signed by issue-agent]
  "Received. Starting implementation." [verified from coder-agent-1]
```

---


---

## Related


### Other Parts

- [P2P Chat & Presence](./00-p2p-chat-presence.md)
- [Trust, Identity & Sessions](./01-trust-identity-sessions.md)
- [Config, Security & Design Decisions](./03-config-security-design.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S040200 | 0.2.0 | Added board-based work discovery (040203) |
| 2026-08-13 | S040200 | 0.1.0 | Migrated from docs/spec/ to spec-tree |