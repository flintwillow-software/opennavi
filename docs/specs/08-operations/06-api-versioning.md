---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - operations
priority: medium
spec-refs: [080600, 080601, 080602, 080603, 080604, 080605, 080606, 080607]
---

# Plan: API Versioning Strategy

**Scope:** Versioning for OpenNavi's runtime HTTP/WebSocket endpoints, plugin protocol, and config file format. Based on patterns from Home Assistant, Node-RED, pi-code, and Hermes Agent.

---

## 00 Overview

OpenNavi exposes multiple APIs that evolve over time:
- **HTTP REST endpoints** — Health checks, webhook receivers, WebSocket chat
- **WebSocket message protocol** — Real-time agent event streaming to clients
- **Plugin/Extension protocol** — JSON-RPC communication with extensions (defined in 06-extension-contract.md)
- **Config file format** — YAML configuration files loaded at runtime

Each has different versioning requirements. This doc defines the strategy for each.

### Requirement: Distinct Versioning Strategy Per API Surface

The system SHALL apply a distinct versioning strategy to each of the four API surfaces — REST endpoints, WebSocket message protocol, plugin/extension protocol, and config file format — appropriate to each surface's evolution requirements.

#### Scenario: Independent surface evolution
- **GIVEN** the WebSocket protocol version advances
- **WHEN** the system releases a new version
- **THEN** the REST, plugin, and config versioning schemes SHALL remain unchanged unless their own requirements dictate a version bump

### Requirement: Multi-Surface Versioning Documentation

The system SHALL define a versioning strategy for each of the four API surfaces — URL path prefix for REST, calendar-versioned handshake for WebSocket, major.minor protocol string for plugins, and explicit version field with migration steps for config files.

#### Scenario: Strategy availability
- **GIVEN** a developer needs to version any of the four API surfaces
- **WHEN** they consult this spec
- **THEN** the system SHALL provide a documented versioning approach for that surface

---

## 01 REST Endpoint Versioning

### 2.1 Strategy: URL Path Prefix (`/v1/`)

REST endpoints use a simple URL path prefix for versioning:

```
GET /health                          # Unversioned — always stable
GET /v1/sessions                     # Versioned session API
POST /v1/chat                      # Versioned chat endpoint
WS /v1/ws                          # Versioned WebSocket
```

**Why URL path?**
- Simple to understand and debug
- Works with standard HTTP caching/proxying
- No content negotiation complexity
- Easy to add new versions without breaking old ones (e.g., `/v2/sessions`)
- Matches patterns used by Home Assistant, Node-RED, and most Go REST APIs

**Why not content negotiation (`Accept: application/vnd.opennavi.v1+json`)?**
- Adds complexity for simple clients
- Harder to debug in browser dev tools
- WebSocket can't use Accept headers meaningfully

### 2.2 Version Lifecycle

| Stage | Description | Timeline |
|-------|-------------|----------|
| **Experimental** | Marked with `X-Experimental: true` header, may change without notice | N/A |
| **Stable** | Fully documented, backward-compatible changes only | Indefinite |
| **Deprecated** | Still works but emits deprecation warnings, scheduled for removal | 6 months minimum |
| **Removed** | Endpoint no longer exists, returns 404 with migration hint | After sunset date |

### 2.3 Deprecation Headers

```http
HTTP/1.1 200 OK
Content-Type: application/json
Sunset: Sat, 01 Jan 2027 00:00:00 GMT
Deprecation: true
Link: <https://docs.opennavi.io/migration/v1-to-v2>; rel="successor-version"

{"status": "ok", ...}
```

Clients should check `Sunset` and `Deprecation` headers to detect deprecated endpoints.

### 2.4 Endpoint Examples

```go
// Unversioned — always available, minimal changes
func healthHandler(w http.ResponseWriter, r *http.Request) {
    // No version header needed
    json.NewEncoder(w).Encode(healthResponse)
}

// Versioned session API
func v1SessionsHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("X-API-Version", "v1")
    
    switch r.Method {
    case "GET":
        // List sessions
    case "POST":
        // Create session
    }
}

// Experimental endpoint (subject to change without notice)
func experimentalWebhookHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("X-Experimental", "true")
    // May be removed or changed in any future release
}
```

