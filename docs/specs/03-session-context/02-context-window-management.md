---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - session-context
priority: medium
spec-refs: [030200, 030201, 030202, 030203, 030204, 030205, 030206, 030207]
---

# Plan: Context Window Management

**Scope:** Token budgeting, compaction strategy, overflow handling, and integration with session/memory systems. Research-informed from Claude Code, Hermes Agent, and pi-code patterns.

> **See also:** [P2P Chat & Presence](../04-multi-agent/00-p2p-chat-presence.md) for how chat messages between agents become thoughts in the recipient's queue. Each chat thought gets its own context window managed by the rules defined here — per-thread token budgeting, compaction, and overflow handling. The context window is per-thought, so P2P chat messages don't share context with the agent's other work.

---

## 00 Overview

Context window management is the system that controls how much of a conversation gets sent to the LLM at any given time. When conversations grow beyond the model's context limit (e.g., 128K tokens for GPT-4, 200K for Claude), the system must decide:
- What to keep in context
- What to summarize/compact
- What to drop entirely
- How to estimate token counts efficiently

### Requirement: Context Window Governed by Model Limits

The system SHALL control how much of a conversation is sent to the LLM at any given time, bounded by the model's context window limit, and decide what to keep, summarize, drop, and how to estimate token counts.

#### Scenario: Conversation within context limit

- GIVEN a conversation that fits within the model's context window
- WHEN the system builds the context for the LLM
- THEN the system SHALL include the full conversation without truncation or compaction

#### Scenario: Conversation exceeds context limit

- GIVEN a conversation that grows beyond the model's context limit
- WHEN the system builds the context for the LLM
- THEN the system SHALL apply its keep/summarize/drop strategy to fit within the token budget
- AND the system SHALL never send more tokens than the model's context window allows

### Requirement: Per-Thought Context Windows

The system SHALL maintain a separate context window per thought so that P2P chat messages and agent tasks do not share context with other work.

#### Scenario: Chat message gets its own context

- GIVEN a P2P chat message arrives for an agent
- WHEN the message becomes a thought in the recipient's queue
- THEN the system SHALL allocate a new context window for that thought
- AND the thought's context SHALL be managed independently under the token budgeting, compaction, and overflow rules

---

## 01 Token Budgeting Model

### 2.1 Context Window Limits by Provider

| Provider | Model | Context Window (tokens) | Notes |
|----------|-------|------------------------|-------|
| Anthropic | Claude Sonnet 4 | 200,000 | Largest available |
| Anthropic | Claude Haiku 3.5 | 200,000 | Smaller model, same limit |
| OpenAI | GPT-4o | 128,000 | Standard GPT-4 variant |
| OpenAI | GPT-4 Turbo | 128,000 | Older GPT-4 variant |
| OpenAI | o1-preview | 128,000 | Reasoning model |
| Ollama | qwen2.5-coder:32b | 32,768 | Local model, varies by quantization |
| Ollama | llama3.1:70b | 8,192 | Conservative default for older models |
| OpenRouter | Varies by model | Provider-dependent | Check model metadata via API |
| MiniMax | abab6.5s | 16,384 | Chinese market model |

### 2.2 Token Estimation Heuristics

For the Go runtime, we need fast token estimation without calling provider APIs:

```go
// Rough heuristic: ~4 characters per token for English text
func EstimateTokens(text string) int {
    runeCount := len([]rune(text))
    return runeCount / 4
}

// For tool outputs with structured data (JSON, code), use lower ratio
func EstimateToolOutputTokens(text string) int {
    return len([]rune(text)) / 3
}

// For markdown/text with lots of punctuation, use higher ratio
func EstimateMarkdownTokens(text string) int {
    return len([]rune(text)) / 5
}
```

### 2.3 Per-Provider Token Counting

When the provider supports token counting (Anthropic, OpenAI), use exact counts:

