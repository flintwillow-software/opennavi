---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - agent-runtime
priority: high
spec-refs: [010400, 010401, 010402, 010403, 010404, 010405, 010406, 010407]
---

# Plan: Tool Error Recovery During Partial Streaming

**Scope:** Handling partial tool outputs (crashes mid-output), output truncation communicated to the LLM, parallel tool failure strategies, and recovery from inconsistent state. Research-informed by Hermes Agent patterns and production agent loop best practices.

---

## 00 Overview

When a tool call fails or returns unexpected output during execution, the system must:
- Detect partial outputs (tool crashes mid-stream)
- Handle truncation gracefully (communicate to LLM that output was truncated)
- Manage parallel tool failures (some succeed, some fail)
- Recover from inconsistent state (tool called but result not received)

This doc defines the recovery strategies for each scenario.

### Requirement: Tool Error Recovery Scope

The system SHALL detect and handle four categories of tool failures: partial outputs from crashes mid-execution, truncation from timeouts, malformed returns, and inconsistent state from lost results.

#### Scenario: Failure detection

GIVEN a tool execution that encounters an error
WHEN the tool returns
THEN the system SHALL classify the failure as a crash, timeout, malformed output, or network error.

### Requirement: Recovery Strategy Coverage

The system SHALL provide recovery strategies for each failure scenario: communicating truncation to the LLM, handling parallel tool failures where some succeed and others fail, and recovering from inconsistent state where a tool was called but the result was not received.

#### Scenario: Recovery application

GIVEN a tool execution failure
WHEN the system applies the recovery strategy
THEN the system SHALL communicate the failure to the LLM, handle partial results, and allow the LLM to decide whether to retry or continue.

---

## 01 Partial Tool Output Detection

### 2.1 What Constitutes a "Partial" Output?

A partial output occurs when:
1. **Tool crashes mid-execution** — Process exits unexpectedly (panic, segfault, OOM)
2. **Tool returns malformed JSON** — Expected structured output but got garbage
3. **Tool times out** — Execution exceeded timeout but didn't return a clean error
4. **Network failure during streaming** — Tool was streaming results when connection dropped

### 2.2 Detection Mechanisms

```go
type ToolExecutionResult struct {
    ToolCallID string
    Name       string
    Content    string
    IsError    bool
    Partial    bool        // true if output was truncated or incomplete
    TruncationReason string  // why it was partial: "timeout", "crash", "network_error"
    Duration   time.Duration
}

// Detect partial outputs based on tool behavior
func detectPartialOutput(result ToolExecutionResult, tool AgentTool) (partial bool, reason string) {
    // Check for explicit timeout marker from executor
    if result.Duration > tool.Timeout && !result.IsError {
        return true, "timeout"
    }
    
    // Check for crash indicators in content
    if strings.Contains(result.Content, "[tool crashed]") || 
       strings.Contains(result.Content, "PANIC:") ||
       strings.Contains(result.Content, "segfault") {
        return true, "crash"
    }
    
    // Check for network error markers
    if strings.Contains(result.Content, "[connection lost]") ||
       strings.Contains(result.Content, "ECONNRESET") {
        return true, "network_error"
    }
    
    // Check for malformed JSON (if tool is expected to return structured output)
    if tool.ExpectsJSON && !isValidJSON(result.Content) {
        return true, "malformed_output"
    }
    
    return false, ""
}

// Validate that content looks like complete JSON
func isValidJSON(content string) bool {
    var js json.RawMessage
    return json.Unmarshal([]byte(content), &js) == nil
}

// Check if tool output looks truncated (common patterns)
func isTruncatedOutput(content string, expectedLength int) bool {
    // Content ends with ellipsis or truncation marker
    if strings.HasSuffix(strings.TrimSpace(content), "...") ||
       strings.HasSuffix(strings.TrimSpace(content), "[truncated]") {
        return true
    }
    
    // Content seems cut off mid-sentence (no closing punctuation)
    trimmed := strings.TrimRight(content, " \n\t")
    if len(trimmed) > 0 {
        lastChar := rune(trimmed[len(trimmed)-1])
        if !unicode.IsPunct(lastChar) && !unicode.IsLetter(lastChar) {
            // Ends with whitespace or special char — might be truncated
            return true
        }
    }
    
    // Content length significantly less than expected (heuristic)
    if expectedLength > 0 && len(content) < expectedLength*0.5 {
        return true
    }
    
    return false
}
```

### 2.3 Timeout Handling with Clean Shutdown

When a tool times out, we want to:
1. Send an interrupt signal to the tool process (SIGTERM)
2. Wait briefly for cleanup
3. Return a clean error message indicating timeout

```go
func executeToolWithTimeout(ctx context.Context, tool AgentTool, args map[string]any) (*ToolExecutionResult, error) {
    // Set up context with timeout
    ctx, cancel := context.WithTimeout(ctx, tool.Timeout)
    defer cancel()
    
    // Execute tool in goroutine
    resultCh := make(chan ToolExecutionResult, 1)
    errCh := make(chan error, 1)
    
    go func() {
        content, err := tool.Execute(args)
        if err != nil {
            errCh <- err
            return
        }
        
        resultCh <- ToolExecutionResult{
            Content: content,
            IsError: false,
            Duration: time.Since(startTime),
        }
    }()
    
    select {
    case result := <-resultCh:
        // Tool completed normally
        partial, reason := detectPartialOutput(result, tool)
        if partial {
            return &ToolExecutionResult{
                Content:          result.Content,
                IsError:          true,
                Partial:          true,
                TruncationReason: reason,
                Duration:         result.Duration,
            }, nil
        }
        
        return &result, nil
    
    case err := <-errCh:
        // Tool returned an error (crash, panic, etc.)
        return &ToolExecutionResult{
            Content:          fmt.Sprintf("[tool %s failed] %v", tool.Name, err),
            IsError:          true,
            Partial:          true,
            TruncationReason: "error",
        }, nil
    
    case <-ctx.Done():
        // Timeout — tool didn't complete in time
        return &ToolExecutionResult{
            Content:          fmt.Sprintf("[tool %s timed out after %v]", tool.Name, tool.Timeout),
            IsError:          true,
            Partial:          true,
            TruncationReason: "timeout",
            Duration:         tool.Timeout,
        }, nil
    }
}
```

### Requirement: Partial Output Detection Mechanisms

The system SHALL detect partial tool outputs by checking for tool crashes, malformed JSON, timeouts, and network failures during streaming, using a structured ToolExecutionResult that includes IsError, Partial, and TruncationReason fields.

#### Scenario: Timeout detection

GIVEN a tool execution that exceeds its configured timeout
WHEN the tool does not complete
THEN the system SHALL mark the result as partial with truncation reason "timeout" and return a clean error message.

#### Scenario: Crash detection

GIVEN a tool that crashes mid-execution
WHEN the system inspects the output
THEN the system SHALL detect crash indicators (panic, segfault) and mark the result as partial.

### Requirement: Timeout Handling with Clean Shutdown

The system SHALL handle tool timeouts by sending an interrupt signal to the tool process, waiting briefly for cleanup, and returning a clean error message indicating the timeout duration.

#### Scenario: Timeout execution

GIVEN a tool call with a configured timeout
WHEN the tool exceeds the timeout
THEN the system SHALL cancel the context, return a ToolExecutionResult with IsError true, Partial true, and TruncationReason set to "timeout".

### Requirement: Truncated Output Heuristics

The system SHALL detect truncated tool output by checking for truncation markers at the end of content, lack of closing punctuation, and content length significantly less than expected.

#### Scenario: Truncation marker detection

GIVEN a tool output ending with "..." or "[truncated]"
WHEN the system checks for truncation
THEN the system SHALL mark the result as partial.

---

## 02 Communicating Truncation to the LLM

### 3.1 Why Communication Matters

When a tool output is truncated or partial, the LLM needs to know — otherwise it might assume the output is complete and make decisions based on incomplete information. This prevents:
- The LLM acting on half a file listing (missed files)
- The LLM thinking a command succeeded when it only partially ran
- The LLM wasting tokens trying to "fix" something that was already truncated

### 3.2 Truncation Message Format

```go
// Append truncation notice to tool output before returning to agent
func appendTruncationNotice(content string, reason string) string {
    notice := fmt.Sprintf(
        "\n\n[TRUNCATED: %s — output was cut off or incomplete. The LLM should not assume this is the full result.]",
        reason,
    )
    
    return content + notice
}

// Example tool outputs with truncation notices:
func exampleToolOutputs() {
    // Timeout scenario
    timeoutOutput := "[tool bash timed out after 30s]\n[TRUNCATED: timeout — output was cut off or incomplete...]"
    
    // Crash scenario  
    crashOutput := "Processing files...\n[1/5] file1.txt\n[2/5] file2.txt\n...\n[TRUNCATED: crash — process exited unexpectedly]"
    
    // Network error during streaming
    networkOutput := "Connecting to database...\nQuery started...\n[TRUNCATED: network_error — connection lost before query completed]"
}

// Format the tool result for the agent loop
func formatToolResult(result *ToolExecutionResult, tool AgentTool) string {
    var sb strings.Builder
    
    if result.IsError {
        sb.WriteString(fmt.Sprintf("### Tool `%s` returned an error:\n", tool.Name))
        
        if result.Partial {
            sb.WriteString(fmt.Sprintf("**Note:** This output was %s.\n\n", result.TruncationReason))
        }
    } else {
        sb.WriteString(fmt.Sprintf("### Tool `%s` executed successfully:\n", tool.Name))
    }
    
    // Truncate content if it exceeds max output bytes (configurable)
    maxOutputBytes := 1024 * 1024 // 1MB default
    
    if len(result.Content) > maxOutputBytes {
        truncatedContent := result.Content[:maxOutputBytes] + "\n... [tool output truncated — exceeded size limit]"
        
        sb.WriteString("```")
        if tool.OutputFormat != "" {
            sb.WriteString(tool.OutputFormat) // e.g., "json", "bash" for syntax highlighting
        }
        sb.WriteString("\n")
        sb.WriteString(truncatedContent)
        sb.WriteString("\n```\n\n[TRUNCATED: output exceeded size limit]\n")
    } else {
        sb.WriteString("```")
        if tool.OutputFormat != "" {
            sb.WriteString(tool.OutputFormat)
        }
        sb.WriteString("\n")
        sb.WriteString(result.Content)
        sb.WriteString("\n```\n")
    }
    
    return sb.String()
}
```

### 3.3 System Prompt Guidance for Truncated Outputs

Add guidance to the system prompt about how to handle truncated tool outputs:

```go
// Inject truncation handling guidance into system prompt
func addTruncationGuidance(systemPrompt string) string {
    guidance := `
## Handling Tool Output

When a tool returns output that is marked as [TRUNCATED] or indicates partial execution:

1. **Do NOT assume the output is complete.** The tool may have been cut off mid-execution.
2. **Ask for clarification** if you need more information: "The previous output was truncated — can you provide the full results?"
3. **Retry with smaller scope** if appropriate: If a file listing was truncated, ask for a specific directory instead of the whole tree.
4. **Note the truncation reason** in your response so the user understands what happened (timeout, crash, network error, size limit).

Never fabricate or guess at missing output — always request it explicitly if needed.`

    return systemPrompt + guidance
}
```

### Requirement: Truncation Notice in Tool Output

The system SHALL append a truncation notice to tool outputs that are partial or incomplete, clearly indicating that the output was cut off and that the LLM should not assume the full result is present.

#### Scenario: Truncation notice formatting

GIVEN a partial tool output
WHEN the system formats the result for the LLM
THEN the system SHALL append a [TRUNCATED: reason] marker with the truncation reason so the LLM knows the output is incomplete.

### Requirement: System Prompt Guidance for Truncated Outputs

The system SHALL inject guidance into the system prompt instructing the LLM not to assume truncated output is complete, to ask for clarification when needed, and to retry with smaller scope when appropriate.

#### Scenario: Truncation guidance injection

GIVEN an agent with a system prompt
WHEN the system starts the agent
THEN the system SHALL append truncation handling guidance directing the LLM not to guess at missing output and to request clarification.

### Requirement: Tool Result Formatting with Size Limits

The system SHALL format tool results for the LLM with a configurable maximum output size, truncating oversized content with a clear truncation marker and including optional syntax highlighting per tool output format.

#### Scenario: Size limit truncation

GIVEN a tool output exceeding the maximum output size
WHEN the system formats the result
THEN the system SHALL truncate the content at the limit and append a [TRUNCATED: output exceeded size limit] notice.

---

## 03 Parallel Tool Failure Strategies

### 4.1 Scenario: Some Tools Succeed, Others Fail

When the agent calls multiple tools in parallel (e.g., `file_read` on file A, `query_grafana`, `shell_exec ls /tmp`), some may succeed while others fail. The system must:
- Return successful results to the LLM immediately
- Include error/partial information for failed tools
- Allow the LLM to decide whether to retry or continue

```go
type ParallelToolExecution struct {
    calls     []ToolCall
    executor  *ToolExecutor
    timeout   time.Duration
}

// Execute multiple tool calls in parallel, collecting all results
func (p *ParallelToolExecution) Execute(ctx context.Context) ([]ToolExecutionResult, error) {
    if p.executor == nil {
        return nil, fmt.Errorf("no executor configured")
    }
    
    // Use bounded concurrency via semaphore (from 03-agent-runtime.md)
    results := make([]ToolExecutionResult, len(p.calls))
    var wg sync.WaitGroup
    
    for i, call := range p.calls {
        wg.Add(1)
        
        go func(idx int, c ToolCall) {
            defer wg.Done()
            
            // Execute tool with timeout
            result, err := executeToolWithTimeout(ctx, c.Tool, c.Input)
            
            if err != nil {
                results[idx] = ToolExecutionResult{
                    ToolCallID: c.ID,
                    Name:       c.Name,
                    Content:    fmt.Sprintf("[tool %s failed: %v]", c.Name, err),
                    IsError:    true,
                    Partial:    false, // Failure is complete — not partial
                }
            } else {
                results[idx] = *result
            }
        }(i, call)
    }
    
    wg.Wait()
    
    return results, nil
}

// Process parallel tool results for the agent loop
func processParallelResults(results []ToolExecutionResult, agent *Agent) {
    var successfulResults []string
    var errorMessages []string
    
    for _, result := range results {
        if result.IsError {
            // Add error message to session history for LLM visibility
            errorMessages = append(errorMessages, formatToolResult(&result, AgentTool{Name: result.Name}))
            
            log.Warnf("Parallel tool %s failed: %v", result.Name, result.Content)
        } else {
            successfulResults = append(successfulResults, formatToolResult(&result, AgentTool{Name: result.Name}))
        }
    }
    
    // Feed all results back to agent (successes + errors)
    for _, msg := range successfulResults {
        agent.AppendMessage(AgentMessage{
            Role:       RoleTool,
            ToolResult: msg,
        })
    }
    
    for _, errMsg := range errorMessages {
        agent.AppendMessage(AgentMessage{
            Role:       RoleTool,
            ToolResult: errMsg,
            IsError:    true,
        })
    }
}

// Example: Agent calls 3 tools in parallel, one times out
func exampleParallelExecution() {
    // Agent wants to: list files, query metrics, check disk space
    calls := []ToolCall{
        {ID: "call_1", Name: "bash", Input: map[string]any{"cmd": "ls /tmp"}},
        {ID: "call_2", Name: "query_grafana", Input: map[string]any{"query": "rate(http_requests_total[5m])"}},
        {ID: "call_3", Name: "bash", Input: map[string]any{"cmd": "df -h /"}},
    }
    
    executor := &ParallelToolExecution{
        calls:     calls,
        executor:  defaultExecutor,
        timeout:   30 * time.Second,
    }
    
    results, err := executor.Execute(context.Background())
    if err != nil {
        log.Errorf("parallel execution failed: %v", err)
        return
    }
    
    // Process results — some may have timed out or crashed
    processParallelResults(results, agent)
}
```

### 4.2 Retry Logic for Failed Parallel Tools

When a tool fails in parallel execution, should we retry it? Generally **no** for the first failure — the LLM should see all results (success + error) and decide whether to retry based on context.

However, for certain tool types (network calls, API requests), a single retry with backoff may be appropriate:

```go
// Retry configuration for specific tool categories
var retryableToolCategories = map[string]bool{
    "api_call":      true,  // External API calls are often transiently failing
    "http_request":  true,  // HTTP requests can have network hiccups
    "database_query": false, // Database queries should fail loudly if broken
}

// Check if a tool failure is retryable based on its category
func isRetryableFailure(toolName string, result *ToolExecutionResult) bool {
    if !result.Partial && !result.IsError {
        return false // Not an error — no need to retry
    }
    
    // Check tool category for retry eligibility
    _, ok := retryableToolCategories[strings.ToLower(toolName)]
    if !ok {
        return false // Tool type doesn't support automatic retries
    }
    
    // Check if failure reason is transient (timeout, network error)
    switch result.TruncationReason {
    case "timeout", "network_error":
        return true // Transient — retry with backoff
    
    default:
        return false // Permanent error (crash, malformed output) — don't retry
    }
}

// Retry a failed parallel tool call with exponential backoff
func retryToolCall(ctx context.Context, call ToolCall, maxRetries int, initialDelay time.Duration) (*ToolExecutionResult, error) {
    var lastErr error
    
    for attempt := 0; attempt < maxRetries; attempt++ {
        // Calculate backoff delay with jitter
        delay := calculateBackoff(attempt, initialDelay)
        if attempt > 0 {
            time.Sleep(delay)
        }
        
        result, err := executeToolWithTimeout(ctx, call.Tool, call.Input)
        if err == nil {
            return result, nil // Success on this attempt
        }
        
        lastErr = err
        
        log.Warnf("Retry %d/%d for tool %s failed: %v", attempt+1, maxRetries, call.Name, err)
    }
    
    return nil, fmt.Errorf("tool %s failed after %d retries: %w", call.Name, maxRetries, lastErr)
}

func calculateBackoff(attempt int, initialDelay time.Duration) time.Duration {
    delay := float64(initialDelay) * math.Pow(2.0, float64(attempt))
    
    // Add jitter (±25%)
    jitter := time.Duration(rand.Int63n(int64(delay) / 4))
    
    return time.Duration(delay) + jitter
}

// Default retry configuration for parallel tool failures
var defaultRetryConfig = struct {
    MaxRetries   int
    InitialDelay time.Duration
}{
    MaxRetries:   2,       // Up to 2 retries (3 total attempts)
    InitialDelay: 500 * time.Millisecond,
}

// Example: Retry a failed query_grafana call in parallel execution
func exampleRetryLogic() {
    calls := []ToolCall{
        {ID: "call_1", Name: "query_grafana", Input: map[string]any{"query": "..."}},
        // ... other calls
    }
    
    executor := &ParallelToolExecution{
        calls:     calls,
        executor:  defaultExecutor,
        timeout:   30 * time.Second,
    }
    
    results, err := executor.Execute(context.Background())
    if err != nil {
        log.Errorf("execution failed: %v", err)
        return
    }
    
    // Check for retryable failures and retry them
    var retryCalls []ToolCall
    
    for i, result := range results {
        if isRetryableFailure(result.Name, &result) {
            retryCalls = append(retryCalls, calls[i])
        } else {
            // Non-retryable failure — include in final output as-is
            processParallelResults([]ToolExecutionResult{result}, agent)
        }
    }
    
    // Retry failed tools with backoff
    for _, call := range retryCalls {
        result, err := retryToolCall(ctx, call, defaultRetryConfig.MaxRetries, defaultRetryConfig.InitialDelay)
        if err != nil {
            log.Errorf("retry failed for tool %s: %v", call.Name, err)
            // Include final failure in output
            processParallelResults([]ToolExecutionResult{*result}, agent)
        } else {
            // Success on retry — include success result
            processParallelResults([]ToolExecutionResult{*result}, agent)
        }
    }
}
```

### Requirement: Parallel Tool Mixed Results

The system SHALL handle parallel tool execution where some tools succeed and others fail, returning successful results to the LLM while including error and partial information for failed tools, allowing the LLM to decide whether to retry or continue.

#### Scenario: Mixed result processing

GIVEN parallel tool calls where some succeed and some fail
WHEN all results are collected
THEN the system SHALL feed both successful results and error messages back to the agent state, marking failed results with IsError true.

### Requirement: No Automatic Retry Default

The system SHALL not automatically retry failed tools by default, instead reporting the failure to the LLM and letting it decide whether to retry based on context.

#### Scenario: First failure reporting

GIVEN a tool fails during parallel execution
WHEN the system processes the results
THEN the system SHALL present the error to the LLM without automatically retrying.

### Requirement: Retryable Tool Categories

The system SHALL support configurable retry for specific tool categories (API calls, HTTP requests) with exponential backoff and jitter, while excluding categories such as database queries from automatic retry.

#### Scenario: Retry with backoff

GIVEN a retryable tool failure (network call, API request)
WHEN retry is configured
THEN the system SHALL retry the tool with exponential backoff and jitter up to the configured max retries.

#### Scenario: Non-retryable failure

GIVEN a non-retryable tool failure (database query, local command)
WHEN the tool fails
THEN the system SHALL NOT retry the tool and SHALL report the final failure to the LLM.

---

## 04 Recovery from Inconsistent State

### 5.1 Scenario: Tool Called but Result Not Received

If the agent loop expects N tool results but only receives M < N (due to crash, network drop, etc.), we need to:
- Detect missing results
- Generate placeholder error messages for missing tools
- Allow the LLM to decide whether to retry or continue

```go
type ExpectedResultsTracker struct {
    expected map[string]bool // ToolCallID → received?
    mu       sync.RWMutex
}

func (t *ExpectedResultsTracker) MarkReceived(toolCallID string) {
    t.mu.Lock()
    defer t.mu.Unlock()
    
    if _, exists := t.expected[toolCallID]; !exists {
        log.Warnf("Result received for unexpected tool call: %s", toolCallID)
    } else {
        t.expected[toolCallID] = false // Mark as not yet received (will be set to true when result arrives)
    }
}

func (t *ExpectedResultsTracker) MarkComplete(toolCallID string, result ToolExecutionResult) {
    t.mu.Lock()
    defer t.mu.Unlock()
    
    t.expected[toolCallID] = false // Will be marked true in a separate step
    
    // Actually, let's use a different approach: track received IDs separately
}

// Better approach: track which calls have results
type ResultsTracker struct {
    expected []ToolCall
    received map[string]*ToolExecutionResult
    mu       sync.RWMutex
}

func NewResultsTracker(calls []ToolCall) *ResultsTracker {
    return &ResultsTracker{
        expected: calls,
        received: make(map[string]*ToolExecutionResult),
    }
}

// Add a result to the tracker
func (r *ResultsTracker) AddResult(callID string, result *ToolExecutionResult) {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    r.received[callID] = result
}

// Check for missing results
func (r *ResultsTracker) GetMissingCalls() []ToolCall {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    var missing []ToolCall
    
    for _, call := range r.expected {
        if _, exists := r.received[call.ID]; !exists {
            missing = append(missing, call)
        }
    }
    
    return missing
}

// Generate placeholder error messages for missing calls
func generateMissingResultMessages(missing []ToolCall) []AgentMessage {
    var messages []AgentMessage
    
    for _, call := range missing {
        msg := AgentMessage{
            Role:       RoleTool,
            ToolResult: fmt.Sprintf("[tool %s result missing — tool execution did not complete or result was lost]", call.Name),
            IsError:    true,
        }
        messages = append(messages, msg)
    }
    
    return messages
}

// Wait for all expected results with timeout
func (r *ResultsTracker) WaitForAll(ctx context.Context, timeout time.Duration) ([]ToolExecutionResult, []AgentMessage, error) {
    deadline := time.After(timeout)
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            // Context cancelled — return what we have plus missing placeholders
            missing := r.GetMissingCalls()
            results := make([]ToolExecutionResult, 0, len(r.expected))
            
            for _, call := range r.expected {
                if result, ok := r.received[call.ID]; ok {
                    results = append(results, *result)
                } else {
                    results = append(results, ToolExecutionResult{
                        ToolCallID: call.ID,
                        Name:       call.Name,
                        Content:    fmt.Sprintf("[tool %s result missing]", call.Name),
                        IsError:    true,
                    })
                }
            }
            
            return results, nil, ctx.Err()
            
        case <-deadline:
            // Timeout — return what we have plus missing placeholders
            missing := r.GetMissingCalls()
            placeholderMessages := generateMissingResultMessages(missing)
            
            results := make([]ToolExecutionResult, 0, len(r.expected))
            for _, call := range r.expected {
                if result, ok := r.received[call.ID]; ok {
                    results = append(results, *result)
                } else {
                    results = append(results, ToolExecutionResult{
                        ToolCallID: call.ID,
                        Name:       call.Name,
                        Content:    fmt.Sprintf("[tool %s result missing — timeout after %v]", call.Name, timeout),
                        IsError:    true,
                    })
                }
            }
            
            return results, placeholderMessages, nil
            
        case <-ticker.C:
            r.mu.RLock()
            allReceived := len(r.received) == len(r.expected)
            r.mu.RUnlock()
            
            if allReceived {
                // All results received — assemble in expected order
                results := make([]ToolExecutionResult, 0, len(r.expected))
                
                for _, call := range r.expected {
                    result, ok := r.received[call.ID]
                    if !ok {
                        log.Warnf("Expected result for %s not found in tracker", call.ID)
                        continue
                    }
                    
                    results = append(results, *result)
                }
                
                return results, nil, nil
            }
        }
    }
}

