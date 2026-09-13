---
status: draft
created: '24'
updated: '2026-08-11'
tags:
  - spec
  - multi-agent
priority: medium
spec-refs: [040000, 040001]
---

# Plan: Inter-Agent Communication (P2P Chat Model): P2P Chat & Presence

**Scope:** Peer-to-peer chat between navis, presence detection, session memory, IAM-based chat permissions, work discovery via idle scheduling, public key trust model.

> **Part of:** [Plan: Inter-Agent Communication (./README.md) — split into 4 parts for readability.

## 00 Overview: P2P Chat Model

### 1.1 How It Works

Navis communicate by opening **direct chat sessions** with each other. There is no central message broker, no gateway routing, and no single point of coordination. Each navi:

1. **Broadcasts its presence** — announces it's online, available, and signs the announcement with its key
2. **Discovers other navis** — queries for navis on the network, verifies their public keys
3. **Opens chat sessions** — initiates direct P2P conversations
4. **Exchanges signed messages** — each message carries full metadata and is signed with the sender's private key
5. **Tracks sessions locally** — session state lives in each participant's memory

```
┌─────────────────┐        ┌─────────────────┐
│  spec-agent     │        │  detail-agent    │
│                 │        │                  │
│  ┌───────────┐  │        │  ┌───────────┐   │
│  │ Session   │  │        │  │ Session   │   │
│  │ Store     │  │        │  │ Store     │   │
│  │           │  │        │  │           │   │
│  │ ID: abc   │──┐     ┌──│ ID: abc    │   │
│  │ Partner:  │  │     │  │ Partner:   │   │
│  │ detail    │  │     │  │ spec       │   │
│  │           │  │     │  │           │   │
│  │ Turn: us  │  │     │  │ Turn: them│   │
│  └───────────┘  │     │  └───────────┘   │
│                 │     │                  │
│  Messages:      │     │  Messages:       │
│  ┌───────────┐  │     │  ┌───────────┐   │
│  │ "Issue #42│  │     │  │ "Issue #42│   │
│  │  is ready"│──┼─────┼─►│  received"│   │
│  │  [signed] │  │     │  │  [signed] │   │
│  └───────────┘  │     │  └───────────┘   │
└─────────────────┘     └─────────────────┘
```

### 1.2 Key Principles

| Principle | Description |
|-----------|-------------|
| **No central broker** | Navis talk directly. No gateway, no router, no single point of failure. |
| **Session lives in both navis** | Each participant stores the session state locally. No central database. |
| **Initiator tracks the session** | The navi that opens the session is responsible for tracking its lifecycle. |
| **Messages carry full metadata** | Every message includes issue refs, status, turn information, and context. |
| **Every message is signed** | The sender signs each message with its private key; the receiver verifies it against the sender's trusted public key. |
| **IAM controls who can talk** | Permissions define which navis can initiate sessions with which others. |
| **Presence is broadcast and verified** | Navis announce themselves; others listen and verify the announcement's signature. |
| **One key, one identity** | The same keypair authenticates chat, signs presence, and signs git commits. Trust is unified. |
| **Idle scheduling drives work** | When idle, navis check for work via scheduled thoughts. |

### Requirement: Direct P2P Chat Without Central Broker
The system SHALL enable navis to communicate via direct peer-to-peer chat sessions without a central message broker, gateway, or single point of coordination.

#### Scenario: Initiating a direct P2P chat session
- **GIVEN** two navis are online and have discovered each other
- **WHEN** one navi initiates a chat session with the other
- **THEN** the session SHALL be established directly between the two navis without routing through a central broker

### Requirement: Signed Message Exchange With Full Metadata
The system SHALL require every chat message to carry full metadata and be signed by the sender's private key before transmission.

#### Scenario: Sending a signed message with metadata
- **GIVEN** a navi has an active chat session with another navi
- **WHEN** the navi sends a message
- **THEN** the message SHALL include issue references, turn information, status, and context
- **AND** the message SHALL be signed with the sender's private key

### Requirement: Local Session State Storage
The system SHALL store session state locally on each participant without a central database.

#### Scenario: Storing session state locally
- **GIVEN** two navis are engaged in a chat session
- **WHEN** either navi processes a message
- **THEN** the session state SHALL be stored locally on that navi
- **AND** the initiator SHALL be responsible for tracking the session lifecycle

---


## 01 Presence Detection

### 2.1 How Navis Find Each Other

When the P2P interface is enabled, the navi **queries for available navis on the network**. Every navi broadcasts its presence when it comes online and listens for other navis' broadcasts. Presence is a lightweight, **signed** heartbeat that says "I'm here, I'm this type of navi, and I'm available for work."

**Presence message (signed):**

```go
type PresenceMessage struct {
    NaviID      string            // unique identifier
    Name        string            // display name (e.g., "spec-agent")
    Role        string            // role (e.g., "spec-writer", "detailer", "coder")
    Version     string            // semver
    Status      PresenceStatus    // "online", "busy", "away"
    Capabilities []string         // what this navi can do
    Interfaces  []string         // gateway, cron, chat, etc.
    Endpoint    string            // how to reach this navi (address/port/pipe)
    PublicKey   string            // the navi's public key (SSH format)
    GitIdentity string            // the git identity/email this navi signs commits as
    Signature   string            // signature over the payload with this navi's private key
    Timestamp   time.Time
}

type PresenceStatus string
const (
    PresenceOnline PresenceStatus = "online"
    PresenceBusy   PresenceStatus = "busy"    // actively processing thoughts
    PresenceAway   PresenceStatus = "away"    // not accepting new work
    PresenceOffline PresenceStatus = "offline"
)
```

The presence message is signed so that a malicious actor can't impersonate a navi on the network. When a navi receives a presence broadcast, it:

1. Extracts the `PublicKey` from the message
2. Checks whether it **already knows and trusts** this public key (from its trusted key store)
3. Verifies the message signature against the claimed key
4. Only stores the navi if both checks pass

### 2.2 Presence Discovery Mechanisms

The actual transport depends on the deployment environment:

| Environment | Mechanism | How It Works |
|-------------|-----------|-------------|
| **Same machine** | Unix domain socket multicast | Navis on the same machine discover each other via a shared socket |
| **Same Docker network** | DNS-SD / mDNS | Navis broadcast via multicast DNS (e.g., `_opennavi._tcp`) |
| **Kubernetes** | Kubernetes API + annotations | Navis register themselves as services with OpenNavi annotations |
| **Same LAN** | mDNS / Avahi | Standard mDNS browsing for `_opennavi._tcp` |
| **Remote** | Registry server (optional) | Opt-in HTTP registry for cross-network discovery |

**For the SDLC use case (same machine or same Docker Compose):**

```yaml
# .config/navi/spec-agent/config.yaml
presence:
  enabled: true
  mechanism: "unix-socket"     # or "mdns" or "k8s"
  socket_path: "/tmp/opennavi-presence.sock"
  heartbeat_interval: "30s"    # how often to broadcast
  listen: true                 # listen for other navis' presence
```

### 2.3 Presence Flow (with Verification)

```
spec-agent starts:
  1. Loads its keypair (private key from config, public key derived)
  2. Broadcasts: "I'm spec-agent, role: spec-writer, online" [signed with private key]
  3. Listens for other navis' presence broadcasts

detail-agent starts:
  1. Loads its keypair
  2. Broadcasts: "I'm detail-agent, role: detailer, online" [signed]
  3. Receives spec-agent's broadcast
  4. Checks spec-agent's public key against its trusted key store:
     - Is this key known? → check trusted_keys in config
     - Is the signature valid? → verify against the claimed public key
  5. If trusted → stores: "spec-agent is online, can chat via endpoint X"
  6. If not trusted → marks as untrusted, does not store
     (or stores with a flag, depending on policy: "require_trusted_keys")

issue-agent starts:
  1. Loads its keypair
  2. Broadcasts: "I'm issue-agent, role: planner, online" [signed]
  3. Receives spec-agent and detail-agent broadcasts
  4. Verifies both keys, stores the trusted ones

All navis now know about each other — and only trust the ones they should.
```

### 2.4 Presence Store

Each navi maintains a local store of known navis:

```go
type PresenceStore struct {
    navis    map[string]*PresenceEntry  // naviID → presence
    listeners []func(PresenceEvent)     // callbacks on presence change
    mu       sync.RWMutex
}

type PresenceEntry struct {
    LastSeen   time.Time
    Message    PresenceMessage
    ChatActive bool  // currently in a chat session with this navi
    Trusted    bool  // was the public key verified against our trusted store?
}

type PresenceEvent struct {
    Type    PresenceEventType  // "online", "offline", "update", "untrusted"
    Message PresenceMessage
}

func (ps *PresenceStore) Update(msg PresenceMessage) {
    ps.mu.Lock()
    defer ps.mu.Unlock()

    existing, ok := ps.navis[msg.NaviID]

    // Verify the signature before accepting
    trusted, err := ps.keyStore.VerifyAndTrust(msg)
    if err != nil {
        ps.fireEvent(PresenceEvent{Type: "untrusted", Message: msg})
        return // drop the presence — untrusted
    }

    if !ok {
        // New navi discovered (verified)
        ps.navis[msg.NaviID] = &PresenceEntry{
            LastSeen: time.Now(),
            Message:  msg,
            Trusted:  trusted,
        }
        ps.fireEvent(PresenceEvent{Type: "online", Message: msg})
        return
    }

    // Existing navi — update status
    oldStatus := existing.Message.Status
    existing.LastSeen = time.Now()
    existing.Message = msg
    existing.Trusted = trusted

    if oldStatus != msg.Status {
        ps.fireEvent(PresenceEvent{Type: "update", Message: msg})
    }
}

func (ps *PresenceStore) CheckStale(timeout time.Duration) {
    ps.mu.Lock()
    defer ps.mu.Unlock()

    for id, entry := range ps.navis {
        if time.Since(entry.LastSeen) > timeout {
            entry.Message.Status = PresenceOffline
            ps.fireEvent(PresenceEvent{Type: "offline", Message: entry.Message})
            delete(ps.navis, id)
        }
    }
}

// Get all navis of a specific role (only trusted navis)
func (ps *PresenceStore) GetByRole(role string) []PresenceMessage {
    ps.mu.RLock()
    defer ps.mu.RUnlock()

    var result []PresenceMessage
    for _, entry := range ps.navis {
        if entry.Message.Role == role &&
           entry.Message.Status == PresenceOnline &&
           entry.Trusted {
            result = append(result, entry.Message)
        }
    }
    return result
}

// Get all available navis of a specific type (only trusted navis)
func (ps *PresenceStore) GetAvailable(capability string) []PresenceMessage {
    ps.mu.RLock()
    defer ps.mu.RUnlock()

    var result []PresenceMessage
    for _, entry := range ps.navis {
        if entry.Message.Status != PresenceOnline || !entry.Trusted {
            continue
        }
        for _, cap := range entry.Message.Capabilities {
            if cap == capability {
                result = append(result, entry.Message)
            }
        }
    }
    return result
}
```

### 2.5 Presence in the Navi Definition and Runtime Config

Presence settings split across the two sources per [S000200 §04](../00-navi-core/02-navi-definition.md): what the navi advertises is part of the static Go definition compiled into the binary, while transport and timing values are deployment-time runtime config.

```go
// cmd/spec-agent/main.go
sdk.Build(navi{
    name: "spec-agent",
    // ...
    presence: PresenceConfig{
        Enabled: true,

        // What this navi advertises (definition-level, compiled in)
        Capabilities: []string{
            "spec-writing",
            "user-story-creation",
            "clarifying-questions",
        },
        Role:        "spec-writer",
        AcceptsWork: true, // can receive handoffs
    },
})
```

```yaml
# .config/navi/spec-agent/config.yaml
presence:
  mechanism: "unix-socket"     # same-machine
  socket_path: "/tmp/opennavi-presence.sock"
  heartbeat_interval: "30s"
  stale_timeout: "120s"        # consider offline after 2 min of no heartbeat
  listen: true
```

### Requirement: Signed Presence Broadcast
The system SHALL require each navi to broadcast a signed presence announcement when it comes online, including its identity, role, capabilities, and public key.

#### Scenario: Broadcasting signed presence on startup
- **GIVEN** a navi has loaded its keypair and enabled the P2P interface
- **WHEN** the navi comes online
- **THEN** the navi SHALL broadcast a presence message containing its NaviID, name, role, version, status, capabilities, endpoint, public key, and signature
- **AND** the presence message SHALL be signed with the navi's private key

### Requirement: Presence Verification Against Trusted Keys
The system SHALL verify every received presence broadcast against the trusted key store, accepting only messages from known and trusted public keys.

#### Scenario: Verifying a presence broadcast from an unknown key
- **GIVEN** a navi receives a presence broadcast from another navi
- **WHEN** the receiving navi verifies the broadcast
- **THEN** the system SHALL extract the public key from the message
- **AND** check whether the key exists in the trusted key store
- **AND** verify the message signature against the claimed key
- **AND** accept the presence only if both checks pass

### Requirement: Multi-Environment Presence Discovery
The system SHALL support presence discovery across multiple deployment environments including same-machine Unix sockets, Docker DNS-SD, Kubernetes annotations, LAN mDNS, and optional remote registry.

#### Scenario: Discovering navis on the same machine
- **GIVEN** a navi is configured with Unix socket presence mechanism
- **WHEN** the navi starts
- **THEN** the navi SHALL listen for presence broadcasts on the configured socket path
- **AND** the navi SHALL broadcast its own presence at the configured heartbeat interval

### Requirement: Presence Store With Trust Gating
The system SHALL maintain a local presence store that only accepts entries from navis whose public key has been verified against the trusted key store.

#### Scenario: Storing a verified presence entry
- **GIVEN** a navi has received and verified a presence broadcast
- **WHEN** the presence is accepted
- **THEN** the system SHALL store the presence entry in the local presence store
- **AND** the entry SHALL include the last-seen timestamp, presence message, and trust status

### Requirement: Stale Presence Timeout
The system SHALL mark a navi as offline and remove it from the presence store when no heartbeat is received within the configured stale timeout.

#### Scenario: Removing a stale presence entry
- **GIVEN** a navi has been in the presence store
- **WHEN** no heartbeat is received within the configured stale timeout
- **THEN** the system SHALL mark the navi as offline
- **AND** the system SHALL remove the entry from the presence store

---


---

## Related


### Other Parts

- [Trust, Identity & Sessions](./01-trust-identity-sessions.md)
- [IAM & Idle Scheduling](./02-iam-idle-scheduling.md)
- [Config, Security & Design Decisions](./03-config-security-design.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S040000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |