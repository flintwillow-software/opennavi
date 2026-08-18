---
status: draft
created: '17'
updated: '2026-08-11'
tags:
  - spec
  - operations
priority: medium
spec-refs: [080000, 080001, 080002, 080003]
---

# Plan: Cross-Cutting Architectural Decisions: Concurrency, Errors & Logging

**Scope:** Cross-cutting concerns that affect every layer — concurrency, error handling, logging, testing, resource management, and more.

> **Part of:** [Plan: Cross-Cutting Architectural Decisions](./README.md) — split into 2 parts for readability.

## 00 Concurrency Model

### 1.1 Guiding Principles

1. **Channels over mutexes** — prefer message passing for coordination, mutexes only for shared mutable state
2. **Context-driven cancellation** — every goroutine accepts a `context.Context` and respects `ctx.Done()`
3. **Fan-in/fan-out patterns** — use channels for parallel work, collect with `sync.WaitGroup`
4. **No goroutine leaks** — every goroutine has a clear termination condition
5. **One goroutine per long-lived task** — adapters, cron scheduler, event loops each get their own goroutine with lifecycle management

### 1.2 Goroutine Lifecycle Pattern

```go
type ManagedGoroutine struct {
    ctx    context.Context
    cancel context.CancelFunc
    done   chan struct{}
    mu     sync.Mutex
    err    error
}

func (g *ManagedGoroutine) Start(fn func(ctx context.Context) error) {
    g.ctx, g.cancel = context.WithCancel(context.Background())
    g.done = make(chan struct{})

    go func() {
        defer close(g.done)
        g.err = fn(g.ctx)
    }()
}

func (g *ManagedGoroutine) Stop() error {
    g.cancel()
    <-g.done
    return g.err
}

func (g *ManagedGoroutine) IsRunning() bool {
    select {
    case <-g.done:
        return false
    default:
        return true
    }
}
```

### 1.3 Two Concurrency Models

**Daemon navis** use a worker pool pattern for thought processing:

```go
// Worker pool uses a select-based event loop
func (w *Worker) Run(ctx context.Context) {
    for {
        // 1. Try to dequeue a thought
        thought, ok := w.queue.Dequeue()
        if !ok {
            // No thoughts available — wait for signal
            select {
            case <-w.queue.ThoughtAvailable():
                continue
            case <-ctx.Done():
                return
            }
        }

        // 2. Process the thought (per-thought agent loop)
        w.processThought(ctx, thought)
    }
}

func (w *Worker) processThought(ctx context.Context, thought *Thought) {
    // Each thought gets its own agent loop (same as below)
    agent := NewAgent(thoughtToConfig(thought))
    agent.Prompt(thought.Prompt)
}
```

**One-off navis and council mode** use a simpler model — single agent loop per session:

```go
// Agent loop uses a select-based event loop (per-session)
func (a *Agent) runLoop(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return // cancellation

        case msg := <-a.steeringQueue.Chan():
            // User sent a steering message during execution
            a.state = a.state.AppendMessage(AgentMessage{Role: RoleUser, Content: msg})

        case event := <-a.provider.Events():
            // LLM stream event — process and forward
            a.processStreamEvent(event)

        case toolResults := <-a.executor.Results():
            // Tool calls completed — feed results back
            a.feedResults(toolResults)

        case <-a.stopCh:
            return // explicit stop signal
        }
    }
}
```

### 1.4 Concurrency Guards

```go
// Parallel tool execution with concurrency limit
type ToolExecutor struct {
    maxParallel int          // max concurrent tool calls
    semaphore   chan struct{} // bounded channel
}

func (e *ToolExecutor) Execute(ctx context.Context, calls []ToolCall) []ToolResult {
    if e.maxParallel == 0 {
        e.maxParallel = 4 // default
    }

    e.semaphore = make(chan struct{}, e.maxParallel)
    results := make([]ToolResult, len(calls))
    var wg sync.WaitGroup

    for i, call := range calls {
        wg.Add(1)
        go func(idx int, c ToolCall) {
            defer wg.Done()

            // Bounded concurrency
            select {
            case e.semaphore <- struct{}{}:
                defer func() { <-e.semaphore }()
            case <-ctx.Done():
                results[idx] = ToolResult{IsError: true, Content: ctx.Err().Error()}
                return
            }

            // Execute tool
            result, err := e.executeTool(ctx, c)
            if err != nil {
                results[idx] = ToolResult{ToolCallID: c.ID, IsError: true, Content: err.Error()}
                return
            }
            results[idx] = *result
        }(i, call)
    }

    wg.Wait()
    return results
}
```

### 1.5 Channel Buffering Strategy

| Channel | Buffer Size | Rationale |
|---------|------------|-----------|
| Steering queue | 16 | User can send a few follow-ups during execution |
| Follow-up queue | 32 | User can queue multiple follow-ups |
| LLM stream events | 64 | Smooth streaming without blocking |
| Tool results | 8 | Usually small number of parallel tools |
| Agent events | 128 | Event bus, can handle bursts |
| Cron events | 0 | Cron events are rare, no buffering needed |
| Adapter messages | 1024 | Platform messages can burst during peak |
| Thought queue events | 64 | Thoughts being enqueued/dequeued |
| Worker signals | 0 | Worker pool signals (no buffering needed) |

### Requirement: Managed Goroutine Lifecycle
The system SHALL manage all long-lived goroutines through a `ManagedGoroutine` pattern that provides context-driven cancellation, explicit start/stop lifecycle, and leak-free termination.

#### Scenario: Goroutine Lifecycle
- GIVEN a long-running task (e.g., adapter listener, cron scheduler, agent loop)
- WHEN the goroutine is started via `ManagedGoroutine.Start()`
- THEN the goroutine SHALL accept a `context.Context` and SHALL respect `ctx.Done()` for cancellation
- AND the goroutine SHALL have a clear termination condition signalled via a `done` channel
- AND `Stop()` SHALL cancel the context and block until the goroutine exits

### Requirement: Worker Pool Pattern for Daemon Navis
The system SHALL use a worker pool pattern for daemon navi thought processing, where workers dequeue thoughts from a shared queue using a select-based event loop.

#### Scenario: Worker Thought Processing
- GIVEN a daemon navi with multiple workers
- WHEN a thought is enqueued on the shared queue
- THEN a worker SHALL dequeue it and process it via a per-thought agent loop
- AND the worker SHALL wait on `ThoughtAvailable()` when no thoughts are queued
- AND the worker SHALL exit when the context is cancelled

### Requirement: Agent Loop Event-Driven Design
The system SHALL implement the agent loop as a select-based event loop that handles steering messages, LLM stream events, tool execution results, and stop signals concurrently.

#### Scenario: Agent Event Loop
- GIVEN an agent running a session
- WHEN the agent loop is active
- THEN it SHALL select on `ctx.Done()`, `steeringQueue`, `provider.Events()`, `executor.Results()`, and `stopCh`
- AND each event type SHALL be processed in its dedicated case branch

### Requirement: Concurrency Guards for Tool Execution
The system SHALL limit parallel tool execution through a bounded semaphore channel pattern.

#### Scenario: Parallel Tool Execution
- GIVEN a set of tool calls to execute in parallel
- WHEN the tool executor dispatches them
- THEN it SHALL use a bounded channel as a semaphore to limit concurrency to `maxParallel` (default 4)
- AND it SHALL respect context cancellation during semaphore acquisition
- AND it SHALL collect all results via `sync.WaitGroup`

---


## 01 Error Handling Strategy

### 2.1 Error Types

```go
// Error taxonomy for structured error handling
type ErrorKind string
const (
    ErrKindProvider  ErrorKind = "provider"   // LLM API error
    ErrKindTool      ErrorKind = "tool"       // Tool execution error
    ErrKindSession   ErrorKind = "session"    // Session I/O error
    ErrKindConfig    ErrorKind = "config"     // Config loading/validation error
    ErrKindPlugin    ErrorKind = "plugin"     // Plugin error
    ErrKindGateway   ErrorKind = "gateway"    // Gateway routing error
    ErrKindAuth      ErrorKind = "auth"       // Authorization error
    ErrKindNetwork   ErrorKind = "network"    // Network/HTTP error
    ErrKindInternal  ErrorKind = "internal"   // Internal bug
)

type OpenNaviError struct {
    Kind     ErrorKind
    Message  string
    Code     string       // machine-readable error code
    Source   error        // wrapped original error
    Recoverable bool     // can the caller retry?
    Context  map[string]any // additional context
}

func (e *OpenNaviError) Error() string {
    return fmt.Sprintf("[%s] %s: %v", e.Kind, e.Message, e.Source)
}

func (e *OpenNaviError) Unwrap() error {
    return e.Source
}

func (e *OpenNaviError) Is(target error) bool {
    te, ok := target.(*OpenNaviError)
    return ok && e.Kind == te.Kind
}
```

### 2.2 Error Propagation Rules

1. **Wrap, don't discard** — always wrap original errors with `fmt.Errorf("context: %w", err)`
2. **Add context at layer boundaries** — LLM layer adds provider name, gateway adds platform name
3. **Return typed errors** — callers can switch on `Kind` for error-specific handling
4. **Recoverable flag** — tells the caller whether retrying makes sense
5. **Never panic in production** — recover panics and convert to `ErrKindInternal`

### 2.3 Retry Strategy

```go
type RetryConfig struct {
    MaxRetries    int
    InitialDelay  time.Duration
    MaxDelay      time.Duration
    BackoffFactor float64
    RetryableKinds []ErrorKind
}

var DefaultRetryConfig = RetryConfig{
    MaxRetries:    3,
    InitialDelay:  500 * time.Millisecond,
    MaxDelay:      10 * time.Second,
    BackoffFactor: 2.0,
    RetryableKinds: []ErrorKind{ErrKindProvider, ErrKindNetwork},
}

func (c *RetryConfig) ShouldRetry(err error) bool {
    if c.MaxRetries == 0 {
        return false
    }

    var openNaviErr *OpenNaviError
    if !errors.As(err, &openNaviErr) {
        return false
    }

    for _, kind := range c.RetryableKinds {
        if openNaviErr.Kind == kind && openNaviErr.Recoverable {
            return true
        }
    }
    return false
}

func (c *RetryConfig) Delay(attempt int) time.Duration {
    delay := float64(c.InitialDelay) * math.Pow(c.BackoffFactor, float64(attempt))
    if delay > float64(c.MaxDelay) {
        delay = float64(c.MaxDelay)
    }
    // Add jitter
    jitter := time.Duration(rand.Int63n(int64(delay) / 2))
    return time.Duration(delay) + jitter
}
```

### 2.4 Error Handling by Layer

| Layer | Strategy |
|-------|----------|
| **LLM Provider** | Retry with backoff for transient errors; return typed errors for auth/model-not-found |
| **Agent Loop** | On unrecoverable error, deliver error message to user, set state to `StatusError` |
| **Gateway** | On adapter error, log and continue (don't crash the gateway); on session error, create new session |
| **Plugin** | On plugin crash, restart (standalone) or disable (in-process); log the error |
| **Config** | On load error, use defaults + log warning; never crash on config error |
| **Session** | On I/O error, try next storage backend; if all fail, log and continue in-memory |

### Requirement: Structured Error Taxonomy
The system SHALL define a typed error hierarchy with `ErrorKind`, machine-readable error codes, a recoverable flag, and wrapped original errors.

#### Scenario: Error Creation
- GIVEN an error condition in any layer
- WHEN the error is created
- THEN it SHALL include a `Kind` from the defined taxonomy (provider, tool, session, config, plugin, gateway, auth, network, internal)
- AND it SHALL include a machine-readable `Code` string
- AND it SHALL set `Recoverable` to indicate whether retrying is appropriate
- AND it SHALL wrap the original error with `fmt.Errorf("...: %w", err)`

### Requirement: Error Propagation with Context
The system SHALL wrap errors with contextual information at each layer boundary, never discarding the original error.

#### Scenario: Error Propagation
- GIVEN an error propagating from a lower layer to a higher layer
- WHEN the error crosses a layer boundary
- THEN the higher layer SHALL wrap it with `fmt.Errorf("context: %w", err)` adding layer-specific context
- AND callers SHALL be able to unwrap and switch on `Kind` for error-specific handling
- AND the system SHALL never panic in production; panics SHALL be recovered and converted to `ErrKindInternal`

### Requirement: Retry with Backoff
The system SHALL implement a configurable retry strategy with exponential backoff and jitter for recoverable errors.

#### Scenario: Retry on Recoverable Error
- GIVEN a recoverable error (e.g., transient provider or network error)
- WHEN the retry configuration permits retries
- THEN the system SHALL retry up to `MaxRetries` times (default 3)
- AND each retry SHALL use exponential backoff (factor 2.0) with jitter, capped at `MaxDelay` (default 10s)
- AND the system SHALL NOT retry non-recoverable errors or errors of kinds not in `RetryableKinds`

---


## 02 Logging and Observability

### 3.1 Structured Logging

```go
// Use a structured logger (zap or slog) with consistent fields
type Logger struct {
    inner    *slog.Logger
    profile  string
    session  string
    agentID  string
}

func (l *Logger) Info(msg string, args ...any) {
    l.inner.Info(msg, append([]any{
        "profile", l.profile,
        "session", l.session,
        "agent", l.agentID,
    }, args...)...)
}

func (l *Logger) Error(msg string, err error, args ...any) {
    l.inner.Error(msg, append([]any{
        "profile", l.profile,
        "session", l.session,
        "agent", l.agentID,
        "error", err,
    }, args...)...)
}

func (l *Logger) WithSession(sessionID string) *Logger {
    next := *l
    next.session = sessionID
    return &next
}

func (l *Logger) WithAgent(agentID string) *Logger {
    next := *l
    next.agentID = agentID
    return &next
}
```

### 3.2 Log Levels by Context

| Level | When to Use | Examples |
|-------|-------------|----------|
| **DEBUG** | Token counts, tool inputs/outputs, plugin lifecycle | "tool_call: bash args={cmd: ls -la}", "token_usage: input=1234 output=567" |
| **INFO** | Session start/end, plugin load/unload, cron triggers | "session_created: id=abc123", "plugin_loaded: name=fileops", "cron_triggered: job=morning-briefing" |
| **WARN** | Rate limiting, config fallbacks, plugin errors | "config_fallback: using default model", "plugin_error: name=broken-plugin", "rate_limit: user=alice" |
| **ERROR** | Unrecoverable errors, adapter failures, auth failures | "adapter_error: platform=telegram error=bot_not_found", "auth_failed: user=bob platform=discord" |

### 3.3 Request Tracing

```go
// Trace IDs for correlating events across layers
type Tracer struct {
    spanID  string
    traceID string
    logger  *Logger
}

func (t *Tracer) Start(operation string) *Tracer {
    spanID := uuid.New().String()[:8]
    traceID := uuid.New().String()[:8]
    t.logger.Info("trace_start", "op", operation, "span", spanID, "trace", traceID)
    return &Tracer{spanID: spanID, traceID: traceID, logger: t.logger}
}

func (t *Tracer) End(err error) {
    if err != nil {
        t.logger.Error("trace_error", "error", err, "span", t.spanID, "trace", t.traceID)
    } else {
        t.logger.Info("trace_end", "span", t.spanID, "trace", t.traceID)
    }
}

// Usage in gateway
func (g *GatewayRunner) handleMessage(raw RawMessage) {
    tracer := g.tracer.Start("handle_message")
    defer tracer.End(nil)

    msg, err := g.router.Parse(raw)
    if err != nil {
        tracer.End(err)
        return
    }

    // All downstream operations carry the trace ID
    tracer.WithField("trace", tracer.traceID).Info("message_parsed", "from", msg.From)
}
```

### 3.4 Metrics

```go
type Metrics struct {
    // Agent metrics
    sessionsCreated   prometheus.Counter
    sessionsActive    prometheus.Gauge
    agentRuns         prometheus.Counter
    agentErrors       prometheus.Counter
    avgTurnDuration   prometheus.Histogram
    avgTokenUsage     prometheus.Histogram

    // Provider metrics
    providerCalls     map[string]prometheus.Counter
    providerErrors    map[string]prometheus.Counter
    providerLatency   map[string]prometheus.Histogram

    // Plugin metrics
    pluginsLoaded     prometheus.Gauge
    pluginsErrors     prometheus.Counter

    // Gateway metrics
    messagesReceived  prometheus.Counter
    messagesDropped   prometheus.Counter
    activeAdapters    prometheus.Gauge

    // Memory metrics
    memoryEntries     prometheus.Gauge
    memorySizeBytes   prometheus.Gauge
}

func NewMetrics() *Metrics {
    return &Metrics{
        sessionsCreated: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "opennavi_sessions_created_total",
            Help: "Total sessions created",
        }),
        sessionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
            Name: "opennavi_sessions_active",
            Help: "Currently active sessions",
        }),
        // ... more metrics
    }
}
```

### 3.5 Log Output

```yaml
# ~/.opennavi/profiles/<name>/logging.yaml
logging:
  # Global log level
  level: "info"  # debug, info, warn, error

  # Output destinations
  outputs:
    - type: "stdout"    # always enabled
    - type: "file"
      path: "~/.opennavi/logs/opennavi.log"
      max_size: "100MB"
      max_age: "7d"     # rotate after 7 days
      compress: true

  # Per-component levels
  component_levels:
    gateway: "info"
    agent: "debug"     # more verbose for agent debugging
    plugin: "warn"     # only warnings/errors for plugins
    provider: "info"
    session: "info"

  # Structured format
  format: "json"       # json or text
  timestamp: true
  caller: false        # don't include file/line (too noisy)
```

### Requirement: Structured Logging with Context Fields
The system SHALL log all events in a structured format (JSON) with consistent context fields including profile, session, and agent identifiers.

#### Scenario: Structured Log Entry
- GIVEN any log event in the system
- WHEN the logger writes the entry
- THEN it SHALL include the fields `profile`, `session`, and `agent` in every log line
- AND the logger SHALL support per-session and per-agent derived instances via `WithSession()` and `WithAgent()`

### Requirement: Log Level Classification
The system SHALL classify log entries into four levels (DEBUG, INFO, WARN, ERROR) with defined criteria for each level.

#### Scenario: Log Level Assignment
- GIVEN a log event
- WHEN the system determines the log level
- THEN DEBUG SHALL be used for token counts, tool inputs/outputs, and plugin lifecycle events
- AND INFO SHALL be used for session start/end, plugin load/unload, and cron triggers
- AND WARN SHALL be used for rate limiting, config fallbacks, and plugin errors
- AND ERROR SHALL be used for unrecoverable errors, adapter failures, and auth failures

### Requirement: Request Tracing
The system SHALL support request tracing with span and trace IDs to correlate events across layers.

#### Scenario: Trace Lifecycle
- GIVEN a request entering the system
- WHEN processing begins
- THEN a trace SHALL be started with a unique `traceID` and `spanID`
- AND all downstream operations SHALL carry the trace ID
- AND the trace SHALL log its start and end, including any error encountered

### Requirement: Metrics Collection
The system SHALL expose Prometheus metrics for agent sessions, provider calls, plugin health, gateway messages, and memory usage.

#### Scenario: Metrics Registration
- GIVEN a running system
- WHEN the system initializes
- THEN counters SHALL be registered for sessions created, agent runs, agent errors, provider calls, and messages received
- AND gauges SHALL be registered for active sessions, plugins loaded, and active adapters
- AND histograms SHALL be registered for turn duration, token usage, and provider latency

---


## 03 Configuration Validation

### 4.1 Validation at Load Time

```go
type ConfigValidator struct{}

func (v *ConfigValidator) ValidateAgentConfig(cfg *AgentConfig) error {
    var errs []error

    // Required fields
    if cfg.Name == "" {
        errs = append(errs, fmt.Errorf("agent name is required"))
    }
    if cfg.Type != AgentPrimary && cfg.Type != AgentSub {
        errs = append(errs, fmt.Errorf("invalid agent type: %s", cfg.Type))
    }
    if cfg.Provider == "" {
        errs = append(errs, fmt.Errorf("provider is required"))
    }
    if cfg.Model == "" {
        errs = append(errs, fmt.Errorf("model is required"))
    }

    // Provider-specific validation
    switch cfg.Provider {
    case "anthropic":
        if cfg.Endpoint != "" {
            errs = append(errs, fmt.Errorf("Anthropic does not support custom endpoints"))
        }
        if !strings.HasPrefix(cfg.Model, "claude-") {
            errs = append(errs, fmt.Errorf("Anthropic model must start with 'claude-'"))
        }

    case "openrouter":
        if cfg.Endpoint != "" {
            if !isValidURL(cfg.Endpoint) {
                errs = append(errs, fmt.Errorf("invalid OpenRouter endpoint URL"))
            }
        }

    case "lmstudio":
        if cfg.Endpoint == "" {
            cfg.Endpoint = "http://localhost:1234/v1" // default
        }

    case "ollama":
        if cfg.Endpoint == "" {
            cfg.Endpoint = "http://localhost:11434/v1" // default
        }

    default:
        errs = append(errs, fmt.Errorf("unknown provider: %s", cfg.Provider))
    }

    // Temperature range
    if cfg.Temperature < 0 || cfg.Temperature > 2 {
        errs = append(errs, fmt.Errorf("temperature must be between 0 and 2, got %f", cfg.Temperature))
    }

    // Max tokens positive
    if cfg.MaxTokens <= 0 {
        errs = append(errs, fmt.Errorf("max_tokens must be positive"))
    }

    if len(errs) > 0 {
        return fmt.Errorf("agent config validation failed: %w", joinErrors(errs))
    }

    return nil
}
```

### 4.2 Config Defaults

```go
func DefaultAgentConfig() *AgentConfig {
    return &AgentConfig{
        Type:        AgentPrimary,
        Provider:    "anthropic",
        Model:       "claude-sonnet-4-20250514",
        Temperature: 0.7,
        MaxTokens:   8192,
        Thinking: ThinkingConfig{
            Enabled: true,
            Budget:  1024,
        },
        SessionMode: SessionNew,
    }
}

func (c *AgentConfig) ApplyDefaults() {
    if c.Type == "" {
        c.Type = AgentPrimary
    }
    if c.Provider == "" {
        c.Provider = "anthropic"
    }
    if c.Model == "" {
        c.Model = DefaultAgentConfig().Model
    }
    if c.Temperature == 0 {
        c.Temperature = 0.7
    }
    if c.MaxTokens == 0 {
        c.MaxTokens = 8192
    }
    if c.SessionMode == "" {
        c.SessionMode = SessionNew
    }
}
```

### 4.3 Config Hot-Reload

```go
// Watch for config changes and apply them without restart
func (c *ConfigManager) Watch(ctx context.Context) {
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        log.Printf("config watcher error: %v", err)
        return
    }
    defer watcher.Close()

    // Watch config files
    for _, path := range c.watchPaths() {
        if err := watcher.Add(path); err != nil {
            log.Printf("config watch error for %s: %v", path, err)
        }
    }

    for {
        select {
        case <-ctx.Done():
            return
        case event, ok := <-watcher.Events:
            if !ok {
                return
            }
            if event.Op&fsnotify.Write == fsnotify.Write {
                c.mu.Lock()
                if err := c.reload(); err != nil {
                    log.Printf("config reload error: %v", err)
                } else {
                    c.emit(SettingsChangedEvent{})
                }
                c.mu.Unlock()
            }
        case err, ok := <-watcher.Errors:
            if !ok {
                return
            }
            log.Printf("config watch error: %v", err)
        }
    }
}
```

---


---

## Related


### Other Parts

- [Testing, Security & API Versioning](./02-testing-security-versioning.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |