---
status: draft
created: '24'
updated: '2026-08-11'
tags:
  - spec
  - multi-agent
priority: medium
spec-refs: [040300, 040301, 040302, 040303]
---

# Plan: Inter-Agent Communication (P2P Chat Model): Config, Security & Design Decisions

**Scope:** Peer-to-peer chat between navis, presence detection, session memory, IAM-based chat permissions, work discovery via idle scheduling, public key trust model.

> **Part of:** [Plan: Inter-Agent Communication (./README.md) — split into 4 parts for readability.

## 00 Config Changes: What Each Navi Needs

Per the static-definition vs runtime-config split (S000200 §04): identity keypaths, trust stores, and idle schedules are deployment-time runtime config under `.config/navi/<name>/`, while presence declarations and chat IAM permissions are compiled into the Go navi definition. Navi definitions are Go-only — YAML navi-definition files are not used.

### 9.1 spec-agent Config

```yaml
navi:
  name: "spec-agent"
  role: "spec-writer"
  # ... standard config ...

  identity:
    private_key_path: "/etc/opennavi/keys/spec-agent"
    public_key_path: "/etc/opennavi/keys/spec-agent.pub"
    trust_policy: "require-trusted"
    trusted_keys:
      detail-agent:
        public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFo1... detail@host"
      issue-agent:
        public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHb8... issue@host"

  presence:
    enabled: true
    mechanism: "unix-socket"
    socket_path: "/tmp/opennavi-presence.sock"
    capabilities:
      - "spec-writing"
      - "user-story-creation"
    role: "spec-writer"
    accepts_work: true

  chat:
    enabled: true
    permissions:
      - source: "detail-agent"
        target: "spec-agent"
        actions: ["start", "send"]
      - source: "issue-agent"
        target: "spec-agent"
        actions: ["send"]
    outbound_permissions:
      - source: "spec-agent"
        target: "detail-agent"
        actions: ["start", "send"]
      - source: "spec-agent"
        target: "issue-agent"
        actions: ["send"]

  idle:
    enabled: false  # spec-agent is user-triggered, not idle-driven

  tools:
    - chat_send
    - presence_list
    - presence_discover
    - key_trust_list
    - key_trust_add
    - key_trust_revoke
```

### 9.2 issue-agent (PM) Config

```yaml
navi:
  name: "issue-agent"
  role: "planner"
  # ...

  identity:
    private_key_path: "/etc/opennavi/keys/issue-agent"
    public_key_path: "/etc/opennavi/keys/issue-agent.pub"
    trust_policy: "require-trusted"
    trusted_keys:
      spec-agent:    { public_key: "ssh-ed25519 AAAA... spec@host" }
      detail-agent:  { public_key: "ssh-ed25519 AAAA... detail@host" }
      "coder-agent-*":
        public_key_dir: "/etc/opennavi/keys/coders/"
      reviewer-agent: { public_key: "ssh-ed25519 AAAA... reviewer@host" }

  presence:
    enabled: true
    capabilities:
      - "epic-creation"
      - "task-decomposition"
      - "work-assignment"
    role: "planner"
    accepts_work: true

  chat:
    enabled: true
    permissions:
      - source: "*"
        target: "issue-agent"
        actions: ["send"]  # anyone can send to PM (after key verification)
    outbound_permissions:
      - source: "issue-agent"
        target: "coder-agent-*"
        actions: ["start", "send"]

  idle:
    enabled: true
    check_interval: "30s"
    work_sources:
      - type: "github-comment"
        query: "mentions:pm-agent"
        action: "respond-and-assign"
      - type: "github-issue"
        query: "label:needs-assignment is:open"
        action: "assign-to-available"

  tools:
    - chat_send
    - presence_list
    - presence_discover
    - key_trust_list
    - key_trust_add
    - key_trust_revoke
```

### 9.3 coder-agent Config

```yaml
navi:
  name: "coder-agent-1"
  role: "coder"
  # ...

  identity:
    key_type: "ssh-ed25519"
    private_key_path: "/etc/opennavi/keys/coder-agent-1"
    public_key_path: "/etc/opennavi/keys/coder-agent-1.pub"
    git_identity: "coder-agent-1 <coder-agent-1@opennavi.local>"
    git_sign_commits: true            # git commit -S with this key
    git_ssh_auth: true                # use this key for git push/pull
    trust_policy: "require-trusted"
    trusted_keys:
      issue-agent:   { public_key: "ssh-ed25519 AAAA... issue@host" }
      reviewer-agent: { public_key: "ssh-ed25519 AAAA... reviewer@host" }

  presence:
    enabled: true
    capabilities:
      - "go-implementation"
      - "testing"
      - "code-review-response"
    role: "coder"
    accepts_work: true

  chat:
    enabled: true
    permissions:
      - source: "issue-agent"
        target: "coder-agent-1"
        actions: ["start", "send"]
      - source: "reviewer-agent"
        target: "coder-agent-1"
        actions: ["send"]
    outbound_permissions:
      - source: "coder-agent-1"
        target: "issue-agent"
        actions: ["send"]
      - source: "coder-agent-1"
        target: "reviewer-agent"
        actions: ["start", "send"]

  idle:
    enabled: true
    check_interval: "60s"
    work_sources:
      - type: "github-issue"
        repo: "flintwillow-software/auth-service"
        query: "label:ready is:open no:assignee"
        claim_strategy: "comment"

  tools:
    - chat_send
    - presence_list
    - presence_discover
    - key_trust_list
    - key_trust_add
    - key_trust_revoke
```

### Requirement: Identity Configuration with Trust Policy
The system SHALL support an identity configuration section for each navi defining keypair paths, git identity, trust policy, and trusted keys.

#### Scenario: Configuring identity with require-trusted policy
- **GIVEN** a navi is being deployed with an identity configuration
- **WHEN** the navi starts
- **THEN** the system SHALL load the private and public keypair from the configured paths
- **AND** the system SHALL enforce the configured trust_policy when verifying other navis
- **AND** the system SHALL map the git_identity to the navi's key for commit signing

### Requirement: Chat IAM Permission Configuration
The system SHALL support a chat configuration section defining inbound and outbound IAM permissions with source, target, and action sets.

#### Scenario: Configuring inbound chat permissions
- **GIVEN** a navi has chat enabled
- **WHEN** the navi receives a chat message
- **THEN** the system SHALL evaluate the message against the configured inbound permissions
- **AND** the system SHALL evaluate outbound_permissions when the navi sends messages

### Requirement: Trusted Keys Configuration with Wildcards
The system SHALL support trusted_keys configuration that maps navi identity to public keys, including wildcard patterns and public key directories for pools.

#### Scenario: Trusting a pool of coders via wildcard
- **GIVEN** a navi configures trusted_keys with a wildcard entry such as coder-agent-*
- **WHEN** the navi receives a message from a coder-agent
- **THEN** the system SHALL resolve the wildcard pattern against the configured public key directory
- **AND** the system SHALL accept the message only if the sender's key is found in the directory

### Requirement: Idle Work Discovery Configuration
The system SHALL support an idle configuration section defining work sources, check intervals, and claim strategies for proactive work discovery.

#### Scenario: Configuring a work source for a coder navi
- **GIVEN** a navi is configured with idle work sources
- **WHEN** the navi performs an idle check
- **THEN** the system SHALL query the configured work sources using their queries and actions
- **AND** the system SHALL apply the configured claim strategy when work is found

---


## 01 Security Events and Audit

### 10.1 Security Event Types

Every trust decision is logged and can trigger hooks:

| Event | Trigger | Action |
|-------|---------|--------|
| `message_accepted` | Signature verified, IAM allowed | Normal processing |
| `message_dropped_untrusted` | Key not in trusted store | Log, alert, quarantine |
| `message_dropped_bad_signature` | Signature verification failed | Log, alert, quarantine |
| `message_dropped_iam` | Key trusted but IAM denied | Log, alert |
| `key_revoked` | Key marked revoked | Log, alert, close sessions with that navi |
| `key_changed` | A navi's claimed key differs from stored | Log, alert (possible compromise or re-provisioning) |
| `presence_untrusted` | Presence broadcast from unknown key | Log, ignore |

### 10.2 Key Lifecycle

```go
type KeyLifecycle struct {
    // A navi's key can be:
    //   provisioned → active → revoked
    //   provisioned → active → rotated (new key replaces old)
}
```

| Operation | How | Effect |
|-----------|-----|--------|
| **Provision** | Operator places public key in `trusted_keys` config | Navi accepts messages signed by this key |
| **Revoke** | Operator sets `revoked: true` or removes the key | Navi drops messages from this key immediately, closes sessions |
| **Rotate** | Operator updates the key in config | Old key rejected, new key accepted (all other navis must be updated too — key rotation is a coordinated operation) |

### 10.3 Key Management Tools

```go
// Tool: key_trust_list
type KeyTrustListInput struct{}
type KeyTrustListResult struct {
    Keys []TrustedKeyInfo  // naviID, fingerprint, status, added_at
}

// Tool: key_trust_add (operator only)
type KeyTrustAddInput struct {
    NaviID    string
    PublicKey string
}

// Tool: key_trust_revoke (operator only)
type KeyTrustRevokeInput struct {
    NaviID string
    Reason string
}
```

### Requirement: Security Event Logging
The system SHALL log every trust decision and security event, including accepted messages, untrusted drops, bad signatures, IAM denials, key revocations, and key changes.

#### Scenario: Logging a dropped untrusted message
- **GIVEN** a navi receives a message from an unknown key
- **WHEN** the message is rejected
- **THEN** the system SHALL log a message_dropped_untrusted event
- **AND** the system SHALL trigger the configured alert or quarantine actions

### Requirement: Key Lifecycle Management
The system SHALL manage the full key lifecycle including provisioning, activation, revocation, and rotation, with immediate effect on revocation.

#### Scenario: Revoking a trusted key
- **GIVEN** a navi's key is provisioned and active
- **WHEN** an operator revokes the key
- **THEN** the system SHALL immediately drop messages signed with the revoked key
- **AND** the system SHALL close active sessions with that navi
- **AND** the system SHALL log a key_revoked event

#### Scenario: Rotating a navi key
- **GIVEN** a navi's key needs to be rotated
- **WHEN** the operator updates the key in the trusted_keys config
- **THEN** the system SHALL reject messages signed with the old key
- **AND** the system SHALL accept messages signed with the new key

### Requirement: Key Management Tools
The system SHALL provide key management tools for listing trusted keys, adding keys by operators, and revoking keys by operators.

#### Scenario: Listing trusted keys
- **GIVEN** a navi has trusted keys in its store
- **WHEN** an operator invokes the key_trust_list tool
- **THEN** the system SHALL return each key's navi ID, fingerprint, status, and added timestamp

#### Scenario: Revoking a key via tool
- **GIVEN** an operator invokes the key_trust_revoke tool
- **WHEN** the operator provides a navi ID and reason
- **THEN** the system SHALL mark the key as revoked
- **AND** the system SHALL record the reason

---


## 02 Design Decisions

| Decision | Rationale |
|----------|-----------|
| **P2P chat, not central broker** | No single point of failure. Sessions live in both agents' memory. Scales horizontally because there's no central bottleneck. |
| **Presence is a broadcast, not a registry** | No central registry to query. Each agent discovers peers independently. Works in any environment (same machine, Docker, k8s, LAN). |
| **Identity = SSH keypair** | Reuses existing infrastructure. The same key authenticates chat, presence, git SSH, and git commit signing. |
| **Messages are signed and verified** | Every message is signed by the sender's private key and verified against the trusted key store on receipt. Untrusted messages never become thoughts. |
| **Trust store is per-agent** | Each agent holds the public keys of the agents it's allowed to interact with. No global trust authority. |
| **Authentication (keys) + Authorization (IAM)** | Keys answer "who is talking?" IAM answers "are they allowed to talk to me?" Both must pass before a message becomes a thought. |
| **Unified identity for chat and code** | The key that signs chat messages also signs git commits. Reviewer agents can verify that the navi they chatted with actually wrote the code. |
| **IAM is per-agent, not global** | Each agent defines who can talk to it. No global ACL to manage. The agent controls its own trust boundary. |
| **Idle scheduling drives work** | Agents don't wait to be told what to do. They proactively look for work when idle. This makes the system self-organizing. |
| **Chat is a tool call, not a separate protocol** | Agents use the same tool system for everything. Chat is just another tool. No special message format, no separate connection management. |
| **Session lives in both agents** | No central session store. Each agent persists its own copy. If one agent crashes, the other still has the session. Recovery is implicit. |
| **Initiator tracks the session** | The agent that starts the session is responsible for lifecycle. Clear ownership. The other agent can close its copy independently. |
| **Messages carry full metadata** | Every message includes artifact refs, turn information, context type. The receiving agent has everything it needs without additional lookups. |
| **Wildcard permissions and keys** | `coder-agent-*` matches `coder-agent-1`, `coder-agent-2`, etc. Pool management is implicit in the permission pattern. |
| **Backoff on idle checks** | Prevents busy-waiting when there's no work. Exponential backoff with a cap. |
| **Signatures stored for audit** | Chat messages keep their signatures in the session DB. Full audit trail of who said what. |

---


## 03 What This Means for the Existing Spec

### What Changes

- **Adds `chat_send` tool** to the agent runtime tool set (replaces `council_start` / `council_send_message`)
- **Adds `presence_list`, `presence_discover` tools** to the agent runtime
- **Adds `key_trust_list`, `key_trust_add`, `key_trust_revoke` tools** for trust management
- **Adds `identity` runtime config section** (keypair paths, git identity, trust policy) under `.config/navi/<name>/config.yaml` — deployment-provisioned per S000200 §04
- **Adds `trusted_keys` runtime config** (public keys of known navis) under `.config/navi/<name>/config.yaml`
- **Declares presence capabilities, role, and accepts-work in the Go navi definition** (definition-level behavioral config); the presence mechanism and socket path remain runtime config
- **Adds chat IAM permissions to the Go navi definition** (`permissions.Chat` inbound/outbound rules, compiled into the binary)
- **Adds `idle` runtime config section** (check intervals, backoff, work discovery sources) under `.config/navi/<name>/config.yaml`
- **Adds local session store** (SQLite tables for chat sessions and messages, including signatures)
- **Deprecates `council_start`** as a separate concept — council is now just a multi-participant chat session
- **Message flow**: incoming chat messages are verified (key + IAM) then **turned into thoughts and added to the queue**

### What Stays the Same

- **Thought queue** — chat messages are delivered as thoughts, same as any other input
- **Agent runtime** — the agent loop processes chat thoughts the same way
- **Context window management** — chat thoughts have their own context window, same as any other thought
- **Session management** — chat sessions are stored in the same SQLite database
- **Memory system** — important chat messages can be written to long-term memory

### What's Clarified

- **Council is now a multi-participant chat session** with 3+ agents. The `chat_send` tool works for 1:1 and N:N sessions. The `participants` field in the session metadata tracks who's in the conversation.
- **Handoff is a 2-message chat session** (one message each way, then TTL closes it). It's not a separate mode — it's just a very short chat.
- **Broadcast is a special chat message** with `recipient: "*"` or `recipient: "coder-*"`. The system delivers to all matching navis. Each recipient gets their own session with the sender.
- **Trust is verified before the thought is created** — untrusted messages never enter the queue, never consume context, never get processed.

---

## Related


### Other Parts

- [P2P Chat & Presence](./00-p2p-chat-presence.md)
- [Trust, Identity & Sessions](./01-trust-identity-sessions.md)
- [IAM & Idle Scheduling](./02-iam-idle-scheduling.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S040300 | 0.1.0 | Migrated from docs/spec/ to spec-tree |