### 2.5 Migration Guide Pattern

When deprecating an endpoint, provide a migration guide:

```go
// v1 → v2 migration example
func deprecatedV1SessionsHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Sunset", "Sat, 01 Jan 2027 00:00:00 GMT")
    w.Header().Set("Deprecation", "true")
    w.Header().Set("Link", "</v2/sessions>; rel=\"successor-version\"")
    
    // Log deprecation warning for monitoring
    log.Warnf("Deprecated v1 sessions endpoint called from %s", r.RemoteAddr)
    
    // Redirect to v2 with 308 Permanent Redirect (preserves method and body)
    http.Redirect(w, r, "/v2/sessions"+r.URL.Path, http.StatusPermanentRedirect)
}
```

### Requirement: URL Path Prefix Versioning

The system SHALL version REST endpoints using a URL path prefix (`/v1/`, `/v2/`), with unversioned endpoints such as `/health` remaining always stable, and versioned endpoints responding with the `X-API-Version` header.

#### Scenario: Versioned session API
- **GIVEN** a client requests `GET /v1/sessions`
- **WHEN** the request reaches the versioned handler
- **THEN** the system SHALL route it to the v1 session handler and respond with `X-API-Version: v1`

#### Scenario: Unversioned health endpoint
- **GIVEN** a client requests `GET /health`
- **WHEN** the request reaches the health handler
- **THEN** the system SHALL serve it without a version prefix or version header, remaining stable across releases

### Requirement: Version Lifecycle Management

The system SHALL manage REST endpoint versions through a lifecycle of experimental, stable, deprecated, and removed stages, with deprecated endpoints remaining available for a minimum of 6 months before removal.

#### Scenario: Experimental endpoint
- **GIVEN** an endpoint is experimental
- **WHEN** the endpoint responds
- **THEN** the system SHALL mark it with the `X-Experimental: true` header and allow it to change without notice

#### Scenario: Deprecated endpoint removal
- **GIVEN** an endpoint has passed its sunset date
- **WHEN** a client requests the removed endpoint
- **THEN** the system SHALL return a 404 with a migration hint

### Requirement: Deprecation Headers

The system SHALL emit `Sunset`, `Deprecation`, and `Link` (with `rel="successor-version"`) headers on responses from deprecated endpoints so clients can detect upcoming removal.

#### Scenario: Deprecation detection by clients
- **GIVEN** a client calls a deprecated endpoint
- **WHEN** the endpoint responds
- **THEN** the system SHALL include `Deprecation: true`, the `Sunset` date, and a `Link` header pointing to the successor version

### Requirement: Deprecation Migration Redirection

The system SHALL redirect requests to a deprecated endpoint to its successor version using a 308 Permanent Redirect (preserving method and body) and SHALL log a deprecation warning for monitoring.

#### Scenario: v1 to v2 redirect
- **GIVEN** a client calls a deprecated v1 sessions endpoint
- **WHEN** the deprecated handler runs
- **THEN** the system SHALL log a deprecation warning and redirect the client to `/v2/sessions` with a 308 Permanent Redirect

---

## 02 WebSocket Message Protocol Versioning

### 3.1 Strategy: Semantic Version String in Handshake

WebSocket connections negotiate version during the initial handshake message:

```json
// Client → Server (initial message)
{
    "type": "handshake",
    "version": "2026.7.0",
    "capabilities": ["streaming", "tool_calls"]
}

// Server → Client (response)
{
    "type": "handshake_ack",
    "version": "2026.7.0",
    "compatible": true,
    "server_capabilities": ["streaming", "tool_calls", "council_mode"],
    "features_required": []
}
```

**Version format:** `YYYY.M.D` (calendar versioning) for clarity:
- `2026.7.0` = July 2026, patch 0
- Easy to see which major feature set is in use
- Backward-compatible with semantic versioning for tooling

### 3.2 Capability Negotiation

Instead of strict version pinning, negotiate capabilities:

```json
// Client declares what it supports
{
    "type": "handshake",
    "version": "2026.7.0",
    "capabilities": {
        "streaming": true,          // Can handle streaming responses
        "tool_calls": true,         // Can display tool call UI
        "council_mode": false,      // Cannot participate in council mode
        "image_upload": true        // Can send images to agent
    }
}

// Server responds with what it offers
{
    "type": "handshake_ack",
    "compatible": true,  // true if all required capabilities are met
    "server_capabilities": {
        "streaming": true,
        "tool_calls": true,
        "council_mode": true,
        "image_upload": false  // Server doesn't support image upload yet
    },
    "features_required": ["streaming"]  // Client must support these
}
```

### 3.3 Message Format Version Tagging

Each WebSocket message includes a version tag for forward compatibility:

```json
{
    "type": "message_delta",
    "version": "1.0",
    "content": "...",
    "metadata": {...}
}
```

If the server sends a message with an unrecognized `version`, clients should log a warning but continue processing known fields. Unknown fields are silently ignored (forward compatibility).

### 3.4 Version Compatibility Matrix

| Server Version | Client Version Supported | Notes |
|---------------|-------------------------|-------|
| v1 (2026.5) | v1 only | Original WebSocket protocol |
| v2 (2026.7) | v1 + v2 | Added council mode messages, capability negotiation |

### Requirement: Handshake Version Negotiation

The system SHALL negotiate the WebSocket protocol version during the initial handshake message using `YYYY.M.D` calendar versioning, with the server responding with a `handshake_ack` containing the negotiated version and compatibility status.

#### Scenario: Compatible handshake
- **GIVEN** a client sends a handshake with a supported version and capabilities
- **WHEN** the server processes the handshake
- **THEN** the system SHALL respond with `handshake_ack` containing the server version and `compatible: true`

### Requirement: Capability Negotiation

The system SHALL negotiate WebSocket capabilities rather than requiring exact version matches, with the server responding `compatible: true` only when all required capabilities are met, and SHALL declare `features_required` that the client must support.

#### Scenario: Missing required capability
- **GIVEN** a client handshake declares `council_mode: false`
- **WHEN** the server requires council mode in `features_required`
- **THEN** the system SHALL respond with `compatible: false`

#### Scenario: Gradual capability rollout
- **GIVEN** a client declares capabilities it supports
- **WHEN** the server responds
- **THEN** the system SHALL list its own `server_capabilities`, allowing new features to roll out without strict version pinning

### Requirement: Message Format Version Tagging

The system SHALL include a version tag on each WebSocket message for forward compatibility, with clients logging a warning on unrecognized versions while continuing to process known fields and silently ignoring unknown fields.

#### Scenario: Unrecognized message version
- **GIVEN** a server sends a message with an unrecognized `version` value
- **WHEN** a client receives the message
- **THEN** the client SHALL log a warning and continue processing the known fields

### Requirement: Backward-Compatible Server Versions

The system SHALL support clients running the previous protocol version when the server protocol advances — a v2 server SHALL accept v1 and v2 clients, while a v1 server accepts v1 clients only.

#### Scenario: v2 server with v1 client
- **GIVEN** a server running WebSocket protocol v2 (2026.7)
- **WHEN** a v1 (2026.5) client connects
- **THEN** the system SHALL accept the connection and negotiate a compatible message set

---

## 03 Plugin/Extension Protocol Versioning

### 4.1 Strategy: Major.Minor Protocol String

Extensions declare their protocol version in the manifest and during initialization:

```json
// Extension manifest.json
{
    "name": "opennavi-monitoring",
    "version": "1.2.0",
    "protocol_version": "1.0"  // Must match host's supported major version
}

// Host → Extension (initialization)
{
    "jsonrpc": "2.0",
    "method": "initialize",
    "params": {
        "protocol_version": "1.0",
        "navi_info": {...},
        "config": {...}
    },
    "id": 0
}

// Extension → Host (response)
{
    "jsonrpc": "2.0",
    "result": {
        "protocol_version": "1.0",
        "compatible": true,
        "capabilities": ["tools", "hooks"]
    },
    "id": 0
}
```