// Example: Wait for all parallel tool results with timeout
func exampleWaitForResults() {
    calls := []ToolCall{
        {ID: "call_1", Name: "read", Input: map[string]any{"path": "/etc/hosts"}},
        {ID: "call_2", Name: "bash", Input: map[string]any{"cmd": "uptime"}},
        {ID: "call_3", Name: "query_grafana", Input: map[string]any{"query": "..."}},
    }
    
    tracker := NewResultsTracker(calls)
    
    // Execute tools in parallel (results will be added to tracker as they complete)
    results, errChans := executeParallelTools(ctx, calls, 30*time.Second)
    
    for i, errChan := range errChans {
        go func(idx int, ch <-chan error) {
            if err := <-ch; err != nil {
                log.Warnf("Tool %s failed: %v", calls[idx].Name, err)
            } else {
                // Result added by the parallel execution goroutine
                tracker.AddResult(calls[idx].ID, results[idx])
            }
        }(i, errChan)
    }
    
    // Wait for all results with 30s timeout
    finalResults, missingMessages, waitErr := tracker.WaitForAll(ctx, 30*time.Second)
    
    if waitErr != nil {
        log.Warnf("WaitForAll returned: %v (some results may be missing)", waitErr)
    }
    
    // Feed all results + missing placeholders to agent
    for _, result := range finalResults {
        agent.AppendMessage(AgentMessage{
            Role:       RoleTool,
            ToolResult: formatToolResult(&result, AgentTool{Name: result.Name}),
            IsError:    result.IsError,
        })
    }
    
    // Add placeholder messages for missing tools
    for _, msg := range missingMessages {
        agent.AppendMessage(msg)
    }
}
```

### 5.2 State Consistency Checks After Tool Execution

After parallel tool execution completes (whether fully or with timeouts), run consistency checks:

```go
// Verify that all expected results are accounted for
func verifyToolExecutionConsistency(expected []ToolCall, received map[string]*ToolExecutionResult) error {
    r := NewResultsTracker(expected)
    
    // Add all received results to tracker
    for id, result := range received {
        r.AddResult(id, result)
    }
    
    // Check for missing calls
    missing := r.GetMissingCalls()
    if len(missing) > 0 {
        return fmt.Errorf(
            "incomplete tool execution: %d of %d tools did not return results — missing: %v",
            len(missing), len(expected),
            funcNames(missing),
        )
    }
    
    // Check for unexpected results (result received but no matching call)
    for id := range received {
        found := false
        for _, call := range expected {
            if call.ID == id {
                found = true
                break
            }
        }
        
        if !found {
            return fmt.Errorf("unexpected tool result: %s (no matching ToolCall found)", id)
        }
    }
    
    // Check for duplicate results (same ID received twice)
    seen := make(map[string]bool)
    for id := range received {
        if seen[id] {
            return fmt.Errorf("duplicate tool result: %s", id)
        }
        seen[id] = true
    }
    
    return nil // All checks passed
}

func funcNames(calls []ToolCall) []string {
    names := make([]string, len(calls))
    for i, call := range calls {
        names[i] = call.Name
    }
    return names
}
```

### Requirement: Missing Result Detection

The system SHALL track expected tool results and detect when fewer results are received than expected, generating placeholder error messages for missing tool calls so the agent loop does not hang waiting for results that will never arrive.

#### Scenario: Missing result placeholder

GIVEN a tool call that does not return a result
WHEN the system waits for all expected results
THEN the system SHALL generate a placeholder error message indicating which tool's result is missing and why the LLM should not assume completion.

### Requirement: Timeout-Bounded Result Collection

The system SHALL wait for all expected parallel tool results within a configurable timeout, returning whatever results are available plus placeholder messages for missing ones if the timeout expires.

#### Scenario: Result collection timeout

GIVEN parallel tool calls with a 30-second timeout
WHEN some tools do not complete within the timeout
THEN the system SHALL return the received results and generate placeholder messages for the missing tools.

### Requirement: Post-Execution Consistency Verification

The system SHALL run consistency checks after tool execution to verify that all expected results are accounted for, detect unexpected results, and detect duplicate results.

#### Scenario: Consistency check

GIVEN expected tool calls and received results
WHEN the system verifies consistency
THEN the system SHALL check that all expected calls have results, no unexpected results exist, and no duplicate results exist.

---

## 05 Hermes Agent Patterns (Research-Based)

### 6.1 Tool Error Handling in Hermes

Based on documented behavior of Hermes-style autonomous agents:

**Patterns observed:**
- **Tool result validation**: After each tool execution, validate that the output looks like a complete result (not truncated, not malformed JSON). If invalid, mark as partial and notify LLM.
- **No automatic retry for most tools**: Hermes generally doesn't retry failed tools — it reports the failure to the user/LLM and lets them decide whether to retry. This prevents infinite loops on broken tools.
- **Explicit timeout per tool category**: Different tool categories have different timeouts (API calls: 30s, shell commands: 60s, file reads: 10s). Shorter timeouts for interactive tools prevent the agent from being stuck waiting for human input.
- **Fallback messages for missing results**: When a tool result is lost, Hermes generates a clear error message indicating which tool failed and why (timeout, crash, etc.), rather than silently omitting it.

**Adaptation for OpenNavi:**
- Adopt Hermes' explicit timeout per tool category (configurable in navi definition)
- Use Hermes' "no automatic retry" policy as default — only enable retries for network-related tools via config
- Implement Hermes-style fallback messages that clearly indicate what went wrong and why the LLM shouldn't assume completion

### 6.2 Pi-Mono Tool Execution Patterns

**Patterns observed:**
- **Copy-on-write state**: Agent state is immutable; tool execution creates new state copies to avoid race conditions between parallel executions.
- **Event-driven result delivery**: Tool results are delivered via events (not direct returns) so the agent loop can handle them in order, even when tools complete out of order.
- **Steering queue integration**: If a user sends a steering message while tools are executing, the tool execution is paused (results queued) until the steering message is processed.

**Adaptation for OpenNavi:**
- Leverage existing copy-on-write state pattern from 03-agent-runtime.md
- Use event-driven result delivery (already partially implemented via ToolExecutor)
- Pause tool execution on steering messages (integrate with SteeringQueue)

### Requirement: Tool Result Validation

The system SHALL validate tool outputs after each execution to ensure they look like complete results, marking outputs as partial if truncated, malformed, or invalid, and communicating the issue to the LLM.

#### Scenario: Output validation

GIVEN a tool execution completes
WHEN the system validates the output
THEN the system SHALL check for completeness and validity, and mark as partial if the output is truncated or malformed.

### Requirement: No Silent Failures

The system SHALL always report tool failures clearly in the agent state, never silently omitting failed tools, so the LLM has full visibility into what went wrong.

#### Scenario: Failure reporting

GIVEN a tool execution failure
WHEN the system processes the result
THEN the system SHALL include a clear error message in the agent state specifying which tool failed and why.

### Requirement: Per-Tool-Category Timeout

The system SHALL support configurable timeouts per tool category, with shorter timeouts for interactive tools to prevent the agent from being stuck waiting for human input.

#### Scenario: Category timeout application

GIVEN a tool call with a category-specific timeout
WHEN the tool executes
THEN the system SHALL apply the configured timeout for that tool category.

---

## 06 Implementation Order

1. **Partial output detection** — Identify truncated/crashed/timeout tool outputs
2. **Timeout handling** — Clean shutdown of tools that exceed their timeout
3. **Truncation communication** — Append notices to partial outputs so LLM knows output is incomplete
4. **Parallel failure handling** — Process mixed success/failure results from parallel execution
5. **Retry logic** — Optional retry for transient failures (network, timeout) with backoff
6. **Missing result detection** — Track expected vs received results and generate placeholders for missing ones
7. **Consistency verification** — Post-execution checks to ensure all tools returned as expected

---

## 07 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Explicit truncation notices in tool output** — Append `[TRUNCATED: reason]` marker | LLM can't infer truncation from content alone; explicit markers prevent hallucination of missing data |
| **No automatic retry for most tools by default** — Only network-related tools get retries via config | Prevents infinite loops on broken tools; gives LLM visibility into failures to decide retry strategy |
| **Timeout per tool category** — API calls: 30s, shell: 60s, file reads: 10s | Different tools have different expected execution times; uniform timeout either kills fast tools too quickly or lets slow tools run forever |
| **Placeholder messages for missing results** — Generate error message when a tool call has no result | Prevents the agent loop from hanging waiting for N results that will never arrive; gives LLM clear information about what went wrong |
| **Consistency verification after execution** — Check expected vs received results post-execution | Catches race conditions and state inconsistencies before they propagate to the LLM or user |
| **Hermes-inspired "no silent failures"** — Always report tool failures clearly, never silently omit them | Aligns with production agent loop best practices; prevents confusing behavior when tools fail mid-execution |


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S010400 | 0.1.0 | Migrated from docs/spec/ to spec-tree |
| 2026-09-12 | S010400 | 0.2.1 | Renamed `read` → `file_read`, `bash` → `shell_exec` in parallel-failure example (namespaced tool names per S130400 §04) |