```go
type TokenCounter struct {
    provider     Provider
    tiktokenEnc  *tiktoken.Encoder  // For OpenAI-compatible models
}

// Get exact token count from provider API
func (tc *TokenCounter) CountExact(messages []Message) int {
    if tc.provider == nil {
        return tc.estimateTokens(messages)
    }
    
    // Call provider's token counting endpoint (if available)
    tokens, err := tc.provider.CountTokens(messages)
    if err != nil {
        return tc.estimateTokens(messages)
    }
    
    return tokens
}

// Estimate using heuristic when exact count unavailable
func (tc *TokenCounter) estimateTokens(messages []Message) int {
    total := 0
    
    for _, msg := range messages {
        switch msg.Role {
        case "system":
            total += 4 // System message overhead
        case "user", "assistant":
            total += 3 // User/assistant message overhead
        case "tool":
            total += 3 // Tool result overhead
        }
        
        if msg.Content != "" {
            total += EstimateTokens(msg.Content)
        }
    }
    
    return total
}

// tiktoken-based exact counting for OpenAI models
func (tc *TokenCounter) CountOpenAITokens(messages []Message, model string) int {
    encoding := "cl100k_base" // Default for GPT-4
    
    if strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1-") {
        encoding = "cl100k_base"
    } else if strings.HasPrefix(model, "text-") {
        encoding = "p50k_base"
    }
    
    enc, err := tiktoken.GetEncoding(encoding)
    if err != nil {
        return tc.estimateTokens(messages)
    }
    
    total := 0
    
    for _, msg := range messages {
        switch msg.Role {
        case "system":
            total += 4 // System message overhead
        case "user", "assistant":
            total += 3 // User/assistant message overhead
        case "tool":
            total += 3 // Tool result overhead
        }
        
        tokens := enc.Encode(msg.Content, nil, nil)
        total += len(tokens)
    }
    
    return total
}
```

### 2.4 Context Budget Configuration

```go
type ContextBudget struct {
    MaxTokens         int     // Hard limit (e.g., 128000 for GPT-4)
    WarningThreshold  float64 // Warn at this % of budget used (default: 0.8)
    CompactionTrigger float64 // Trigger compaction at this % of budget used (default: 0.9)
}

func DefaultContextBudget(provider string, model string) ContextBudget {
    maxTokens := getMaxContextWindow(provider, model)
    
    return ContextBudget{
        MaxTokens:         maxTokens,
        WarningThreshold:  0.8,
        CompactionTrigger: 0.9,
    }
}

func getMaxContextWindow(provider string, model string) int {
    switch provider {
    case "anthropic":
        return 200000 // Claude models support up to 200K tokens
    case "openai":
        if strings.HasPrefix(model, "gpt-4o") || strings.HasPrefix(model, "gpt-4-turbo") {
            return 128000
        }
        return 32768 // Older GPT models
    case "ollama":
        if strings.Contains(model, "qwen2.5") {
            return 32768
        } else if strings.Contains(model, "llama3") {
            return 8192
        }
        return 4096 // Conservative default
    case "openrouter":
        // Check model metadata via API (see 20-llm-provider-layer.md)
        return 128000 // Default assumption
    case "minimax":
        return 16384
    default:
        return 32768 // Conservative fallback
    }
}
```

### Requirement: Per-Provider Context Window Limits

The system SHALL know the context window size (in tokens) for each supported provider/model combination, with provider-specific defaults as defined in the specification.

#### Scenario: Default budget for Anthropic

- GIVEN the agent is configured with an Anthropic model
- WHEN the system derives a context budget
- THEN the system SHALL use 200,000 tokens as the `MaxTokens` limit
- AND the budget SHALL use default warning and compaction thresholds of 0.8 and 0.9

#### Scenario: Default budget for OpenAI GPT-4o

- GIVEN the agent is configured with OpenAI GPT-4o or GPT-4 Turbo
- WHEN the system derives a context budget
- THEN the system SHALL use 128,000 tokens as the `MaxTokens` limit

### Requirement: Token Estimation Heuristics

The system SHALL estimate token counts locally using character-based heuristics: approximately 4 characters per token for general text, 3 for structured tool output, and 5 for markdown with punctuation.

#### Scenario: Local estimation without provider call

- GIVEN the provider does not support exact token counting or is unavailable
- WHEN the system needs a token count
- THEN the system SHALL use the local heuristic estimators (`EstimateTokens`, `EstimateToolOutputTokens`, `EstimateMarkdownTokens`)
- AND each estimator SHALL use its documented characters-per-token ratio

### Requirement: Exact Token Counting When Available

The system SHALL use exact token counts from the provider API or tiktoken encoders when available, falling back to heuristics on error.

#### Scenario: Provider token counting endpoint succeeds

- GIVEN the provider exposes a token counting endpoint
- WHEN `CountExact` is called
- THEN the system SHALL use the provider's exact count
- AND the system SHALL NOT apply the heuristic estimate

#### Scenario: Provider count fails, fallback to heuristic

- GIVEN the provider token counting call returns an error
- WHEN `CountExact` is called
- THEN the system SHALL fall back to the local heuristic estimation
- AND processing SHALL continue without failing the request

### Requirement: Context Budget Configuration

The system SHALL maintain a context budget per model with a `MaxTokens` hard limit, a `WarningThreshold` (default 0.8), and a `CompactionTrigger` (default 0.9).

#### Scenario: Budget defaults applied

- GIVEN the system initializes a context budget
- WHEN no explicit thresholds are configured
- THEN the `WarningThreshold` SHALL default to 0.8
- AND the `CompactionTrigger` SHALL default to 0.9

---

## 02 Context Building Pipeline

### 3.1 Priority Order for Context Components

When building the final context sent to the LLM, components are added in this order (highest priority first):

| Priority | Component | Description | When Included |
|----------|-----------|-------------|---------------|
| **1** | System Prompt | Persona + active skills | Always included |
| **2** | Recent Messages | Last N messages (last 3-5 turns) | Always included |
| **3** | Memory Injection | User memory, session state | Always injected at session start |
| **4** | Context Files | AGENTS.md, project conventions | Loaded based on relevance |
| **5** | Older Messages | Messages before the "recent" window | Compacted/summarized if over budget |
| **6** | Tool Outputs | Results from recent tool calls | Included with messages |

### 3.2 Context Builder Implementation

```go
type ContextBuilder struct {
    persona        string
    activeSkills   []*CompiledSkill
    userMemory     *UserMemory
    sessionState   *SessionState
    contextFiles   []ContextFile
    recentMessages []Message
}

func (cb *ContextBuilder) Build(maxTokens int, budget ContextBudget) ([]Message, int) {
    var messages []Message
    tokenCount := 0
    
    // 1. System prompt (highest priority — always included)
    systemPrompt := cb.buildSystemPrompt()
    sysMsg := Message{Role: "system", Content: systemPrompt}
    
    tokens := tc.CountOpenAITokens([]Message{sysMsg}, "gpt-4")
    if tokens > budget.MaxTokens {
        // System prompt alone exceeds context — truncate it
        systemPrompt = cb.truncateToTokenLimit(systemPrompt, budget.MaxTokens - 100)
        sysMsg.Content = systemPrompt
        tokenCount += budget.MaxTokens - 100
    } else {
        messages = append(messages, sysMsg)
        tokenCount += tokens
    }
    
    // 2. Recent messages (last 3-5 turns — always included)
    recentStart := max(0, len(cb.recentMessages) - 10) // Last ~5 turns (user+assistant pairs)
    for _, msg := range cb.recentMessages[recentStart:] {
        tokens = tc.CountOpenAITokens([]Message{msg}, "gpt-4")
        
        if tokenCount + tokens > budget.MaxTokens {
            break // Don't exceed budget
        }
        
        messages = append(messages, msg)
        tokenCount += tokens
    }
    
    // 3. Memory injection (user memory, session state)
    if cb.userMemory != nil {
        memMsg := Message{Role: "system", Content: cb.userMemory.Inject()}
        tokens = tc.CountOpenAITokens([]Message{memMsg}, "gpt-4")
        
        if tokenCount + tokens < budget.MaxTokens * 0.95 { // Leave some buffer
            messages = append(messages, memMsg)
            tokenCount += tokens
        }
    }
    
    // 4. Context files (if relevant and fit within budget)
    for _, file := range cb.contextFiles {
        fileMsg := Message{Role: "system", Content: fmt.Sprintf("## %s\n%s", file.Name, file.Content)}
        tokens = tc.CountOpenAITokens([]Message{fileMsg}, "gpt-4")
        
        if tokenCount + tokens < budget.MaxTokens * 0.9 { // Leave more buffer for files
            messages = append(messages, fileMsg)
            tokenCount += tokens
        }
    }
    
    return messages, tokenCount
}

// Truncate content to fit within a token limit (rough heuristic)
func (cb *ContextBuilder) truncateToTokenLimit(content string, maxTokens int) string {
    // Rough estimate: 4 chars per token for English text
    maxChars := maxTokens * 4
    
    if len([]rune(content)) <= maxChars {
        return content
    }
    
    // Truncate to max characters
    truncated := []rune(content)[:maxChars]
    return string(truncated) + "\n... [truncated due to context limits]"
}
```

### 3.3 Compaction Trigger Logic

```go
func shouldCompact(budget ContextBudget, currentTokens int) bool {
    if currentTokens >= budget.MaxTokens * budget.CompactionTrigger {
        return true
    }
    
    // Also compact if we're approaching the warning threshold and have many messages
    if currentTokens >= budget.MaxTokens * budget.WarningThreshold {
        messageCount := len(cb.recentMessages)
        if messageCount > 50 { // More than ~25 turns
            return true
        }
    }
    
    return false
}

func runCompaction(ctx context.Context, session *Session, budget ContextBudget, tc TokenCounter) error {
    messages := buildFullMessageHistory(session)
    currentTokens := tc.CountExact(messages)
    
    if !shouldCompact(budget, currentTokens) {
        return nil // No compaction needed
    }
    
    // Identify messages to compact (older than last 5 turns)
    cutoffIndex := len(messages) - 10 // Keep last ~5 turns
    toCompact := messages[:cutoffIndex]
    keepMessages := messages[cutoffIndex:]
    
    if len(toCompact) == 0 {
        return nil
    }
    
    // Build compaction prompt
    compactionPrompt := buildCompactionPrompt(toCompact)
    
    // Call LLM to summarize compacted messages
    summary, err := callLLMForSummary(ctx, compactionPrompt, budget)
    if err != nil {
        return fmt.Errorf("compaction failed: %w", err)
    }
    
    // Replace compacted messages with summary
    summaryMsg := Message{Role: "system", Content: summary}
    newMessages := append([]Message{summaryMsg}, keepMessages...)
    
    // Update session state
    session.Messages = newMessages
    
    return nil
}

// Build a prompt for the LLM to summarize messages
func buildCompactionPrompt(messages []Message) string {
    var sb strings.Builder
    
    sb.WriteString("Summarize the following conversation, preserving:\n")
    sb.WriteString("- Key decisions and outcomes\n")
    sb.WriteString("- Important context and constraints\n")
    sb.WriteString("- Action items or next steps\n")
    sb.WriteString("\nThe summary should be concise but comprehensive enough to continue the conversation.\n\n")
    
    for _, msg := range messages {
        role := strings.ToUpper(msg.Role[:1]) + msg.Role[1:]
        sb.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, msg.Content))
    }
    
    return sb.String()
}

// Call the LLM to generate a summary (uses provider from 20-llm-provider-layer.md)
func callLLMForSummary(ctx context.Context, prompt string, budget ContextBudget) (string, error) {
    config := DefaultConfig("anthropic") // Use primary provider
    
    messages := []Message{{Role: "user", Content: prompt}}
    
    events, err := provider.Stream(ctx, messages, nil, config)
    if err != nil {
        return "", err
    }
    
    var summary strings.Builder
    for event := range events {
        if event.Type == StreamEventContentDelta {
            summary.WriteString(event.Content)
        }
        if event.Type == StreamEventStop || event.Type == StreamEventError {
            break
        }
    }
    
    return summary.String(), nil
}
```

### Requirement: Priority-Ordered Context Assembly

The system SHALL assemble the final LLM context in priority order: system prompt first, then recent messages, then memory injection, then context files, with older messages compacted if over budget.

#### Scenario: System prompt always included

