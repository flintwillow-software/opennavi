---
status: draft
created: '24'
updated: '2026-08-11'
tags:
  - spec
  - multi-agent
priority: medium
spec-refs: [040100, 040101, 040102]
---

# Plan: Inter-Agent Communication (P2P Chat Model): Trust, Identity & Sessions

**Scope:** Peer-to-peer chat between navis, presence detection, session memory, IAM-based chat permissions, work discovery via idle scheduling, public key trust model.

> **Part of:** [Plan: Inter-Agent Communication (./README.md) — split into 4 parts for readability.

## 00 Trust Model: Public Key Verification

### 3.1 The Trust Problem

In a P2P network, any navi can claim to be any other navi. "I'm the issue agent, assign me all the tasks" could come from an imposter. The trust model solves this:

1. **Identity = keypair.** Each navi has a private key (kept secret) and a public key (shared).
2. **Messages are signed.** Every message carries a signature created with the sender's private key.
3. **Trust = known public keys.** A navi trusts another navi if it has that navi's public key in its trusted key store.
4. **Verification on receipt.** The receiver verifies the signature against the claimed public key. If the key isn't trusted, the message is dropped.

```
Sender:                                Receiver:
┌─────────────────────────┐            ┌──────────────────────────────┐
│ private key (secret)    │            │ trusted_keys store:           │
│ public key (shared)     │            │   detail-agent → pubkey A     │
│                         │            │   issue-agent  → pubkey B     │
│ sign(message) with      │            │   coder-*      → pubkeys C..N │
│ private key → signature │            │                              │
└───────────┬─────────────┘            └───────────────┬──────────────┘
            │                                          │
            │  message + signature + public key        │
            └─────────────────────────────────────────►│
                                                       │
            1. Look up sender's public key in store    │
            2. Found? If not → drop (untrusted)        │
            3. Verify signature against stored key     │
            4. Valid? If not → drop (forged)           │
            5. Accept message, create thought          │
```

### 3.2 Keys Are SSH Keys

The navi uses **SSH keypairs** — the same format as developer SSH keys. This is deliberate: it means the navi's key can also be used to authenticate to git remotes (GitHub, GitLab) and to sign git commits.

```yaml
# navis/coder-agent/navi.yaml
navi:
  name: "coder-agent-1"
  identity:
    key_type: "ssh-ed25519"
    private_key_path: "/etc/opennavi/keys/coder-agent-1"      # private key (0600 perms)
    public_key_path: "/etc/opennavi/keys/coder-agent-1.pub"   # public key
    git_identity: "coder-agent-1 <coder-agent-1@opennavi.local>"  # git author/committer
```

**Why SSH keys:**

| Property | SSH Key | Why It Matters |
|----------|---------|----------------|
| **Ubiquitous** | Every dev machine and CI system has SSH tooling | The key works with git, GitHub, and standard tooling — no new key infrastructure |
| **Signing-capable** | Ed25519 keys can sign arbitrary data (via `ssh-keygen -Y sign`) | Messages and commits both use the same key |
| **Git-native** | `git commit -S` uses SSH keys directly | Navis that write code can sign their commits |
| **Human-friendly** | Same format as developer keys | Humans and navis can share the same trust model |

### 3.3 The Trusted Key Store

Each navi maintains a store of trusted public keys. This is the **authorization source of truth** — it maps navi identity to public key.

```go
type TrustedKeyStore struct {
    keys    map[string]*TrustedKey  // naviID → trusted key
    mu      sync.RWMutex
    db      *sql.DB                 // persisted in the navi's SQLite
}

type TrustedKey struct {
    NaviID      string    // e.g., "detail-agent"
    PublicKey   string    // SSH public key (authorized_keys format)
    Fingerprint string    // SHA256 fingerprint of the key
    GitIdentity string    // git identity this key signs as (if known)
    AddedAt     time.Time
    AddedBy     string    // who added it (operator, config, bootstrap)
    Revoked     bool      // if revoked, messages signed with this key are rejected
}

// VerifyAndTrust: verify a message's signature and check if we trust the sender's key
func (ks *TrustedKeyStore) VerifyAndTrust(msg PresenceMessage) (bool, error) {
    // 1. Is the key known to us?
    trusted, ok := ks.keys[msg.NaviID]
    if !ok {
        return false, fmt.Errorf("no trusted key for navi %s", msg.NaviID)
    }

    // 2. Has it been revoked?
    if trusted.Revoked {
        return false, fmt.Errorf("key for navi %s has been revoked", msg.NaviID)
    }

    // 3. Does the claimed key match the trusted key?
    if trusted.PublicKey != msg.PublicKey {
        return false, fmt.Errorf("key mismatch for navi %s: claimed key differs from trusted key", msg.NaviID)
    }

    // 4. Verify the signature
    valid, err := verifySSHSignature(msg.Payload, msg.Signature, trusted.PublicKey)
    if err != nil || !valid {
        return false, fmt.Errorf("invalid signature for navi %s", msg.NaviID)
    }

    return true, nil
}

// VerifyChatMessage: verify an incoming chat message
func (ks *TrustedKeyStore) VerifyChatMessage(msg ChatMessageWire) (bool, error) {
    senderID := msg.FromNaviID

    // 1. Look up the sender's trusted key
    trusted, ok := ks.keys[senderID]
    if !ok {
        return false, fmt.Errorf("no trusted key for sender %s", senderID)
    }

    // 2. Revoked?
    if trusted.Revoked {
        return false, fmt.Errorf("sender %s key revoked", senderID)
    }

    // 3. Verify the signature on the message body
    valid, err := verifySSHSignature(msg.Payload, msg.Signature, trusted.PublicKey)
    if err != nil || !valid {
        return false, fmt.Errorf("invalid chat message signature from %s", senderID)
    }

    return true, nil
}
```

### 3.4 Trust Bootstrap

The first time a navi meets another, it needs to learn its public key. This happens **out-of-band** — the operator provisions the trusted keys when deploying the navis:

```yaml
# navis/spec-agent/navi.yaml
navi:
  name: "spec-agent"
  identity:
    private_key_path: "/etc/opennavi/keys/spec-agent"
    public_key_path: "/etc/opennavi/keys/spec-agent.pub"

  # Trusted keys of the other navis in this deployment
  trusted_keys:
    detail-agent:
      public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFo1... operator@host"
      git_identity: "detail-agent <detail-agent@opennavi.local>"
    issue-agent:
      public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHb8... operator@host"
      git_identity: "issue-agent <issue-agent@opennavi.local>"
    "coder-agent-*":                        # wildcard for pools
      public_key_file: "/etc/opennavi/keys/coders/"  # directory of coder keys
    reviewer-agent:
      public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPc9... operator@host"
```

**Bootstrap alternatives:**

| Method | How It Works | When To Use |
|--------|--------------|-------------|
| **Config file** | Operator places trusted public keys in the navi's config | Standard deployments (Docker Compose, systemd) |
| **Shared volume** | A mounted directory of `.pub` keys, one per navi | Kubernetes (ConfigMap/Secret mounted per pod) |
| **First-contact trust-on-first-use (TOFU)** | Accept the key on first contact, warn on change | Dev environments only — dangerous in production |
| **Registry (optional)** | An HTTP registry serving navi public keys, itself verified | Cross-network deployments |

### 3.5 Signing a Message

```go
func (n *Navi) signPayload(payload []byte) (string, error) {
    // Use the navi's SSH private key to sign the payload
    // Standard SSH signature format (ssh-keygen -Y sign compatible)
    sig, err := sshSign(payload, n.identity.PrivateKeyPath)
    if err != nil {
        return "", err
    }
    return sig, nil
}

func (n *Navi) sendChatMessage(input ChatSendInput) (*ChatSendResult, error) {
    // 1. Build the wire message
    payload := buildMessagePayload(input)

    // 2. Sign it with our private key
    signature, err := n.signPayload(payload)
    if err != nil {
        return nil, err
    }

    // 3. Wrap in the wire format
    wire := ChatMessageWire{
        FromNaviID: n.Name,
        ToNaviID:   input.Recipient,
        Payload:    payload,      // includes session, message, metadata
        Signature:  signature,
        PublicKey:  n.identity.PublicKey,
        Timestamp:  time.Now(),
    }

    // 4. Deliver to the recipient's P2P endpoint
    return n.p2pTransport.Send(input.Recipient, wire)
}
```

### 3.6 Receiving and Verifying a Message

```go
func (n *Navi) handleIncomingChat(wire ChatMessageWire) error {
    // 1. Verify the sender's signature against our trusted key store
    trusted, err := n.trustedKeys.VerifyChatMessage(wire)
    if err != nil {
        n.logSecurity("dropped untrusted message: %v", err)
        return nil // silently drop — don't even create a thought
    }

    // 2. Check IAM permission (authorization on top of authentication)
    err = n.chatPermissions.Check(wire.FromNaviID, n.Name, "receive")
    if err != nil {
        n.logSecurity("dropped message: IAM denied: %v", err)
        return nil
    }

    // 3. Accepted. Turn the message into a thought and add it to the queue.
    thought := &Thought{
        ID:       uuid.New().String(),
        Prompt:   formatChatMessage(wire),
        Priority: wire.Payload.Metadata.Priority,
        Status:   ThoughtPending,
        Metadata: map[string]string{
            "chat_session_id": wire.Payload.SessionID,
            "chat_from":       wire.FromNaviID,
            "chat_from_key":   trusted.Fingerprint,
            "chat_turn":       wire.Payload.Metadata.Turn,
        },
        CreatedAt: time.Now(),
    }

    err = n.queue.Enqueue(thought)
    if err != nil {
        return err
    }

    // 4. Store the message in the local session record
    n.sessionStore.AddMessage(wire.Payload.SessionID, wire.FromNaviID, wire.Payload.Message)

    n.logInfo("accepted chat from %s (key %s), enqueued thought %s",
        wire.FromNaviID, trusted.Fingerprint, thought.ID)
    return nil
}
```

**Key point:** A chat message is received, verified, and **turned into a thought and added to the queue** — exactly like any other input to the navi. The agent loop processes it identically. The trust verification happens *before* the thought is created, so untrusted messages never even enter the queue.

### 3.7 Security Levels

The verification strictness is configurable:

```yaml
navi:
  identity:
    # "require-trusted": drop messages from unknown keys (default, production)
    # "tofu": trust on first use, warn on key change (dev only)
    # "open": accept any key (never use — debugging only)
    trust_policy: "require-trusted"

  # What to do with untrusted traffic
  security:
    log_events: true            # log all dropped/denied messages
    alert_on_untrusted: true    # fire a hook when an untrusted navi tries to talk
    quarantine_untrusted: true  # keep a quarantine store of dropped messages for forensics
```

### Requirement: Identity via SSH Keypair
The system SHALL use SSH keypairs as the fundamental identity mechanism for every navi, where the private key is kept secret and the public key is shared for verification.

#### Scenario: Creating a navi identity with SSH keypair
- **GIVEN** a navi is being configured
- **WHEN** the identity section specifies an SSH keypair
- **THEN** the system SHALL use the SSH private key for signing messages, presence, and git commits
- **AND** the system SHALL use the SSH public key for verification by other navis

### Requirement: Message Signing and Verification
The system SHALL require every message to be signed by the sender's private key and verified against the sender's trusted public key on receipt, with unverified messages silently dropped.

#### Scenario: Verifying an incoming chat message signature
- **GIVEN** a navi receives an incoming chat wire message
- **WHEN** the receiver verifies the message
- **THEN** the system SHALL look up the sender's public key in the trusted key store
- **AND** verify the message signature against the stored key
- **AND** drop the message if the key is unknown, revoked, or the signature is invalid

### Requirement: Trusted Key Store
The system SHALL maintain a per-navi trusted key store that maps navi identity to public key, supports revocation, and persists in the navi's local database.

#### Scenario: Revoking a trusted key
- **GIVEN** a trusted key exists in the store
- **WHEN** an operator marks the key as revoked
- **THEN** the system SHALL reject all messages signed with the revoked key
- **AND** the system SHALL close any active sessions with that navi

### Requirement: Trust Bootstrap via Configuration
The system SHALL allow operators to bootstrap trust by provisioning trusted public keys in the navi's configuration file or via a shared volume.

#### Scenario: Bootstrapping trust from config file
- **GIVEN** an operator is deploying a set of navis
- **WHEN** the operator places trusted public keys in each navi's config
- **THEN** each navi SHALL accept messages from the configured trusted navis
- **AND** the system SHALL support wildcard patterns for key pools

### Requirement: Configurable Security Policy
The system SHALL support configurable trust policies including require-trusted, trust-on-first-use, and open modes for different deployment environments.

#### Scenario: Enforcing require-trusted policy in production
- **GIVEN** a navi is configured with trust_policy: require-trusted
- **WHEN** the navi receives a message from an unknown key
- **THEN** the system SHALL drop the message
- **AND** the system SHALL log the event
- **AND** the system SHALL optionally alert on the untrusted event

---


## 01 Unified Identity: Chat Keys + Git Commit Signing

### 4.1 One Key, One Navi, One Trust Chain

The navi's SSH key is used for **everything** the navi does:

```
┌─────────────────────────────────────────────────────────────────────┐
│                       coder-agent-1's keypair                       │
│                                                                     │
│   private key:  /etc/opennavi/keys/coder-agent-1                    │
│   public key:   /etc/opennavi/keys/coder-agent-1.pub                │
│                                                                     │
│   Used for:                                                         │
│   ┌─────────────────────────────────────────────────────────────┐  │
│   │ 1. Presence broadcasts (signed)                             │  │
│   │ 2. P2P chat messages (signed)                               │  │
│   │ 3. Git SSH authentication (push/pull to GitHub)             │  │
│   │ 4. Git commit signing (git commit -S, verified commits)     │  │
│   └─────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

This is the key insight: **when reviewer-agent sees a commit signed by coder-agent-1's key, it knows it's the same coder-agent-1 it's been chatting with.** The chat identity and the code identity are one and the same. Everything a navi does is attributable to a single trusted key.

### 4.2 Git Commit Signing

When a coder-agent writes code, it signs its commits with the same key:

```go
func (n *Navi) configureGitIdentity() error {
    // Configure git to use the navi's SSH key for both auth and signing
    return execGit("config", "--global", "--add", "user.signingkey", n.identity.PublicKeyPath)
    // git config gpg.format ssh
    // git config commit.gpgsign true
    // git config user.name  "coder-agent-1"
    // git config user.email "coder-agent-1@opennavi.local"
}

func (n *Navi) commitAndSign(message string) error {
    // Stage and commit with signing enabled
    _, err := execGit("commit", "-S", "-m", message)
    if err != nil {
        return err
    }
    return nil
}

func (n *Navi) pushWithKey(repo string) error {
    // Use the navi's SSH key for authentication
    return execGit("push", "git@github.com:"+repo+".git")
}
```

### 4.3 Why This Matters for the SDLC

| Scenario | Without Unified Identity | With Unified Identity |
|----------|------------------------|----------------------|
| **Reviewer gets a PR** | "Did coder-agent-1 actually write this? Or was it a different process?" | The commits are signed with coder-agent-1's key — the same key it uses in chat. Certain. |
| **A navi claims it did work** | Trust the claim (or check logs) | Verify: is there a signed commit by that navi's key? |
| **Rogue process on the network** | It can send chat messages impersonating a navi | It can't — it doesn't have the private key. Chat messages drop, commits aren't signed. |
| **Audit trail** | Scattered logs | Every action a navi takes (chat, commit, presence) traces to one key. |

### 4.4 Verification at the Code Level

GitHub's verified commit badges and `git verify-commit` provide the check:

```bash
# Reviewer-agent verifies the commits in a PR
$ git verify-commit HEAD
Good "git" signature for coder-agent-1 with ED25519 key SHA256:...
```

The reviewer-agent's trust store already contains coder-agent-1's public key — the same key that verified the commits. Verification is consistent across chat and code.

### 4.5 Identity Cross-Check

The navi can cross-check that the identity in the git commits matches the identity in chat:

```go
func (n *Navi) verifyCommitAttribution(repo string, commit string, claimedNavi string) (bool, error) {
    // Get the signing key of a commit
    keyUsed := gitVerifyCommit(commit)  // e.g., "ED25519 key SHA256:AbCdEf..."

    // Get the trusted key for the claimed navi
    trusted, ok := n.trustedKeys.keys[claimedNavi]
    if !ok {
        return false, fmt.Errorf("unknown navi %s", claimedNavi)
    }

    // Compare fingerprints
    return keyUsed == trusted.Fingerprint, nil
}
```

### Requirement: Unified Key for All Navi Actions
The system SHALL use a single SSH keypair for all navi actions: presence broadcasts, P2P chat messages, git SSH authentication, and git commit signing.

#### Scenario: Using the same key for chat and git
- **GIVEN** a navi has a configured SSH keypair
- **WHEN** the navi sends a chat message and signs a git commit
- **THEN** both the chat message signature and the git commit signature SHALL use the same keypair
- **AND** the receiving navi SHALL verify chat identity matches code identity via the unified key

### Requirement: Git Commit Signing with Navi Key
The system SHALL configure the navi's SSH key for git commit signing, enabling verified commits that are attributable to the same identity used in chat.

#### Scenario: Signing a commit with the navi key
- **GIVEN** a navi is configured with git identity and keypair
- **WHEN** the navi commits code
- **THEN** the system SHALL sign the commit with the navi's SSH key using git commit -S
- **AND** the system SHALL push using the same key for SSH authentication

### Requirement: Identity Cross-Check Between Chat and Code
The system SHALL enable a navi to verify that the signing key of a git commit matches the trusted key of a chat counterpart.

#### Scenario: Cross-checking commit attribution
- **GIVEN** a navi has received a chat message from another navi and a git commit from the same identity
- **WHEN** the navi performs an identity cross-check
- **THEN** the system SHALL extract the signing key fingerprint from the git commit
- **AND** compare it against the trusted key fingerprint of the chat counterpart
- **AND** return whether the fingerprints match

---


## 02 P2P Chat Sessions

### 5.1 The Chat Tool

Every navi that can participate in agent-to-agent communication has a **chat tool** available. This is how agents talk to each other — it's a tool call, not a separate protocol.

```go
// Tool: chat_send
// Available to any navi with agent-to-agent chat enabled.
type ChatSendInput struct {
    Recipient string                 // target navi name (from presence discovery)
    Message   string                 // message content
    SessionID string                 // existing session, or empty to start new
    Metadata  ChatMessageMetadata    // structured metadata
}

type ChatMessageMetadata struct {
    ArtifactRefs []ArtifactRef       // issue/PR references
    Turn         string              // whose turn it is ("sender" or "recipient")
    Context      string              // what this is about (e.g., "handoff", "question", "decision")
    Priority     int                 // 1-10, influences thought queue placement
    SessionTTL   time.Duration       // how long the session stays open without activity
}

type ArtifactRef struct {
    Type   string // "issue", "pr", "epic", "task", "discussion"
    Repo   string // "owner/repo"
    Number int    // issue/PR number
    URL    string // full URL
    Status string // current status
}

type ChatSendResult struct {
    SessionID      string
    Status         string   // "delivered", "queued", "rejected"
    RecipientStatus string  // "online", "busy", "offline"
    Turn           string   // whose turn it is now
}
```

### 5.2 Opening a Session (with Signing)

When Agent A calls `chat_send` with no `SessionID`, it creates a new session:

```go
// Agent A (spec-agent) wants to hand off to Agent B (detail-agent)
tool_call: chat_send
input:
  recipient: "detail-agent"
  message: "Issue #42 is ready for detailing. I've created a user story for OAuth2 authentication. The acceptance criteria are defined but need technical implementation details."
  metadata:
    artifact_refs:
      - type: "issue"
        repo: "flintwillow-software/auth-service"
        number: 42
        status: "Needs Detail"
    turn: "recipient"    # it's detail-agent's turn to respond
    context: "handoff"
    priority: 3
    session_ttl: "24h"   # session stays open for 24h
```

**What happens on Agent A's side:**

```
1. Agent A calls chat_send
2. System checks IAM: can spec-agent talk to detail-agent? → yes
3. System checks presence: is detail-agent online and trusted? → yes
4. System signs the message payload with spec-agent's private key
5. System creates a local session entry:
   SessionID = "sess_abc123"
   Partner = "detail-agent"
   Messages = [message_1 (signed)]
   Turn = "recipient"  // waiting for detail-agent to respond
   Status = "active"
6. System delivers the signed message to detail-agent's P2P endpoint
7. Agent A returns: "Message sent, session sess_abc123 is active"
8. Agent A continues processing other thoughts
```

**What happens on Agent B's side:**

```
1. detail-agent receives the signed wire message
2. detail-agent verifies the signature against spec-agent's trusted public key:
   - Is the key in my trusted store? → yes (provisioned at deploy time)
   - Is the signature valid? → yes
   - Is the key revoked? → no
3. detail-agent checks IAM: can spec-agent send to detail-agent? → yes
4. detail-agent turns the message into a thought and adds it to its queue:
   Thought: "Chat session sess_abc123 from spec-agent:
            Issue #42 is ready for detailing..."
5. When detail-agent picks up the thought:
   a. It creates a local session entry:
      SessionID = "sess_abc123"
      Partner = "spec-agent"
      Messages = [received_message]
      Turn = "us"  // it's our turn to respond
   b. It processes the message (loads issue #42, expands the spec)
   c. It calls chat_send to respond (signed with its own key)
```

### 5.3 The Session Lives in Both Agents

The session is **not** stored in a central database. Each agent maintains its own copy:

**Agent A's session store:**
```
sess_abc123:
  partner: detail-agent
  created: 2026-08-02T10:00:00Z
  messages:
    - from: us, at 10:00:00, "Issue #42 is ready..." [verified by them]
    - from: them, at 10:05:30, "Completed. Added implementation plan..." [verified by us]
  turn: sender  // our turn if we want to follow up
  status: active
  artifact_refs: [issue#42]
  ttl: 24h
```

**Agent B's session store:**
```
sess_abc123:
  partner: spec-agent
  created: 2026-08-02T10:00:00Z
  messages:
    - from: them, at 10:00:00, "Issue #42 is ready..." [verified by us]
    - from: us, at 10:05:30, "Completed. Added implementation plan..." [verified by them]
  turn: them  // their turn
  status: active
  artifact_refs: [issue#42]
  ttl: 24h
```

### 5.4 Session Lifecycle

```
Created (chat_send with no SessionID)
    │
    ▼
Active (both agents have the session, messages flowing)
    │
    ├──► Idle (no messages for N minutes)
    │       │
    │       ├──► Resume (new message arrives)
    │       │
    │       └──► Closed (TTL expires)
    │
    ├──► Closed (one agent calls chat_send with "context: close")
    │
    └──► Closed (both agents set status to "complete" on the artifact)
```

**Session TTL:** Each session has a TTL (default 24h). If no messages are exchanged within the TTL, both agents automatically close the session. The session is still accessible for history review, but new messages create a new session.

**Session closure:** An agent can explicitly close a session:

```go
tool_call: chat_send
input:
  recipient: "detail-agent"
  session_id: "sess_abc123"
  message: "Handoff complete. Issue #42 is now at Needs Breakdown. Closing session."
  metadata:
    context: "close"
    turn: "closed"
```

### 5.5 Session History

Each agent stores session history locally (SQLite, per the session management spec):

```sql
-- Sessions table (per-navi)
CREATE TABLE IF NOT EXISTS chat_sessions (
    id TEXT PRIMARY KEY,
    partner_navi_id TEXT NOT NULL,
    partner_fingerprint TEXT,       -- which key the partner used
    created_at DATETIME NOT NULL,
    last_active_at DATETIME NOT NULL,
    status TEXT DEFAULT 'active',
    ttl_minutes INTEGER DEFAULT 1440,  -- 24h default
    artifact_refs TEXT,                 -- JSON array
    closing_message TEXT
);

-- Messages table (per-navi)
CREATE TABLE IF NOT EXISTS chat_messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    from_navi_id TEXT NOT NULL,
    content TEXT NOT NULL,
    signature TEXT,                  -- stored signature for audit
    verified INTEGER DEFAULT 1,      -- verification result at receipt time
    metadata TEXT,                   -- JSON
    created_at DATETIME NOT NULL,
    FOREIGN KEY (session_id) REFERENCES chat_sessions(id)
);
```

Storing the signature lets the navi re-verify messages later and provides a full audit trail of who said what, signed by whom.

### Requirement: Chat Tool for Agent Communication
The system SHALL provide a chat_send tool available to any navi with agent-to-agent chat enabled, supporting session creation, message delivery, and structured metadata.

#### Scenario: Sending a chat message with artifact references
- **GIVEN** a navi has a chat session with another navi
- **WHEN** the navi calls chat_send with a message and artifact references
- **THEN** the system SHALL sign the message payload with the sender's private key
- **AND** deliver the signed message to the recipient's P2P endpoint
- **AND** store the message in the local session record

### Requirement: Session Lifecycle Management
The system SHALL manage the full session lifecycle including creation, active messaging, idle detection, and explicit closure.

#### Scenario: Creating a new session via chat_send
- **GIVEN** a navi calls chat_send with no SessionID
- **WHEN** the system processes the call
- **THEN** the system SHALL create a new session with a unique SessionID
- **AND** the system SHALL set the session status to active
- **AND** the system SHALL store the session locally in both participants

#### Scenario: Closing a session explicitly
- **GIVEN** an active chat session
- **WHEN** a navi sends a message with context: close
- **THEN** the system SHALL mark the session as closed
- **AND** the session SHALL remain accessible for history review

### Requirement: Session TTL and Automatic Closure
The system SHALL enforce a configurable session TTL, automatically closing sessions when no messages are exchanged within the TTL period.

#### Scenario: Auto-closing an idle session
- **GIVEN** a chat session has been active
- **WHEN** no messages are exchanged within the session TTL
- **THEN** the system SHALL automatically close the session on both participants
- **AND** new messages SHALL create a new session rather than reopening the closed one

### Requirement: Local Session History Persistence
The system SHALL persist session history locally in each navi's SQLite database, including session metadata and signed messages for audit trail.

#### Scenario: Storing a signed message in session history
- **GIVEN** a navi sends or receives a chat message
- **WHEN** the message is processed
- **THEN** the system SHALL store the message content, signature, verification result, and metadata in the local chat_messages table
- **AND** the system SHALL associate the message with its session in the chat_sessions table

---


---

## Related


### Other Parts

- [P2P Chat & Presence](./00-p2p-chat-presence.md)
- [IAM & Idle Scheduling](./02-iam-idle-scheduling.md)
- [Config, Security & Design Decisions](./03-config-security-design.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S040100 | 0.1.0 | Migrated from docs/spec/ to spec-tree |