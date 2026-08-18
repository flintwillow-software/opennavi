---
status: draft
created: '17'
updated: '2026-08-11'
tags:
  - spec
  - operations
priority: medium
spec-refs: [080200, 080201, 080202, 080203, 080204, 080205]
---

# Plan: Cross-Cutting Architectural Decisions: Testing, Security & API Versioning

**Scope:** Cross-cutting concerns that affect every layer — concurrency, error handling, logging, testing, resource management, and more.

> **Part of:** [Plan: Cross-Cutting Architectural Decisions](./README.md) — split into 2 parts for readability.

## 00 Testing Strategy

### 5.1 Test Layers

```
┌──────────────────────────────────────────────────────────────┐
│                     Testing Pyramid                          │
│                                                              │
│                           ┌───┐                              │
│                          /  U  \                             │  Unit tests (fast, isolated)
│                         /   N   \                            │
│                        /    I    \                           │
│                       /______T_____\                          │
│                                                              │
│                  ┌───────────────┐                          │
│                 /   Integration   \                         │  Integration tests (multi-component)
│                /___________________\                        │
│                                                              │
│           ┌───────────────────────────┐                     │
│          /      E2E / Contract         \                    │  E2E tests (real adapters, real LLM)
│         /________________________________\                   │
└──────────────────────────────────────────────────────────────┘
```

### 5.2 Unit Tests

```go
// Every public function has a unit test
func TestShortTermMemory_Append(t *testing.T) {
    m := &ShortTermMemory{MaxChars: 100}

    // Test normal append
    if err := m.Append("hello"); err != nil {
        t.Fatal(err)
    }
    if m.CurrentChars != 5 {
        t.Errorf("expected 5, got %d", m.CurrentChars)
    }

    // Test auto-compaction
    m.Content = strings.Repeat("x", 80)
    m.CurrentChars = 80
    if err := m.Append("test"); err != nil {
        t.Fatal(err)
    }
    if m.CurrentChars >= m.MaxChars {
        t.Errorf("expected compaction, got %d chars", m.CurrentChars)
    }
}

func TestMessageRouter_Resolve(t *testing.T) {
    router := &MessageRouter{
        agentRegistry: buildTestRegistry(),
        sessionManager: buildTestSessionManager(),
    }

    // Test @mention routing
    msg := &Message{
        Content: MessageContent{Text: "@coder help me"},
        From: MessageSource{Platform: "telegram", UserID: "123"},
    }
    target, err := router.Resolve(msg)
    if err != nil {
        t.Fatal(err)
    }
    if target.AgentID != "coder" {
        t.Errorf("expected agent 'coder', got '%s'", target.AgentID)
    }
}

func TestAgentConfig_Validate(t *testing.T) {
    tests := []struct {
        name    string
        config  AgentConfig
        wantErr bool
        errKind ErrorKind
    }{
        {
            name:    "valid config",
            config:  AgentConfig{Type: AgentPrimary, Provider: "anthropic", Model: "claude-sonnet-4"},
            wantErr: false,
        },
        {
            name:    "missing name",
            config:  AgentConfig{Type: AgentPrimary, Provider: "anthropic", Model: "claude-sonnet-4"},
            wantErr: true,
            errKind: ErrKindConfig,
        },
        {
            name:    "invalid temperature",
            config:  AgentConfig{Type: AgentPrimary, Provider: "anthropic", Model: "claude-sonnet-4", Temperature: 3.0},
            wantErr: true,
            errKind: ErrKindConfig,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := (&ConfigValidator{}).ValidateAgentConfig(&tt.config)
            if tt.wantErr && err == nil {
                t.Error("expected error, got nil")
            }
            if !tt.wantErr && err != nil {
                t.Errorf("unexpected error: %v", err)
            }
            if tt.wantErr && tt.errKind != "" {
                var openNaviErr *OpenNaviError
                if errors.As(err, &openNaviErr) && openNaviErr.Kind != tt.errKind {
                    t.Errorf("expected kind %s, got %s", tt.errKind, openNaviErr.Kind)
                }
            }
        })
    }
}
```

### 5.3 Integration Tests

```go
// Test agent loop with mock LLM
func TestAgentLoop(t *testing.T) {
    mockProvider := &MockLLMProvider{
        Responses: []string{
            "I'll help with that.",
            "", // stop
        },
        CallCount: 0,
    }

    config := AgentConfig{
        Provider: "mock",
        Model: "test",
        Temperature: 0.7,
        MaxTokens: 100,
        Tools: []AgentTool{
            {
                Name:        "echo",
                Description: "Echo input",
                Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
                Execute: func(args map[string]any) (*ToolResult, error) {
                    return &ToolResult{Content: fmt.Sprintf("echo: %s", args["text"])}, nil
                },
            },
        },
    }

    agent := NewAgent(config)
    agent.provider = mockProvider

    err := agent.Prompt("Hello")
    if err != nil {
        t.Fatal(err)
    }

    if mockProvider.CallCount != 2 {
        t.Errorf("expected 2 LLM calls, got %d", mockProvider.CallCount)
    }

    if agent.State().Status != StatusIdle {
        t.Errorf("expected idle status, got %s", agent.State().Status)
    }
}

// Test gateway message flow
func TestGatewayMessageFlow(t *testing.T) {
    gateway := buildTestGateway()

    // Simulate webhook message
    raw := RawMessage{
        Adapter: &TestWebhookAdapter{},
        Payload: []byte(`{"user":"alice","message":"hello","platform":"webhook"}`),
    }

    // Should route to session, create agent, deliver response
    done := make(chan struct{})
    gateway.HandleMessage(raw)

    // Verify session was created
    session, err := gateway.SessionManager.Get("webhook:alice")
    if err != nil {
        t.Fatal(err)
    }
    if session == nil {
        t.Fatal("expected session to be created")
    }

    // Verify response was delivered
    select {
    case <-done:
        // OK
    case <-time.After(5 * time.Second):
        t.Fatal("timeout waiting for response")
    }
}
```

### 5.4 Mock Interfaces

```go
// Every external dependency has a mock interface
type MockLLMProvider struct {
    Responses []string
    CallCount int
    Err       error
}

func (m *MockLLMProvider) Stream(ctx context.Context, messages []llm.Message, tools []llm.Tool) (<-chan llm.StreamEvent, error) {
    m.CallCount++
    ch := make(chan llm.StreamEvent)

    go func() {
        defer close(ch)
        for _, resp := range m.Responses {
            ch <- llm.StreamEvent{
                Type: llm.StreamEventContentDelta,
                Content: resp,
            }
        }
        ch <- llm.StreamEvent{Type: llm.StreamEventStop}
    }()

    return ch, m.Err
}

type MockAdapter struct {
    NameValue  string
    StartFn    func(ctx context.Context, handler MessageHandler) error
    StopFn     func() error
    DeliverFn  func(sessionKey SessionKey, msg Message) error
    ParserFn   func() MessageParser
}

func (m *MockAdapter) Name() string {
    if m.NameValue != "" {
        return m.NameValue
    }
    return "mock"
}

func (m *MockAdapter) Start(ctx context.Context, handler MessageHandler) error {
    if m.StartFn != nil {
        return m.StartFn(ctx, handler)
    }
    return nil
}

func (m *MockAdapter) Stop() error {
    if m.StopFn != nil {
        return m.StopFn()
    }
    return nil
}

func (m *MockAdapter) Deliver(sessionKey SessionKey, msg Message) error {
    if m.DeliverFn != nil {
        return m.DeliverFn(sessionKey, msg)
    }
    return nil
}

func (m *MockAdapter) Parser() MessageParser {
    if m.ParserFn != nil {
        return m.ParserFn()
    }
    return &TestParser{}
}
```

### 5.5 Test Coverage Goals

| Layer | Target Coverage | Notes |
|-------|----------------|-------|
| **LLM Provider** | 80%+ | Core types, transforms, validation |
| **Agent** | 85%+ | State management, tool execution, event system |
| **Session** | 80%+ | Branching, compaction, persistence |
| **Config** | 90%+ | Validation, defaults, merge logic |
| **Gateway** | 70%+ | Router, auth, hooks |
| **Plugins** | 75%+ | Discovery, lifecycle, validation |
| **TUI** | 50% | Hard to test TUI rendering; focus on component logic |