- GIVEN the context builder runs
- WHEN building the final message list
- THEN the system prompt SHALL always be included first regardless of budget
- AND if the system prompt alone exceeds the budget, it SHALL be truncated to fit with a safety margin

#### Scenario: Recent messages added within budget

- GIVEN recent messages exist in the session
- WHEN the context builder adds them
- THEN the last 3-5 turns (approx. 10 messages) SHALL be added after the system prompt
- AND addition SHALL stop before exceeding the `MaxTokens` budget

### Requirement: Token Budget Enforcement During Build

The system SHALL enforce the token budget while building context, adding each component only if it fits within the remaining budget and leaving buffer for higher-priority components.

#### Scenario: Memory injection skipped when over budget

- GIVEN the token count is already near the budget limit
- WHEN the context builder reaches the memory injection step
- THEN the memory message SHALL only be added if the total stays below 95% of `MaxTokens`
- AND the memory message SHALL be skipped if it would exceed that buffer

#### Scenario: Context files limited by budget

- GIVEN context files are available
- WHEN the context builder adds them
- THEN each file SHALL be added only if the total stays below 90% of `MaxTokens`
- AND files SHALL be skipped once the budget buffer is consumed

### Requirement: Compaction Trigger Logic

The system SHALL trigger compaction when token usage reaches `CompactionTrigger` (default 90%) of the budget, or when usage is above the `WarningThreshold` (default 80%) and the message count exceeds 50 messages.

#### Scenario: Compaction at 90% usage

- GIVEN the current token count reaches or exceeds 90% of `MaxTokens`
- WHEN the system checks the compaction trigger
- THEN the system SHALL run compaction
- AND compaction SHALL summarize all messages older than the last ~5 turns into a single summary message

#### Scenario: Compaction at warning threshold with many messages

- GIVEN token usage is between 80% and 90% of `MaxTokens`
- WHEN the message count exceeds 50 messages (approximately 25 turns)
- THEN the system SHALL run compaction
- AND the recent ~5 turns SHALL be preserved uncompacted

### Requirement: Compaction Preserves Recent Turns

The system SHALL keep the last 5 turns (approximately 10 messages) uncompacted during compaction and replace only the older messages with the LLM-generated summary.

#### Scenario: Old messages replaced by summary

- GIVEN compaction runs on a session
- WHEN the system identifies messages to compact
- THEN messages before the cutoff index (last 10 messages) SHALL be replaced by a single system-role summary message
- AND the last 10 messages SHALL remain in the context untouched

---

## 03 Overflow Handling: What Gets Dropped First

### 4.1 Drop Priority (Lowest Priority Dropped First)

| Priority | Component | When Dropped | Replacement |
|----------|-----------|--------------|-------------|
| **1** (dropped first) | Context files beyond relevance threshold | Always dropped when budget is tight | Summarized into context summary if possible |
| **2** | Older messages (before "recent" window) | Compacted/summarized | Replaced with LLM-generated summary |
| **3** | Tool outputs for non-critical tools | When tool output is very long | Truncated to first 1000 chars + "... [truncated]" |
| **4** (dropped last) | Recent messages and system prompt | Only as last resort | Never dropped — triggers compaction instead |

### 4.2 Context File Relevance Filtering

```go
type ContextFileRelevance struct {
    FilePath string
    Score    float64 // Higher = more relevant
}

// Calculate relevance score for context files based on current conversation topic
func calculateContextFileRelevance(files []ContextFile, recentMessages []Message) []ContextFileRelevance {
    // Simple keyword-based scoring (can be enhanced with embeddings later)
    var topicKeywords []string
    
    for _, msg := range recentMessages[len(recentMessages)-5:] {
        words := strings.Fields(strings.ToLower(msg.Content))
        topicKeywords = append(topicKeywords, words...)
    }
    
    keywordSet := make(map[string]bool)
    for _, kw := range topicKeywords {
        if len(kw) > 3 { // Ignore short words
            keywordSet[kw] = true
        }
    }
    
    var results []ContextFileRelevance
    
    for _, file := range files {
        score := calculateFileRelevance(file, keywordSet)
        results = append(results, ContextFileRelevance{
            FilePath: file.Path,
            Score:    score,
        })
    }
    
    // Sort by relevance (highest first)
    sort.Slice(results, func(i, j int) bool {
        return results[i].Score > results[j].Score
    })
    
    return results
}

// Simple keyword overlap scoring
func calculateFileRelevance(file ContextFile, keywords map[string]bool) float64 {
    fileContent := strings.ToLower(file.Content)
    matches := 0
    
    for kw := range keywords {
        if strings.Contains(fileContent, kw) {
            matches++
        }
    }
    
    return float64(matches) / float64(len(keywords))
}

// Filter context files by relevance threshold and budget
func filterContextFiles(files []ContextFileRelevance, maxTokens int, currentTokenCount int) []string {
    var included []string
    
    for _, file := range files {
        // Estimate tokens for this file
        fileMsg := Message{Role: "system", Content: fmt.Sprintf("## %s\n%s", file.FilePath, readFileContent(file.FilePath))}
        tokens := tc.CountOpenAITokens([]Message{fileMsg}, "gpt-4")
        
        if currentTokenCount + tokens > maxTokens * 0.9 { // Leave buffer
            break // Don't exceed budget
        }
        
        included = append(included, file.FilePath)
        currentTokenCount += tokens
    }
    
    return included
}
```

### 4.3 Tool Output Truncation

```go
func truncateToolOutput(output string, maxChars int) string {
    if len([]rune(output)) <= maxChars {
        return output
    }
    
    truncated := []rune(output)[:maxChars]
    return string(truncated) + "\n... [truncated — tool output exceeded character limit]"
}

// Apply truncation to all tool outputs in a message sequence
func truncateAllToolOutputs(messages []Message, maxToolOutputChars int) []Message {
    for i, msg := range messages {
        if msg.Role == "tool" && len([]rune(msg.Content)) > maxToolOutputChars {
            messages[i].Content = truncateToolOutput(msg.Content, maxToolOutputChars)
        }
    }
    
    return messages
}

// Default tool output limit: 1000 characters (configurable per tool)
const defaultMaxToolOutputChars = 1000
```

### Requirement: Overflow Drop Priority Order

The system SHALL drop context components in a fixed priority order when over budget: context files first, then older messages (via compaction), then long tool outputs (via truncation), and never drop recent messages or the system prompt.

#### Scenario: Budget tight, files dropped first

- GIVEN the context is over budget
- WHEN the system handles overflow
- THEN the system SHALL drop context files beyond the relevance threshold first
- AND the system SHALL only compact older messages or truncate tool outputs after files are dropped

#### Scenario: Recent messages never dropped

- GIVEN the budget is nearly exhausted
- WHEN the system handles overflow
- THEN the system SHALL NOT drop recent messages or the system prompt
- AND the system SHALL trigger compaction instead as the last resort

### Requirement: Keyword-Based Context File Relevance

The system SHALL score context files by keyword overlap between the recent conversation and file content, sort them by relevance, and include only the highest-scoring files that fit within the token budget.

#### Scenario: Relevant files included first

- GIVEN context files are candidates for inclusion
- WHEN the system computes relevance scores
- THEN each file SHALL be scored by the ratio of matching topic keywords in its content
- AND files SHALL be included in descending relevance order until the budget buffer (90% of `MaxTokens`) is reached

### Requirement: Tool Output Truncation

The system SHALL truncate tool outputs that exceed a character limit, with a default limit of 1000 characters, appending a truncation marker to the output.

#### Scenario: Long tool output truncated

- GIVEN a tool returns an output longer than the configured limit
- WHEN the system processes the tool result
- THEN the system SHALL truncate the output to the first 1000 characters (or the per-tool configured limit)
- AND the truncated output SHALL end with an explicit `[truncated]` marker

---

## 04 Integration with Session and Memory Systems

### 5.1 Connection to Session Compaction (10-session-management.md)

Session compaction handles long-term persistence, while context window management handles real-time budgeting:

| Concern | Context Window Management | Session Compaction |
|---------|--------------------------|-------------------|
| **When** | During agent loop execution | When session exceeds message count threshold (e.g., 5000 messages) or user runs `/compact` |
| **What** | Summarizes older messages in current context | Replaces compacted messages with summary in SQLite DB |
| **Where** | In-memory, temporary | Persisted to SQLite with `is_compacted=1` flag |
| **Who triggers it** | Automatic (based on token budget) | User-initiated or automatic (message count threshold) |

### 5.2 Connection to Memory System ([00-memory-system.md](../06-memory/00-memory-system.md))

Memory injection happens before context window management kicks in:

```go
// Session start flow (from 00-memory-system.md):
func onStartSession(session *Session) {
    // 1. Load user memory — inject into system prompt
    userMemory := loadUserMemory(session.UserID)
    session.UserMemory = userMemory
    
    // 2. Build initial context with budget awareness
    budget := DefaultContextBudget(currentProvider, currentModel)
    messages, tokenCount := buildInitialContext(userMemory, session)
    
    if tokenCount > budget.MaxTokens {
        log.Warnf("initial context exceeds token limit: %d > %d", tokenCount, budget.MaxTokens)
        // Truncate user memory or context files to fit
        truncateToFit(messages, budget)
    }
}

// During interaction flow (from agent loop):
func onUserMessage(session *Session, userInput string) {
    // Add new message
    session.AddMessage(Message{Role: "user", Content: userInput})
    
    // Check if context is getting too large
    budget := DefaultContextBudget(currentProvider, currentModel)
    messages, tokenCount := buildContextForAgent(session)
    
    if shouldCompact(budget, tokenCount) {
        runCompaction(context.Background(), session, budget, tc)
    }
}
```

### Requirement: Session Start Context Budget Check

The system SHALL build the initial context at session start with budget awareness, and if the initial context exceeds `MaxTokens`, warn and truncate user memory or context files to fit.

#### Scenario: Initial context fits budget

- GIVEN a session starts
- WHEN the system builds the initial context
- THEN the system SHALL load user memory and inject it into the session
- AND the system SHALL check the token count against the budget for the active provider and model

#### Scenario: Initial context exceeds budget

- GIVEN the initial context token count exceeds `MaxTokens`
- WHEN the session starts
- THEN the system SHALL log a warning with the token counts
- AND the system SHALL truncate user memory or context files to fit within the budget

### Requirement: Context Check on Each User Message

The system SHALL check the context size on every user message during the agent loop and trigger compaction automatically when the budget threshold is crossed.

#### Scenario: Message pushes context over threshold

- GIVEN a user sends a message during an active session
- WHEN the system adds the message and rebuilds the context
- THEN the system SHALL compute the new token count
- AND if the count reaches the compaction trigger, the system SHALL run compaction before continuing

---

## 05 Claude Code and Hermes Agent Patterns (Research-Based)

### 6.1 Claude Code Approach

**Key patterns observed:**
- **Progressive context building**: Starts with system prompt + recent conversation, adds files on-demand as user references them
- **File relevance scoring**: Uses keyword overlap between conversation topic and file content to decide which AGENTS.md/project docs to include
- **Token budget awareness**: Tracks token usage per component (system prompt, messages, tools) and drops lowest-priority items first
- **Compaction as last resort**: Only compacts when context is nearly full; prefers truncating tool outputs or dropping old files

**Adaptation for OpenNavi:**
- Implement file relevance scoring based on conversation topic keywords
- Add per-component token tracking (system prompt, messages, tools, memory)
- Drop context files before compacting messages (files are less critical than recent conversation)

### 6.2 Hermes Agent Approach

**Key patterns observed:**
- **Memory-first design**: User memory and session state are always injected into system prompt at session start
- **Transparent storage**: No approval gate for reading/writing user memories — agent has full access to its own memory
- **Compaction via summary**: When conversation grows too long, older messages are summarized by LLM and replaced with a single summary message
- **User-controlled compaction**: User can manually trigger `/compact` command; system auto-compacts when approaching token limits

**Adaptation for OpenNavi:**
- Adopt Hermes' transparent memory injection pattern (no approval gate)
- Use LLM-based summarization for compaction (not simple truncation)
- Support both manual (`/compact`) and automatic compaction triggers

### 6.3 pi-code / Pi Agent Approach

**Key patterns observed:**
- **Copy-on-write state**: Agent state is immutable; mutations create new state copies to avoid race conditions
- **Event-driven context building**: Context is rebuilt from events (message deltas, tool call results) rather than reading full history each time
- **Steering queue integration**: User steering messages are injected at safe points during agent execution

**Adaptation for OpenNavi:**
- Leverage OpenNavi's existing copy-on-write state pattern (from 03-agent-runtime.md)
- Add context rebuild trigger after each tool call result or steering message
- Track token count incrementally as events are processed

### Requirement: Progressive Context Building

The system SHALL build context progressively, starting with the system prompt and recent conversation, and add context files on demand as the user references them.

#### Scenario: Files added on demand

- GIVEN a conversation is in progress
- WHEN the user references a file or project convention
- THEN the system SHALL load the relevant context file and add it to the context
- AND the file SHALL be added only if it fits within the remaining token budget

### Requirement: Per-Component Token Tracking

The system SHALL track token usage separately per component (system prompt, messages, tools, memory) to enable fine-grained drop decisions when over budget.

#### Scenario: Component usage tracked separately

- GIVEN the context builder assembles the context
- WHEN components are added
- THEN the system SHALL record token counts per component (system prompt, messages, memory, files, tool outputs)
- AND when over budget, the system SHALL use these per-component counts to drop the lowest-priority component first

### Requirement: Manual and Automatic Compaction Triggers

The system SHALL support both manual compaction via the `/compact` command and automatic compaction when approaching token limits, using LLM summarization rather than simple truncation.

#### Scenario: Manual compact command

- GIVEN a user issues the `/compact` command
- WHEN the system receives the command
- THEN the system SHALL run LLM-based compaction on older messages
- AND replace the compacted messages with a single summary message

#### Scenario: Automatic compaction near limit

- GIVEN the token count approaches the compaction trigger threshold
- WHEN the system processes a user message
- THEN the system SHALL automatically run compaction
- AND the compaction SHALL use LLM summarization, never simple truncation

### Requirement: Copy-on-Write Context State

The system SHALL build context from immutable state copies and rebuild the context after each tool call result or steering message, tracking token counts incrementally.

#### Scenario: Context rebuilt after tool call

- GIVEN a tool call completes and returns a result
- WHEN the agent continues execution
- THEN the system SHALL rebuild the context from the updated state
- AND the system SHALL incrementally update the token count rather than recounting the full history

---

## 06 Implementation Order

1. **Token counting** — Exact counts via provider API, heuristics for estimation
2. **Context builder** — Priority-ordered context assembly (system → recent messages → memory → files)
3. **Budget configuration** — Per-provider token limits and thresholds
4. **Compaction logic** — Identify messages to compact, call LLM for summary, replace in session
5. **Overflow handling** — Drop priority tiers (files first, then old messages, then tool outputs)
6. **Session integration** — Connect context building to session start/interaction flows
7. **Memory integration** — Inject user memory before context window management kicks in

---

## 07 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Priority-ordered context assembly** — System prompt first, then recent messages, then memory, then files | Ensures most critical information is always included; least critical (files) dropped first |
| **LLM-based compaction** — Summarize old messages via LLM rather than truncating | Preserves important context better than simple character truncation |
| **Drop files before compacting messages** — Context files are less critical than recent conversation | Files can be re-loaded if needed; recent conversation is harder to recover |
| **Token budget as soft limit with warning threshold** — Compact at 90%, warn at 80% | Gives agent time to handle overflow gracefully without hard cutoffs |
| **Per-component token tracking** — Track tokens for system prompt, messages, memory separately | Enables fine-grained control over what gets dropped when over budget |
| **Keyword-based file relevance scoring** — Simple heuristic for deciding which AGENTS.md/project docs to include | Works well enough for most cases; can be enhanced with embeddings later |
| **Tool output truncation** — Default 1000 char limit, configurable per tool | Prevents single large tool outputs from consuming entire context window |


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S030200 | 0.1.0 | Migrated from docs/spec/ to spec-tree |