### 4.2 Version Compatibility Rules

```go
// Check protocol compatibility (from 06-extension-contract.md)
func isProtocolCompatible(hostVersion, pluginVersion string) bool {
    hostMajor, hostMinor := parseVersion(hostVersion)
    pluginMajor, pluginMinor := parseVersion(pluginVersion)

    // Compatible if major version matches
    return hostMajor == pluginMajor
}

// Upgrade path: minor versions are always backward-compatible
// e.g., 1.0 ↔ 1.2 is compatible (host supports 1.x)
// Breaking change requires major bump: 1.x → 2.0
```

### 4.3 Extension Update Strategy

When the host updates its protocol version:
1. **Major version bump** — Requires extensions to update their code
2. **Minor version bump** — Extensions continue working (backward compatible)
3. **Patch version bump** — Bug fixes, no extension changes needed

Host logs a warning when loading an extension with a mismatched protocol version:

```go
func validateExtensionProtocol(manifest Manifest, hostVersion string) error {
    if !isProtocolCompatible(hostVersion, manifest.ProtocolVersion) {
        return fmt.Errorf(
            "extension %s uses protocol %s but host supports %s; update required",
            manifest.Name, manifest.ProtocolVersion, hostVersion,
        )
    }
    return nil
}
```

### Requirement: Protocol Version Declaration

The system SHALL require extensions to declare their protocol version in the manifest and during initialization, with the host and extension exchanging `protocol_version` fields in the JSON-RPC `initialize` handshake.

#### Scenario: Successful extension handshake
- **GIVEN** an extension manifest declares `protocol_version: "1.0"` matching the host's major version
- **WHEN** the host initializes the extension
- **THEN** the system SHALL respond with `compatible: true` and the extension's capabilities

### Requirement: Major.Minor Compatibility Rules

The system SHALL treat plugin protocol compatibility as major-version matching — minor versions are always backward-compatible, while a breaking change requires a major version bump from 1.x to 2.0.

#### Scenario: Compatible minor versions
- **GIVEN** an extension uses protocol 1.2 and the host supports 1.x
- **WHEN** compatibility is checked
- **THEN** the system SHALL consider the extension compatible

#### Scenario: Breaking major bump
- **GIVEN** an extension uses protocol 1.x and the host has moved to protocol 2.0
- **WHEN** compatibility is checked
- **THEN** the system SHALL require the extension to be updated before loading

### Requirement: Extension Update Strategy

The system SHALL apply the extension update strategy on host protocol version changes: a major bump requires extensions to update their code, a minor bump leaves extensions working, and a patch bump requires no extension changes.

#### Scenario: Minor host version bump
- **GIVEN** the host bumps its protocol version from 1.0 to 1.1
- **WHEN** existing extensions load
- **THEN** the system SHALL load them unchanged without requiring updates

### Requirement: Mismatch Warning on Load

The system SHALL log a warning when loading an extension with a mismatched protocol version and SHALL reject incompatible extensions with an error indicating that an update is required.

#### Scenario: Incompatible extension load
- **GIVEN** an extension's protocol version is incompatible with the host
- **WHEN** the host validates the extension manifest
- **THEN** the system SHALL return an error stating the extension's protocol version, the supported host version, and that an update is required

---

## 04 Config File Versioning and Migration

### 5.1 Strategy: Explicit Version Field with Migration Steps

Config files declare their version explicitly. The system applies migrations when loading older formats:

```yaml
# agents.yaml v1.0 (current)
config_version: "1.0"

agents:
  default:
    type: primary
    provider: anthropic
    model: claude-sonnet-4-20250514
    tools: ["read", "write", "edit"]
```

### 5.2 Migration Definition Format

```yaml
# migrations.yaml (embedded in the binary or loaded from config)
migrations:
  - from_version: "0.9"
    to_version: "1.0"
    steps:
      # Rename top-level fields
      - rename: "agents.primary" → "agents.default"
      
      # Add new required fields with defaults
      - add_default: "agents[*].thinking.enabled" = true
      - add_default: "agents[*].memory.short_term.max_chars" = 4096
      
      # Transform nested structures
      - transform: "agents.subagents" → "agents[1..]" (array of sub-agents)
      
      # Validate and error on incompatible values
      - validate: "agents[*].provider" in ["anthropic", "openai", "ollama", "openrouter", "minimax"]
```

### 5.3 Migration Execution

```go
type ConfigMigration struct {
    FromVersion string
    ToVersion   string
    Steps       []MigrationStep
}

type MigrationStep struct {
    Type        string      // "rename", "add_default", "transform", "validate"
    Source      any         // source path or expression
    Target      any         // target value or default
    Validate    bool        // whether to validate after step
}

func (m *ConfigManager) Migrate(config map[string]any, fromVersion string) (map[string]any, error) {
    migrations := m.loadMigrations()
    
    for _, mig := range migrations {
        if mig.FromVersion != fromVersion {
            continue
        }
        
        // Apply each step in order
        for _, step := range mig.Steps {
            switch step.Type {
            case "rename":
                config = renameField(config, step.Source.(string), step.Target.(string))
            case "add_default":
                config = addDefaultField(config, step.Source.(string), step.Target)
            case "transform":
                config = transformStruct(config, step.Source.(string), step.TransformFn)
            case "validate":
                if err := validateField(config, step.Validate); err != nil {
                    return nil, fmt.Errorf("migration failed at %s: %w", step.Type, err)
                }
            }
        }
        
        // Update version after successful migration
        config["config_version"] = mig.ToVersion
        fromVersion = mig.ToVersion  // Chain migrations
    }
    
    return config, nil
}

// Load and apply all migrations for a given starting version
func (m *ConfigManager) loadAndApplyMigrations(config map[string]any) (map[string]any, error) {
    currentVersion := config["config_version"].(string)
    migrated, err := m.Migrate(config, currentVersion)
    if err != nil {
        return nil, fmt.Errorf("config migration failed: %w", err)
    }
    
    // Verify all migrations applied
    expectedVersion := m.latestConfigVersion()
    if migrated["config_version"] != expectedVersion {
        return nil, fmt.Errorf(
            "incomplete migration: got version %s, expected %s",
            migrated["config_version"], expectedVersion,
        )
    }
    
    return migrated, nil
}
```

### 5.4 Version History

| Version | Changes | Date |
|---------|---------|------|
| `0.9` (legacy) | Original pi-mono format | N/A |
| `1.0` (current) | New unified agent config, envvar expansion, thinking support | 2026-07 |

### Requirement: Explicit Config Version Field

The system SHALL require config files to declare an explicit `config_version` field, and SHALL apply migrations automatically when loading configs written in an older format.

#### Scenario: Loading a legacy config
- **GIVEN** a config file declares `config_version: "0.9"`
- **WHEN** the system loads the config
- **THEN** the system SHALL apply the registered migrations to bring it to the current version

### Requirement: Migration Definition Format

The system SHALL define config migrations as from/to version pairs with ordered steps of type `rename`, `add_default`, `transform`, and `validate`, with migrations embedded in the binary or loaded from config.

#### Scenario: Migration with multiple step types
- **GIVEN** a migration from 0.9 to 1.0 is registered
- **WHEN** the migration runs
- **THEN** the system SHALL apply its steps in order — renaming fields, adding defaults, transforming nested structures, and validating values

### Requirement: Chained Migration Execution

The system SHALL apply config migrations as a chain, updating the `config_version` after each successful migration and verifying that the final version equals the latest supported version.

#### Scenario: Multi-step version chain
- **GIVEN** a config at version 0.8 with migrations 0.8→0.9 and 0.9→1.0
- **WHEN** the system loads the config
- **THEN** the system SHALL apply both migrations in sequence and end with `config_version: "1.0"`

### Requirement: Migration Failure Handling

The system SHALL return an error when a config migration step fails validation or when the applied version does not reach the expected latest version.

#### Scenario: Incomplete migration
- **GIVEN** a config whose applied migrations end at version 0.9
- **WHEN** the expected latest version is 1.0
- **THEN** the system SHALL return an error stating the applied version and the expected version

---

## 05 Stable vs Experimental Classification

### 6.1 Criteria

| Criterion | Stable | Experimental |
|-----------|--------|--------------|
| **Duration** | Used in production for ≥3 months | Recently added or under active development |
| **Documentation** | Fully documented with examples | Partially documented, subject to change |
| **Testing** | Covered by integration tests | May have limited test coverage |
| **Breaking changes** | Only additive changes allowed | Can be changed or removed without notice |

### 6.2 Classification Registry

```go
type EndpointClassification struct {
    Path         string
    Version      string        // e.g., "v1", "v2"
    Status       string        // "stable", "experimental", "deprecated"
    Description  string
    SunsetDate   *time.Time    // nil if stable
    SuccessorURL string        // URL of successor endpoint (if deprecated)
}

var endpointRegistry = []EndpointClassification{
    {Path: "/health", Version: "", Status: "stable", Description: "Health check"},
    {Path: "/v1/sessions", Version: "v1", Status: "stable", Description: "Session management"},
    {Path: "/v1/chat", Version: "v1", Status: "stable", Description: "Chat endpoint"},
    {Path: "/v1/council", Version: "v2", Status: "experimental", Description: "Council mode (may change)"},
}
```

### Requirement: Stable vs Experimental Criteria

The system SHALL classify endpoints as stable or experimental based on production duration (≥3 months for stable), documentation completeness, integration test coverage, and the permitted breaking-change policy.

#### Scenario: Stable endpoint criteria
- **GIVEN** an endpoint has been in production for at least 3 months, is fully documented, and is covered by integration tests
- **WHEN** the endpoint is classified
- **THEN** the system SHALL classify it as stable with only additive changes allowed

#### Scenario: Experimental endpoint
- **GIVEN** an endpoint is recently added or under active development
- **WHEN** the endpoint is classified
- **THEN** the system SHALL classify it as experimental, subject to change or removal without notice

### Requirement: Endpoint Classification Registry

The system SHALL maintain an endpoint registry recording each endpoint's path, version, status (stable, experimental, or deprecated), description, sunset date, and successor URL.

#### Scenario: Registry lookup
- **GIVEN** the endpoint registry contains classification entries
- **WHEN** a consumer queries an endpoint's status
- **THEN** the system SHALL return its path, version, status, description, sunset date (if deprecated), and successor URL

---

## 06 Implementation Order

1. **REST versioning** — URL path prefix (`/v1/`), deprecation headers, migration guides
2. **WebSocket versioning** — Handshake negotiation with capability exchange
3. **Plugin protocol** — Major.minor protocol string, compatibility checking
4. **Config migrations** — Version field, migration steps, chain execution
5. **Endpoint registry** — Classification of stable/experimental/deprecated endpoints
6. **Sunset headers** — HTTP `Sunset` and `Deprecation` response headers

---

## 07 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **URL path versioning for REST** — `/v1/`, `/v2/` | Simple, debuggable, works with standard HTTP tooling; matches Home Assistant and Node-RED patterns |
| **Calendar versioning for WebSocket** — `YYYY.M.D` | Easier to understand than semver for protocol versions; shows when feature was added |
| **Capability negotiation over strict pinning** — Client declares what it supports | More flexible than requiring exact version match; allows gradual rollout of new features |
| **Major.minor for plugin protocol** — `1.0`, `1.1` | Matches existing extension contract (06); backward-compatible minor versions allow extensions to keep working |
| **Explicit config version with migration steps** — `config_version: "1.0"` | Makes format changes auditable; allows loading older configs without manual conversion |
| **Sunset headers for deprecation** — Standard HTTP headers | Industry standard, works with monitoring tools and automated clients |


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080600 | 0.1.0 | Migrated from docs/spec/ to spec-tree |