### Requirement: Test Layer Pyramid
The system SHALL organize automated tests into three layers: unit tests (fast, isolated), integration tests (multi-component), and E2E/contract tests (real adapters, real LLM).

#### Scenario: Test Layer Selection
- GIVEN a piece of new functionality
- WHEN tests are written for it
- THEN unit tests SHALL cover isolated functions with no external dependencies
- AND integration tests SHALL cover multi-component interactions (e.g., agent loop with mock LLM, gateway message flow)
- AND E2E/contract tests SHALL exercise real adapters and real LLM providers

### Requirement: Unit Test Coverage for Public Functions
The system SHALL provide a unit test for every public function, covering normal behavior, error paths, and edge cases such as auto-compaction and invalid configuration.

#### Scenario: Unit Test Completeness
- GIVEN a public function (e.g., `ShortTermMemory.Append`, `MessageRouter.Resolve`, `AgentConfig.Validate`)
- WHEN the function is tested
- THEN a unit test SHALL verify normal behavior and assert exact expected values
- AND a unit test SHALL verify error paths, including typed error kinds via `errors.As`

### Requirement: Mock Interfaces for External Dependencies
The system SHALL define mock interfaces for every external dependency (LLM providers, platform adapters) so components are testable without real APIs.

#### Scenario: Mock Dependency Usage
- GIVEN a component that depends on an external service (LLM provider or adapter)
- WHEN it is tested
- THEN the test SHALL substitute a mock implementation (e.g., `MockLLMProvider`, `MockAdapter`)
- AND the mock SHALL record call counts and programmable responses/errors for assertions

### Requirement: Test Coverage Goals
The system SHALL target defined test coverage percentages per component: LLM provider 80%+, agent 85%+, session 80%+, config 90%+, gateway 70%+, plugins 75%+, and TUI 50% (focusing on component logic).

#### Scenario: Coverage Enforcement
- GIVEN a component with a coverage target
- WHEN its test suite is measured
- THEN coverage SHALL meet or exceed the target for that component
- AND TUI rendering SHALL be excluded from strict coverage requirements in favor of component logic tests

---


## 01 Resource Management

### 6.1 Memory Budgets

```go
// Hard limits to prevent unbounded memory growth
var MemoryLimits = struct {
    ShortTermMaxChars int           // 4096 (enforced in ShortTermMemory)
    LongTermMaxEntries int          // 10000 (enforced by pruning)
    MaxSessionMessages int           // 5000 (enforced by compaction)
    MaxContextTokens  int           // 128000 (enforced by context building)
    MaxToolOutputBytes int           // 1MB per tool call output
    MaxMemoryPerEntry int           // 10KB per memory entry
}{
    ShortTermMaxChars:  4096,
    LongTermMaxEntries: 10000,
    MaxSessionMessages: 5000,
    MaxContextTokens:   128000,
    MaxToolOutputBytes: 1024 * 1024,
    MaxMemoryPerEntry:  10 * 1024,
}
```

### 6.2 Goroutine Limits

```go
// Maximum goroutines per component to prevent runaway spawning
var GoroutineLimits = struct {
    AgentLoop      int  // 1 per agent (fixed)
    ToolExecutor   int  // maxParallel (default 4)
    AdapterWorkers int  // 1 per adapter (fixed)
    CronWorkers    int  // 1 per cron job (fixed)
    PluginWorkers  int  // 1 per standalone plugin (fixed)
    HookWorkers    int  // 10 max (hook functions run synchronously)
}{
    AgentLoop:      1,
    ToolExecutor:   4,
    AdapterWorkers: 1,
    CronWorkers:    1,
    PluginWorkers:  1,
    HookWorkers:    10,
}
```

### 6.3 Cleanup Guarantees

```go
// Every component has a guaranteed cleanup path
type Component interface {
    Start(ctx context.Context) error
    Stop() error
}

// Ensure all components are stopped
func ShutdownAll(components ...Component) {
    // Stop in reverse order
    for i := len(components) - 1; i >= 0; i-- {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()

        done := make(chan struct{})
        go func(c Component) {
            c.Stop()
            close(done)
        }(components[i])

        select {
        case <-done:
            // OK
        case <-ctx.Done():
            log.Printf("component shutdown timeout: %T", components[i])
        }
    }
}

// Usage in main
func main() {
    gateway := NewGateway(config)
    agentPool := NewAgentPool(config)
    sessionManager := NewSessionManager(config)

    // Register for cleanup
    defer ShutdownAll(gateway, agentPool, sessionManager)
}
```

### Requirement: Memory Budget Limits
The system SHALL enforce hard memory limits to prevent unbounded growth: 4096 chars short-term memory, 10000 long-term entries, 5000 session messages, 128000 context tokens, 1MB max tool output, and 10KB per memory entry.

#### Scenario: Memory Limit Enforcement
- GIVEN a component that accumulates data (memory, sessions, context, tool output)
- WHEN it approaches the defined limit
- THEN the system SHALL enforce the corresponding hard limit (e.g., compaction of short-term memory at 4096 chars, pruning of long-term entries at 10000, truncation of tool output at 1MB)

### Requirement: Goroutine Limits per Component
The system SHALL bound goroutine usage per component: 1 per agent loop, maxParallel (default 4) for tool execution, 1 per adapter, 1 per cron job, 1 per standalone plugin, and 10 max for hook workers.

#### Scenario: Goroutine Limit Enforcement
- GIVEN a component that spawns goroutines
- WHEN it is running
- THEN the system SHALL keep goroutine count within the defined per-component limit
- AND tool execution SHALL default to 4 parallel goroutines when not configured
- AND hook workers SHALL be capped at 10

### Requirement: Cleanup Guarantees
The system SHALL provide a guaranteed cleanup path for every component via a `Component` interface (`Start`/`Stop`) and a `ShutdownAll` helper that stops components in reverse order with a 5-second timeout per component.

#### Scenario: Shutdown of All Components
- GIVEN multiple running components (gateway, agent pool, session manager)
- WHEN the process shuts down
- THEN the system SHALL call `Stop()` on each component in reverse startup order
- AND it SHALL time out any component that does not stop within 5 seconds, logging the timeout

---


## 02 Plugin Security Model

### 7.1 Capability Declaration

```go
type PluginCapabilities struct {
    CanReadFiles  bool   // can read any file
    CanWriteFiles bool   // can write any file
    CanExecShell  bool   // can execute shell commands
    CanAccessNet  bool   // can make network requests
    CanAccessEnv  bool   // can read environment variables
    MaxOutputBytes int   // output size limit
    Timeout        time.Duration // per-call timeout
    Sandbox        bool    // run in sandbox (future)
}

type PluginConfig struct {
    Name        string
    Enabled     bool
    Capabilities PluginCapabilities
    Settings    map[string]any
}
```

### 7.2 Capability Enforcement

```go
// Tool execution checks capabilities before running
func (e *ToolExecutor) Execute(ctx context.Context, call ToolCall) (*ToolResult, error) {
    plugin := e.getPluginForTool(call.Name)

    // Check capabilities
    switch call.Name {
    case "bash", "sh":
        if !plugin.Capabilities.CanExecShell {
            return nil, &OpenNaviError{
                Kind:      ErrKindPlugin,
                Message:   fmt.Sprintf("plugin %s lacks CanExecShell capability", plugin.Name),
                Code:      "CAPABILITY_DENIED",
                Recoverable: false,
            }
        }
    case "read", "write", "edit":
        if !plugin.Capabilities.CanReadFiles && !plugin.Capabilities.CanWriteFiles {
            return nil, &OpenNaviError{
                Kind:      ErrKindPlugin,
                Message:   fmt.Sprintf("plugin %s lacks file access", plugin.Name),
                Code:      "CAPABILITY_DENIED",
                Recoverable: false,
            }
        }
    }

    // Apply output size limit
    result, err := call.Execute(ctx)
    if err != nil {
        return nil, err
    }

    if plugin.Capabilities.MaxOutputBytes > 0 && len(result.Content) > plugin.Capabilities.MaxOutputBytes {
        result.Content = result.Content[:plugin.Capabilities.MaxOutputBytes] + "\n... [truncated]"
    }

    return result, nil
}
```

### 7.3 Standalone Plugin Resource Limits

```go
type StandalonePluginConfig struct {
    Name        string
    Path        string
    Enabled     bool
    Timeout     time.Duration // per-tool-call (default: 30s)
    MaxOutput   int           // max bytes (default: 1MB)
    RestartOnCrash bool        // auto-restart on crash (default: true)
    MaxMemory   int64         // max RSS memory (default: 256MB)
    CPUQuota    float64       // CPU usage limit (default: 1.0 = 100%)
}

// Process resource monitoring
type ProcessMonitor struct {
    pid       int
    maxMemory int64
    maxCPU    float64
}

func (m *ProcessMonitor) Check(process *os.Process) error {
    // Check memory usage
    if m.maxMemory > 0 {
        mem, err := getProcessMemory(process.Pid)
        if err == nil && mem > m.maxMemory {
            return fmt.Errorf("plugin exceeded memory limit: %d > %d", mem, m.maxMemory)
        }
    }

    // Check CPU usage
    if m.maxCPU > 0 {
        cpu, err := getProcessCPU(process.Pid)
        if err == nil && cpu > m.maxCPU {
            return fmt.Errorf("plugin exceeded CPU limit: %.2f > %.2f", cpu, m.maxCPU)
        }
    }

    return nil
}
```

### Requirement: Capability Declaration
The system SHALL require every plugin to declare its capabilities (file read/write, shell execution, network access, environment access), output size limit, timeout, and sandbox flag.

#### Scenario: Capability Declaration
- GIVEN a plugin being configured
- WHEN it is loaded
- THEN its `PluginCapabilities` SHALL be declared including `CanReadFiles`, `CanWriteFiles`, `CanExecShell`, `CanAccessNet`, and `CanAccessEnv`
- AND its `MaxOutputBytes`, per-call `Timeout`, and `Sandbox` flag SHALL be declared

### Requirement: Capability Enforcement
The system SHALL check a plugin's declared capabilities before executing any of its tools, denying tools that require undeclared capabilities and applying the output size limit.

#### Scenario: Capability Denial
- GIVEN a plugin without the `CanExecShell` capability
- WHEN a `bash` or `sh` tool call is requested from it
- THEN the system SHALL return a non-recoverable `OpenNaviError` with `Kind: ErrKindPlugin` and code `CAPABILITY_DENIED`
- AND it SHALL deny file tools unless the plugin declares file read or write capability
- AND it SHALL truncate tool output to `MaxOutputBytes` with an `... [truncated]` suffix when exceeded

### Requirement: Standalone Plugin Resource Limits
The system SHALL enforce resource limits for standalone plugins: per-tool-call timeout (default 30s), max output (default 1MB), auto-restart on crash (default on), max RSS memory (default 256MB), and CPU quota (default 100%).

#### Scenario: Standalone Plugin Monitoring
- GIVEN a standalone plugin process being monitored by `ProcessMonitor`
- WHEN it exceeds its configured memory or CPU limit
- THEN the system SHALL return an error identifying the exceeded limit (memory or CPU)
- AND it SHALL apply the per-call timeout and output limits, and auto-restart the plugin on crash when enabled

---


## 03 API Versioning

### 8.1 Versioned Protocols

```go
// Plugin protocol versioning
const PluginProtocolVersion = "1.0"

type Plugin interface {
    Name() string
    Version() string
    ProtocolVersion() string // "1.0"
    // ...
}

// Check protocol compatibility
func isProtocolCompatible(hostVersion, pluginVersion string) bool {
    hostMajor, hostMinor := parseVersion(hostVersion)
    pluginMajor, pluginMinor := parseVersion(pluginVersion)

    // Compatible if major version matches
    return hostMajor == pluginMajor
}
```

### 8.2 Config Versioning

```yaml
# Config file version
config_version: "1.0"

# Migration: when config_version changes, apply migrations
migrations:
  - from: "0.9"
    to: "1.0"
    steps:
      - rename: "agents.default" → "agents[0]"
      - rename: "agents.subagents" → "agents[1]"
      - add: "memory.short_term.max_chars" = 4096
```

---


## 04 Graceful Degradation

### 9.1 Component Failure Modes

| Component | Failure | Degradation |
|-----------|---------|-------------|
| **LLM Provider** | API error | Retry → fallback provider → inform user |
| **Plugin** | Crash | Restart (standalone) / disable (in-process) → continue without plugin tools |
| **Session Storage** | Disk error | Continue in-memory → warn user → try next storage backend |
| **Gateway Adapter** | Connection lost | Log error → retry connection → disable adapter temporarily |
| **Memory** | SQLite corruption | Drop index → rebuild → continue |
| **Config** | Invalid YAML | Use defaults + log warning → continue |
| **Cron** | Job fails | Log error → skip → next scheduled run |

### 9.2 Fallback Provider

```go
// If primary provider fails, try fallback
type ProviderFallback struct {
    primary   llm.Provider
    fallback  llm.Provider
    maxRetries int
}

func (f *ProviderFallback) Stream(ctx context.Context, messages []llm.Message, tools []llm.Tool) (<-chan llm.StreamEvent, error) {
    events, err := f.primary.Stream(ctx, messages, tools)
    if err != nil && f.shouldRetry(err) && f.maxRetries > 0 {
        // Retry primary
        events, err = f.primary.Stream(ctx, messages, tools)
    }

    if err != nil && f.fallback != nil {
        log.Printf("primary provider failed, falling back to %s", f.fallback.Name())
        return f.fallback.Stream(ctx, messages, tools)
    }

    return events, err
}
```

### Requirement: Component Failure Modes
The system SHALL define a degradation path for each component failure: retry then fallback then inform for LLM provider, restart or disable for plugins, in-memory continuation for session storage, temporary disable for gateway adapters, index rebuild for SQLite corruption, defaults for invalid config, and skip for failing cron jobs.

#### Scenario: Component Degradation
- GIVEN a component failure (e.g., adapter connection lost, config YAML invalid, cron job failure)
- WHEN the failure occurs
- THEN the system SHALL follow the defined degradation path for that component
- AND a single component failure SHALL NOT take down the system

### Requirement: Provider Fallback
The system SHALL fall back to a secondary LLM provider when the primary provider fails, after retrying retryable errors.

#### Scenario: Fallback on Primary Failure
- GIVEN a primary provider that returns an error
- WHEN the error is retryable and retries remain
- THEN the system SHALL retry the primary provider
- AND when the primary still fails and a fallback provider is configured, it SHALL use the fallback provider and log the fallback
- AND when no fallback is available, the error SHALL propagate to the caller

---


## 05 Key Design Decisions Summary

| Decision | Rationale |
|----------|-----------|
| **Channels over mutexes** | Go's strength; prevents deadlock, easier to reason about |
| **Context-driven cancellation** | Clean shutdown, no goroutine leaks |
| **Structured errors with `Kind`** | Callers can handle errors specifically without type assertions |
| **Typed error with `Recoverable`** | Tells callers whether retrying makes sense |
| **Structured logging with trace IDs** | Correlate events across layers for debugging |
| **Config validation at load time** | Fail fast, not at runtime |
| **Mock interfaces for all external deps** | Testability without real APIs |
| **Hard memory limits** | Prevent unbounded growth in production |
| **Goroutine limits per component** | Prevent runaway spawning |
| **Capability-based plugin security** | Principle of least privilege for plugins |
| **Protocol versioning (major.minor)** | Forward-compatible plugin updates |
| **Graceful degradation per component** | One failure doesn't take down the system |
| **Provider fallback** | Availability when primary provider is down |
| **Hot-reload for config** | No restart needed for config changes |

---

## Related


### Other Parts

- [Concurrency, Errors & Logging](./00-concurrency-logging.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080200 | 0.1.0 | Migrated from docs/spec/ to spec-tree |