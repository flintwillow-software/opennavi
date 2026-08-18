---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - llm-provider
priority: medium
spec-refs: [070000, 070001, 070002, 070003, 070004, 070005, 070006, 070007, 070008, 070009, 070010]
---

# Plan: LLM Provider Layer

**Scope:** Unified abstraction over model providers (Anthropic, OpenAI, Ollama, LM Studio, OpenRouter, MiniMax) with streaming protocol, token counting, and tool calling normalization.

---

## 00 Overview

The LLM Provider Layer is the Go SDK component that wraps multiple model providers behind a single interface. It handles:
- Streaming responses (text deltas, tool calls, stop signals)
- Token counting for budget management
- Tool calling format normalization across providers
- Error handling and retry logic per provider
- Configuration (API keys, endpoints, defaults)

### Requirement: Unified Provider Abstraction

The system SHALL provide a unified provider abstraction that wraps multiple model providers (Anthropic, OpenAI, Ollama, LM Studio, OpenRouter, MiniMax) behind a single interface.

#### Scenario: Single interface for all providers
- GIVEN the system needs to interact with any supported model provider
- WHEN an LLM request is made
- THEN the system SHALL use the same interface regardless of the underlying provider
- AND the system SHALL handle provider-specific protocol differences transparently

#### Scenario: Streaming responses across providers
- GIVEN a provider supports streaming
- WHEN the system sends a streaming request
- THEN the system SHALL return text deltas, tool calls, and stop signals via a unified stream event channel

### Requirement: Cross-Cutting Capabilities

The system SHALL support token counting, tool calling format normalization, error handling with retry, and provider configuration as cross-cutting capabilities of the provider layer.

#### Scenario: Token counting for budget management
- GIVEN a sequence of messages
- WHEN the system needs to estimate token usage
- THEN the system SHALL count tokens per-provider using the appropriate strategy

#### Scenario: Tool call format normalization
- GIVEN a tool call from a provider
- WHEN the system processes the tool call
- THEN the system SHALL normalize the provider-specific format to the internal OpenNavi format

---

## 01 Unified Provider Interface

```go
type Provider interface {
    // Stream returns a channel of stream events
    Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error)
    
    // Complete is a non-streaming variant that returns the full response
    Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error)
    
    // Ping checks provider connectivity and auth
    Ping(ctx context.Context) error
    
    // Name returns the provider identifier
    Name() string
    
    // TokenCount estimates tokens for a message sequence
    TokenCount(messages []Message) int
    
    // MaxContextWindow returns the model's context window size in tokens
    MaxContextWindow(config Config) int
}

type Message struct {
    Role      string          // "system", "user", "assistant", "tool"
    Content   string          // text content
    ToolCalls []ToolCall      // for assistant messages with tool calls
    ToolResults []ToolResult  // for tool role messages
    Images    []ImageRef      // optional image attachments
}

type Tool struct {
    Name        string
    Description string
    Parameters  json.RawMessage // JSON Schema
}

type Config struct {
    Provider    string          // "anthropic", "openai", "ollama", "openrouter", "minimax"
    Model       string
    Temperature float64
    MaxTokens   int
    Thinking    ThinkingConfig
    Endpoint    string          // optional custom endpoint (Ollama, OpenRouter)
    APIKey      string          // resolved from env var or config
}

type StreamEvent struct {
    Type     StreamEventType
    Content  string            // text delta for content events
    ToolCall *ToolCallDelta    // tool call start/end
    Usage    *UsageInfo        // token usage (if available)
    Error    error             // stream error
}

type StreamEventType string
const (
    StreamEventMessageStart  StreamEventType = "message_start"
    StreamEventContentDelta  StreamEventType = "content_delta"
    StreamEventToolCallStart StreamEventType = "tool_call_start"
    StreamEventToolCallEnd   StreamEventType = "tool_call_end"
    StreamEventStop          StreamEventType = "stop"
    StreamEventError         StreamEventType = "error"
)

type ToolCallDelta struct {
    ID        string
    Name      string
    Arguments string  // JSON string of arguments (partial for streaming)
}

type UsageInfo struct {
    InputTokens     int
    OutputTokens    int
    ThinkingTokens  int  // Anthropic-specific
}
```

### Requirement: Provider Interface Contract

The system SHALL define a `Provider` interface contract with `Stream`, `Complete`, `Ping`, `Name`, `TokenCount`, and `MaxContextWindow` methods that all provider adapters implement.

#### Scenario: Streaming request
- GIVEN a context, messages, tools, and config
- WHEN `Stream` is called
- THEN the system SHALL return a channel of `StreamEvent` and an error
- AND the events SHALL include message start, content deltas, tool call start/end, stop, and error events

#### Scenario: Non-streaming request
- GIVEN a context, messages, tools, and config
- WHEN `Complete` is called
- THEN the system SHALL return the full `Response` and an error

#### Scenario: Connectivity check
- GIVEN a provider with API credentials
- WHEN `Ping` is called
- THEN the system SHALL return an error if the provider is unreachable or credentials are invalid

### Requirement: Message and Tool Types

The system SHALL define `Message`, `Tool`, `Config`, and `StreamEvent` types that represent requests and responses in a provider-agnostic format.

#### Scenario: Message format
- GIVEN a conversation with system, user, assistant, and tool messages
- WHEN converting to the internal format
- THEN each message SHALL contain a role, content, and optional tool calls, tool results, or image attachments

#### Scenario: Tool definition format
- GIVEN a tool with a name, description, and JSON Schema parameters
- WHEN specifying the tool to a provider
- THEN the system SHALL represent it as a `Tool` struct with `Name`, `Description`, and `Parameters` fields

### Requirement: Stream Event Types

The system SHALL define a `StreamEvent` type with typed event types covering message start, content delta, tool call start, tool call end, stop, and error events.

#### Scenario: Content delta streaming
- GIVEN a streaming response from a provider
- WHEN the provider emits a text delta
- THEN the system SHALL emit a `StreamEventContentDelta` event with the text content

#### Scenario: Tool call streaming
- GIVEN a streaming response that includes tool calls
- WHEN the provider starts a tool call
- THEN the system SHALL emit a `StreamEventToolCallStart` event with the tool call delta
- AND when the tool call completes, the system SHALL emit a `StreamEventToolCallEnd` event

#### Scenario: Stream termination
- GIVEN a streaming response
- WHEN the provider signals the end of the stream
- THEN the system SHALL emit a `StreamEventStop` event

---

## 02 Provider Adapters

### 3.1 Anthropic Adapter

**API:** https://docs.anthropic.com/en/docs/build-with-claude/tool-use/streaming-tool-calls  
**Streaming:** SSE (Server-Sent Events)  
**Tool Calling:** Native function calling with `tool_use` blocks  

```go
type AnthropicProvider struct {
    client   *http.Client
    baseURL  string
}

func (p *AnthropicProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
    // Convert OpenNavi messages to Anthropic format
    anthropicMessages := convertToAnthropic(messages)
    
    // Build request body with stream=true
    reqBody := map[string]any{
        "model":       config.Model,
        "max_tokens":  config.MaxTokens,
        "messages":    anthropicMessages,
        "stream":      true,
        "temperature": config.Temperature,
    }
    
    if len(tools) > 0 {
        reqBody["tools"] = convertToolsToAnthropic(tools)
    }
    
    // POST to /v1/messages with SSE stream
    resp, err := p.client.Post("https://api.anthropic.com/v1/messages", "application/json", reqBody)
    if err != nil {
        return nil, err
    }
    
    events := make(chan StreamEvent, 64)
    go func() {
        defer close(events)
        
        scanner := bufio.NewScanner(resp.Body)
        for scanner.Scan() {
            line := scanner.Text()
            
            if !strings.HasPrefix(line, "event: ") {
                continue
            }
            
            eventType := strings.TrimPrefix(line, "event: ")
            // Read the data line (next non-empty line)
            scanner.Scan()
            data := scanner.Text()
            
            event := parseAnthropicEvent(eventType, data)
            events <- event
            
            if event.Type == StreamEventStop || event.Type == StreamEventError {
                return
            }
        }
    }()
    
    return events, nil
}

// Anthropic tool format:
type AnthropicTool struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema json.RawMessage `json:"input_schema"`  // JSON Schema
}
```

**Token counting:** Anthropic provides token counts in stream events (`usage.input_tokens`, `usage.output_tokens`). Use these directly when available.

### Requirement: Anthropic Adapter

The system SHALL implement an Anthropic provider adapter that communicates with the Anthropic API via SSE streaming, native tool calling with `tool_use` blocks, and uses token counts from stream events.

#### Scenario: Anthropic streaming request
- GIVEN an Anthropic provider adapter with valid API credentials
- WHEN `Stream` is called with messages and optional tools
- THEN the adapter SHALL POST to `/v1/messages` with stream enabled
- AND the adapter SHALL parse Anthropic SSE events and convert them to unified `StreamEvent` values

#### Scenario: Anthropic tool format
- GIVEN tools are specified in an Anthropic request
- WHEN converting to the Anthropic format
- THEN the adapter SHALL convert tool definitions to the Anthropic `input_schema` format

---

### 3.2 OpenAI Adapter

**API:** https://platform.openai.com/docs/api-reference/chat  
**Streaming:** SSE (Server-Sent Events)  
**Tool Calling:** Function calling with `tool_calls` array  

```go
type OpenAIProvider struct {
    client   *http.Client
    baseURL  string
}

func (p *OpenAIProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
    // Convert to OpenAI format
    openaiMessages := convertToOpenAI(messages)
    
    reqBody := map[string]any{
        "model":       config.Model,
        "messages":    openaiMessages,
        "stream":      true,
        "temperature": config.Temperature,
        "max_tokens":  config.MaxTokens,
    }
    
    if len(tools) > 0 {
        reqBody["tools"] = convertToolsToOpenAI(tools)
    }
    
    resp, err := p.client.Post(p.baseURL+"/chat/completions", "application/json", reqBody)
    if err != nil {
        return nil, err
    }
    
    events := make(chan StreamEvent, 64)
    go func() {
        defer close(events)
        
        scanner := bufio.NewScanner(resp.Body)
        for scanner.Scan() {
            line := scanner.Text()
            
            if !strings.HasPrefix(line, "data: ") {
                continue
            }
            
            data := strings.TrimPrefix(line, "data: ")
            if data == "[DONE]" {
                events <- StreamEvent{Type: StreamEventStop}
                return
            }
            
            var chunk struct {
                Choices []struct {
                    Delta struct {
                        Content   string          `json:"content"`
                        ToolCalls []ToolCallDelta `json:"tool_calls"`
                    } `json:"delta"`
                } `json:"choices"`
                Usage *UsageInfo `json:"usage"`
            }
            
            if err := json.Unmarshal([]byte(data), &chunk); err != nil {
                continue
            }
            
            // Emit events for each choice delta
            for _, choice := range chunk.Choices {
                if choice.Delta.Content != "" {
                    events <- StreamEvent{
                        Type:    StreamEventContentDelta,
                        Content: choice.Delta.Content,
                    }
                }
                
                if len(choice.Delta.ToolCalls) > 0 {
                    for _, tc := range choice.Delta.ToolCalls {
                        events <- StreamEvent{
                            Type:     StreamEventToolCallStart,
                            ToolCall: &tc,
                        }
                        events <- StreamEvent{
                            Type:     StreamEventToolCallEnd,
                            ToolCall: &tc,
                        }
                    }
                }
            }
        }
    }()
    
    return events, nil
}

// OpenAI tool format:
type OpenAITool struct {
    Type       string          `json:"type"`  // "function"
    Function   OpenAIFunction  `json:"function"`
}

type OpenAIFunction struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  json.RawMessage `json:"parameters"`  // JSON Schema
}
```

**Token counting:** Use the `tiktoken` Go library (`github.com/pkoukk/tiktoken-go`) with the model's tokenizer (e.g., `cl100k_base` for GPT-4).

### Requirement: OpenAI Adapter

The system SHALL implement an OpenAI provider adapter that communicates with the OpenAI API via SSE streaming, function calling with `tool_calls` array, and uses the `tiktoken` Go library for token counting.

#### Scenario: OpenAI streaming request
- GIVEN an OpenAI provider adapter with valid API credentials
- WHEN `Stream` is called with messages and optional tools
- THEN the adapter SHALL POST to `/chat/completions` with stream enabled
- AND the adapter SHALL parse `data:` SSE lines and convert them to unified `StreamEvent` values
- AND the adapter SHALL emit a `StreamEventStop` upon receiving the `[DONE]` signal

#### Scenario: OpenAI tool format
- GIVEN tools are specified in an OpenAI request
- WHEN converting to the OpenAI format
- THEN the adapter SHALL convert tool definitions to the OpenAI function-calling format with `type: "function"` and a `function` object containing `name`, `description`, and `parameters`

---

### 3.3 LM Studio Adapter

LM Studio exposes an OpenAI-compatible API at a local endpoint (default `http://localhost:1234/v1`). The LM Studio adapter follows the same pattern as the Ollama adapter: a local base URL, model name resolution, and streaming using the OpenAI-compatible format. It exists as a named adapter so local-only configuration can reference `lm-studio` directly rather than pointing an OpenAI adapter at a local URL.

```go
type LMStudioProvider struct {
    baseURL string  // default: http://localhost:1234/v1
}

func NewLMStudioProvider(endpoint string) *LMStudioProvider {
    if endpoint == "" {
        endpoint = "http://localhost:1234/v1"
    }
    return &LMStudioProvider{baseURL: endpoint}
}
```

### Requirement: LM Studio Local Provider

The system SHALL provide an LM Studio provider adapter that connects to a local LM Studio instance via its OpenAI-compatible API, supporting local-only execution without a cloud dependency.

#### Scenario: Local-only mode with LM Studio

- **GIVEN** a navi configured with the LM Studio provider and no cloud providers
- **WHEN** the agent makes an LLM call
- **THEN** the call SHALL go to the local LM Studio endpoint
- **AND** no code SHALL be sent to an external server

---

### 3.4 Ollama Adapter

**API:** https://ollama.ai/library  
**Streaming:** SSE (OpenAI-compatible) or non-streaming HTTP  
**Tool Calling:** OpenAI-compatible function calling  

```go
type OllamaProvider struct {
    client   *http.Client
    baseURL  string  // default: http://localhost:11434
}

func NewOllamaProvider(endpoint string) *OllamaProvider {
    if endpoint == "" {
        endpoint = "http://localhost:11434"
    }
    return &OllamaProvider{
        client:  &http.Client{Timeout: 5 * time.Minute},
        baseURL: endpoint,
    }
}

// Ollama uses OpenAI-compatible streaming format
func (p *OllamaProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
    // Convert to OpenAI format and use OpenAI adapter logic
    return (&OpenAIProvider{client: p.client, baseURL: p.baseURL + "/v1"}).Stream(ctx, messages, tools, config)
}

// Ollama supports custom model names (e.g., "qwen2.5-coder:32b", "llama3.1:70b")
func (p *OllamaProvider) Ping(ctx context.Context) error {
    resp, err := p.client.Get(p.baseURL + "/api/tags")
    if err != nil {
        return fmt.Errorf("ollama not reachable at %s: %w", p.baseURL, err)
    }
    defer resp.Body.Close()
    
    var tags struct {
        Models []struct {
            Name string `json:"name"`
        } `json:"models"`
    }
    
    if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
        return fmt.Errorf("failed to parse ollama tags: %w", err)
    }
    
    return nil
}

// Ollama context window varies by model — check model info endpoint
func (p *OllamaProvider) MaxContextWindow(config Config) int {
    // Query /api/show for model details, or use known defaults:
    switch {
    case strings.Contains(config.Model, "qwen2.5"):
        return 32768
    case strings.Contains(config.Model, "llama3"):
        return 8192
    case strings.Contains(config.Model, "codellama"):
        return 16384
    default:
        return 4096 // conservative default
    }
}
```

**Note:** Ollama's tool calling is OpenAI-compatible when using the `/v1` endpoint. For older versions or custom setups, use the native `/api/chat` format.

### Requirement: Ollama Adapter

The system SHALL implement an Ollama provider adapter that communicates with local Ollama instances via the OpenAI-compatible `/v1` endpoint, with configurable base URL defaulting to `http://localhost:11434`.

#### Scenario: Ollama streaming request
- GIVEN an Ollama provider adapter
- WHEN `Stream` is called with messages and tools
- THEN the adapter SHALL delegate to the OpenAI adapter logic using the Ollama `/v1` endpoint

#### Scenario: Ollama connectivity check
- GIVEN an Ollama provider adapter
- WHEN `Ping` is called
- THEN the adapter SHALL query `/api/tags` and return an error if the instance is unreachable

#### Scenario: Ollama context window determination
- GIVEN an Ollama model name
- WHEN `MaxContextWindow` is called
- THEN the adapter SHALL return the known context window for supported model families (Qwen, Llama, CodeLlama) or a conservative default of 4096

---

### 3.5 OpenRouter Adapter

**API:** https://openrouter.ai/docs  
**Streaming:** SSE (OpenAI-compatible)  
**Tool Calling:** Pass-through to underlying provider  

```go
type OpenRouterProvider struct {
    client   *http.Client
    baseURL  string
}

func NewOpenRouterProvider() *OpenRouterProvider {
    return &OpenRouterProvider{
        client:  &http.Client{Timeout: 5 * time.Minute},
        baseURL: "https://openrouter.ai/api/v1",
    }
}

// OpenRouter uses OpenAI-compatible format with extra headers
func (p *OpenRouterProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
    // Add OpenRouter-specific headers
    ctx = context.WithValue(ctx, "x-api-key", config.APIKey)
    ctx = context.WithValue(ctx, "x-title", "OpenNavi")
    
    // Use same logic as OpenAI provider but with different baseURL and headers
    return (&OpenAIProvider{client: p.client, baseURL: p.baseURL}).Stream(ctx, messages, tools, config)
}

// OpenRouter provides model metadata including context window
func (p *OpenRouterProvider) MaxContextWindow(config Config) int {
    // Fetch from /api/v1/models/{model_id} or cache locally
    models := map[string]int{
        "anthropic/claude-sonnet-4":           200000,
        "openai/gpt-4o":                       128000,
        "google/gemini-pro-1.5":               1000000,
    }
    
    if ctx, ok := models[config.Model]; ok {
        return ctx
    }
    
    // Try fetching from API
    resp, err := p.client.Get(p.baseURL + "/models/" + config.Model)
    if err != nil {
        return 4096 // fallback
    }
    defer resp.Body.Close()
    
    var model struct {
        ContextLength int `json:"context_length"`
    }
    
    json.NewDecoder(resp.Body).Decode(&model)
    return model.ContextLength
}
```

**Note:** OpenRouter acts as a proxy to multiple providers. Use it when you want access to many models through one API key, or need fallback routing between providers.

### Requirement: OpenRouter Adapter

The system SHALL implement an OpenRouter provider adapter that communicates with the OpenRouter API via OpenAI-compatible streaming with provider-specific headers, acting as a proxy to multiple underlying providers.

#### Scenario: OpenRouter streaming request
- GIVEN an OpenRouter provider adapter with valid API key
- WHEN `Stream` is called with messages and tools
- THEN the adapter SHALL add OpenRouter-specific headers (`x-api-key`, `x-title`) to the request context
- AND the adapter SHALL delegate to the OpenAI adapter logic using the OpenRouter base URL

#### Scenario: OpenRouter context window resolution
- GIVEN an OpenRouter model identifier
- WHEN `MaxContextWindow` is called
- THEN the adapter SHALL look up the context window from a known model map or fetch it from the `/api/v1/models/{model_id}` endpoint
- AND the adapter SHALL return a fallback of 4096 if the model is unknown and the API call fails

---

### 3.6 MiniMax Adapter

**API:** https://api.minimax.chat  
**Streaming:** SSE  
**Tool Calling:** Function calling (OpenAI-compatible format)  

```go
type MiniMaxProvider struct {
    client   *http.Client
    baseURL  string
}

func NewMiniMaxProvider() *MiniMaxProvider {
    return &MiniMaxProvider{
        client:  &http.Client{Timeout: 5 * time.Minute},
        baseURL: "https://api.minimax.chat/v1",
    }
}

// MiniMax uses OpenAI-compatible format with custom auth headers
func (p *MiniMaxProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
    // Use same OpenAI adapter logic but with MiniMax endpoint and auth
    return (&OpenAIProvider{client: p.client, baseURL: p.baseURL}).Stream(ctx, messages, tools, config)
}

// MiniMax context windows (as of 2026)
func (p *MiniMaxProvider) MaxContextWindow(config Config) int {
    switch {
    case strings.Contains(config.Model, "abab"):
        return 16384
    default:
        return 16384 // conservative default
    }
}
```

**Note:** MiniMax is a Chinese AI company with models optimized for multilingual tasks. Use when you need access to their specific model family or pricing.

### Requirement: MiniMax Adapter

The system SHALL implement a MiniMax provider adapter that communicates with the MiniMax API via OpenAI-compatible streaming with custom auth headers.

#### Scenario: MiniMax streaming request
- GIVEN a MiniMax provider adapter with valid credentials
- WHEN `Stream` is called with messages and tools
- THEN the adapter SHALL delegate to the OpenAI adapter logic using the MiniMax base URL

#### Scenario: MiniMax context window
- GIVEN a MiniMax model name
- WHEN `MaxContextWindow` is called
- THEN the adapter SHALL return a default context window of 16384 for the `abab` model family

---

## 03 Token Counting Strategy

### 4.1 Per-Provider Approaches

| Provider | Method | Library/Approach |
|----------|--------|------------------|
| Anthropic | Built into API response (`usage.input_tokens`, `usage.output_tokens`) | Use provider counts directly |
| OpenAI | tiktoken library with model-specific encoding | `github.com/pkoukk/tiktoken-go` |
| Ollama | Varies by model — use tiktoken for OpenAI models, estimate for others | Hybrid approach |
| OpenRouter | Depends on underlying provider | Use underlying provider's method |
| MiniMax | Estimate using chars-per-token ratio or vendor API | Heuristic estimation |

### 4.2 Unified Token Counter Interface

```go
type TokenCounter interface {
    CountTokens(messages []Message) int
    
    // CountText returns approximate token count for a string
    CountText(text string) int
}

// tiktoken-based counter for OpenAI-compatible models
type TikTokenCounter struct {
    encoder *tiktoken.Encoder
}

func NewTikTokenCounter(modelName string) (*TikTokenCounter, error) {
    encoding := "cl100k_base" // default for GPT-4
    
    if strings.HasPrefix(modelName, "gpt-") || strings.HasPrefix(modelName, "o1-") {
        encoding = "cl100k_base"
    } else if strings.HasPrefix(modelName, "text-") {
        encoding = "p50k_base"
    }
    
    enc, err := tiktoken.GetEncoding(encoding)
    if err != nil {
        return nil, fmt.Errorf("failed to get tokenizer for %s: %w", modelName, err)
    }
    
    return &TikTokenCounter{encoder: enc}, nil
}

func (c *TikTokenCounter) CountTokens(messages []Message) int {
    total := 0
    
    // Count special tokens (role prefixes, etc.)
    for _, msg := range messages {
        switch msg.Role {
        case "system":
            total += 4 // system message overhead
        case "user", "assistant":
            total += 3 // user/assistant message overhead
        case "tool":
            total += 3 // tool result overhead
        }
        
        total += len(c.encoder.Encode(msg.Content, nil, nil))
    }
    
    return total
}

func (c *TikTokenCounter) CountText(text string) int {
    tokens := c.encoder.Encode(text, nil, nil)
    return len(tokens)
}
```

### 4.3 Heuristic Estimation for Non-tiktoken Models

For providers without tiktoken support (MiniMax, some Ollama models):

```go
// Rough heuristic: ~4 characters per token for English text
func EstimateTokens(text string) int {
    return len([]rune(text)) / 4
}

// For tool outputs with structured data, use a higher ratio (~3 chars/token)
func EstimateToolOutputTokens(text string) int {
    return len([]rune(text)) / 3
}
```

### Requirement: Per-Provider Token Counting

The system SHALL use a per-provider token counting strategy that leverages built-in API counts for Anthropic, the `tiktoken` Go library for OpenAI-compatible models, and heuristic estimation for MiniMax and non-tiktoken Ollama models.

#### Scenario: Anthropic token counting
- GIVEN an Anthropic streaming response
- WHEN token usage information is available in stream events
- THEN the system SHALL use the provider's `usage.input_tokens` and `usage.output_tokens` directly

#### Scenario: OpenAI token counting
- GIVEN an OpenAI-compatible model
- WHEN counting tokens for a message sequence
- THEN the system SHALL use the `tiktoken` Go library with the model-specific encoding
- AND the system SHALL account for message format overhead (role prefixes, delimiters)

### Requirement: Unified Token Counter Interface

The system SHALL define a `TokenCounter` interface with `CountTokens` and `CountText` methods that provides a unified API for token estimation across all providers.

#### Scenario: Token counter for message sequences
- GIVEN a sequence of messages with various roles
- WHEN `CountTokens` is called
- THEN the system SHALL return the total estimated token count including per-message overhead

#### Scenario: Token counter for plain text
- GIVEN a plain text string
- WHEN `CountText` is called
- THEN the system SHALL return the approximate token count for the string

### Requirement: Heuristic Token Estimation

The system SHALL provide heuristic token estimation for providers without tiktoken support, using approximately 4 characters per token for English text and 3 characters per token for structured tool output.

#### Scenario: Heuristic text estimation
- GIVEN a text string without a tiktoken encoding
- WHEN estimating tokens
- THEN the system SHALL divide the rune length by 4

#### Scenario: Heuristic tool output estimation
- GIVEN a structured tool output string
- WHEN estimating tokens
- THEN the system SHALL divide the rune length by 3

---

## 04 Tool Calling Format Normalization

Different providers use different formats for function calling. The provider layer normalizes them to OpenNavi's internal format:

### 5.1 Input Normalization (OpenNavi → Provider)

```go
func NormalizeTool(tool AgentTool, provider string) any {
    switch provider {
    case "anthropic":
        return map[string]any{
            "name":         tool.Name,
            "description":  tool.Description,
            "input_schema": json.RawMessage(tool.Parameters),
        }
    
    case "openai", "ollama", "openrouter", "minimax":
        return map[string]any{
            "type": "function",
            "function": map[string]any{
                "name":        tool.Name,
                "description": tool.Description,
                "parameters":  json.RawMessage(tool.Parameters),
            },
        }
    
    default:
        return nil
    }
}
```

### 5.2 Output Normalization (Provider → OpenNavi)

```go
type NormalizedToolCall struct {
    ID        string
    Name      string
    Arguments map[string]any
}

func NormalizeToolCalls(raw any, provider string) ([]NormalizedToolCall, error) {
    switch provider {
    case "anthropic":
        // Anthropic: [{id, name, input}]
        var anthropicCalls []struct {
            ID   string          `json:"id"`
            Name string          `json:"name"`
            Input json.RawMessage `json:"input"`
        }
        
        if err := json.Unmarshal(raw.(json.RawMessage), &anthropicCalls); err != nil {
            return nil, err
        }
        
        var result []NormalizedToolCall
        for _, call := range anthropicCalls {
            var args map[string]any
            json.Unmarshal(call.Input, &args)
            
            result = append(result, NormalizedToolCall{
                ID:        call.ID,
                Name:      call.Name,
                Arguments: args,
            })
        }
        
        return result, nil
    
    case "openai", "ollama", "openrouter", "minimax":
        // OpenAI: [{id, type="function", function:{name, arguments}}]
        var openaicalls []struct {
            ID       string `json:"id"`
            Type     string `json:"type"`
            Function struct {
                Name      string          `json:"name"`
                Arguments json.RawMessage `json:"arguments"`
            } `json:"function"`
        }
        
        if err := json.Unmarshal(raw.(json.RawMessage), &openaicalls); err != nil {
            return nil, err
        }
        
        var result []NormalizedToolCall
        for _, call := range openaicalls {
            var args map[string]any
            json.Unmarshal(call.Function.Arguments, &args)
            
            result = append(result, NormalizedToolCall{
                ID:        call.ID,
                Name:      call.Function.Name,
                Arguments: args,
            })
        }
        
        return result, nil
    
    default:
        return nil, fmt.Errorf("unknown provider: %s", provider)
    }
}
```

### Requirement: Input Normalization (OpenNavi to Provider)

The system SHALL normalize tool definitions from the OpenNavi internal format to each provider's specific format, using the Anthropic `input_schema` format for Anthropic and the OpenAI function-calling format for OpenAI, Ollama, OpenRouter, and MiniMax.

#### Scenario: Input normalization for Anthropic
- GIVEN an OpenNavi tool definition
- WHEN converting to the Anthropic format
- THEN the system SHALL produce a map with `name`, `description`, and `input_schema` fields

#### Scenario: Input normalization for OpenAI-compatible providers
- GIVEN an OpenNavi tool definition
- WHEN converting to the OpenAI, Ollama, OpenRouter, or MiniMax format
- THEN the system SHALL produce a map with `type: "function"` and a `function` object containing `name`, `description`, and `parameters`

### Requirement: Output Normalization (Provider to OpenNavi)

The system SHALL normalize tool call results from each provider's specific format into a unified `NormalizedToolCall` struct with `ID`, `Name`, and parsed `Arguments` fields.

#### Scenario: Output normalization for Anthropic
- GIVEN an Anthropic tool call response with `id`, `name`, and `input` fields
- WHEN normalizing to the OpenNavi format
- THEN the system SHALL parse the `input` JSON into a `map[string]any` and produce a `NormalizedToolCall`

#### Scenario: Output normalization for OpenAI-compatible providers
- GIVEN an OpenAI-style tool call response with `id`, `type: "function"`, and `function: {name, arguments}`
- WHEN normalizing to the OpenNavi format
- THEN the system SHALL parse the `arguments` JSON into a `map[string]any` and produce a `NormalizedToolCall`

#### Scenario: Unknown provider handling
- GIVEN a provider name that is not supported
- WHEN normalizing tool calls
- THEN the system SHALL return an error indicating the unknown provider

---

## 05 Error Handling and Retry Logic

### 6.1 Provider-Specific Errors

```go
type ProviderError struct {
    Provider string
    Code     int    // HTTP status or provider-specific code
    Message  string
    IsRetryable bool
}

func (e *ProviderError) Error() string {
    return fmt.Sprintf("[%s] %d: %s", e.Provider, e.Code, e.Message)
}

// Anthropic errors
func isAnthropicRetryable(code int, message string) bool {
    switch code {
    case 429: // Rate limit
        return true
    case 500: // Server error
        return true
    case 503: // Service unavailable
        return true
    }
    
    if strings.Contains(message, "rate_limit") {
        return true
    }
    
    return false
}

// OpenAI errors
func isOpenAIRetryable(code int) bool {
    switch code {
    case 429: // Rate limit
        return true
    case 500, 502, 503, 504: // Server/network errors
        return true
    }
    return false
}
```

### 6.2 Retry Configuration

```go
type RetryConfig struct {
    MaxRetries       int           `json:"max_retries"`
    InitialDelay     time.Duration `json:"initial_delay"`
    MaxDelay         time.Duration `json:"max_delay"`
    BackoffFactor    float64       `json:"backoff_factor"`
    JitterEnabled    bool          `json:"jitter_enabled"`
}

var DefaultRetryConfig = RetryConfig{
    MaxRetries:    3,
    InitialDelay:  500 * time.Millisecond,
    MaxDelay:      10 * time.Second,
    BackoffFactor: 2.0,
    JitterEnabled: true,
}

func (c *RetryConfig) ShouldRetry(err error, attempt int) bool {
    if attempt >= c.MaxRetries {
        return false
    }
    
    var providerErr *ProviderError
    if errors.As(err, &providerErr) {
        switch providerErr.Provider {
        case "anthropic":
            return isAnthropicRetryable(providerErr.Code, providerErr.Message)
        case "openai", "ollama", "openrouter", "minimax":
            return isOpenAIRetryable(providerErr.Code)
        }
    }
    
    // Network errors are always retryable
    if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
        return true
    }
    
    return false
}

func (c *RetryConfig) Delay(attempt int) time.Duration {
    delay := float64(c.InitialDelay) * math.Pow(c.BackoffFactor, float64(attempt))
    if delay > float64(c.MaxDelay) {
        delay = float64(c.MaxDelay)
    }
    
    if c.JitterEnabled {
        jitter := time.Duration(rand.Int63n(int64(delay) / 2))
        return time.Duration(delay) + jitter
    }
    
    return time.Duration(delay)
}
```

### Requirement: Provider-Specific Error Classification

The system SHALL classify provider errors by provider and HTTP status code, with retryable flags for Anthropic (429, 500, 503, rate_limit message) and OpenAI-compatible providers (429, 500, 502, 503, 504).

#### Scenario: Anthropic retryable error classification
- GIVEN an Anthropic provider error with a 429, 500, or 503 status code, or a message containing "rate_limit"
- WHEN `isAnthropicRetryable` is called
- THEN the system SHALL return `true`

#### Scenario: OpenAI retryable error classification
- GIVEN an OpenAI-compatible provider error with a 429, 500, 502, 503, or 504 status code
- WHEN `isOpenAIRetryable` is called
- THEN the system SHALL return `true`

#### Scenario: Network error classification
- GIVEN a network timeout error
- WHEN `ShouldRetry` is called
- THEN the system SHALL return `true` regardless of provider

### Requirement: Retry with Exponential Backoff and Jitter

The system SHALL implement retry logic with configurable maximum retries (default 3), exponential backoff (initial delay 500ms, factor 2.0, max 10s), and optional jitter.

#### Scenario: Retry attempt limit
- GIVEN an error that has been retried the maximum number of times
- WHEN `ShouldRetry` is called
- THEN the system SHALL return `false` and not retry further

#### Scenario: Exponential backoff delay
- GIVEN a retry attempt number
- WHEN `Delay` is called
- THEN the system SHALL compute the delay as `initialDelay * backoffFactor^attempt`
- AND the system SHALL cap the delay at the maximum configured delay
- AND if jitter is enabled, the system SHALL add a random jitter of up to half the computed delay

---

## 06 Configuration and Defaults

### 7.1 Provider Registry

```go
type ProviderRegistry struct {
    providers map[string]Provider
}

func NewProviderRegistry() *ProviderRegistry {
    return &ProviderRegistry{
        providers: map[string]Provider{
            "anthropic":  &AnthropicProvider{},
            "openai":     &OpenAIProvider{},
            "ollama":     NewOllamaProvider(""),
            "openrouter": NewOpenRouterProvider(),
            "minimax":    NewMiniMaxProvider(),
        },
    }
}

func (r *ProviderRegistry) Get(name string) (Provider, error) {
    p, ok := r.providers[name]
    if !ok {
        return nil, fmt.Errorf("unknown provider: %s", name)
    }
    
    // Initialize with config
    switch p := p.(type) {
    case *OllamaProvider:
        p.baseURL = "http://localhost:11434"
    case *OpenRouterProvider:
        p.baseURL = "https://openrouter.ai/api/v1"
    }
    
    return p, nil
}

func (r *ProviderRegistry) List() []string {
    names := make([]string, 0, len(r.providers))
    for name := range r.providers {
        names = append(names, name)
    }
    sort.Strings(names)
    return names
}
```

### 7.2 Default Configuration per Provider

```go
func DefaultConfig(provider string) Config {
    switch provider {
    case "anthropic":
        return Config{
            Provider:  "anthropic",
            Model:     "claude-sonnet-4-20250514",
            Temperature: 0.7,
            MaxTokens:   8192,
        }
    
    case "openai":
        return Config{
            Provider:  "openai",
            Model:     "gpt-4o",
            Temperature: 0.7,
            MaxTokens:   4096,
        }
    
    case "ollama":
        return Config{
            Provider:  "ollama",
            Endpoint:  "http://localhost:11434",
            Model:     "qwen2.5-coder:32b",
            Temperature: 0.7,
            MaxTokens:   4096,
        }
    
    case "openrouter":
        return Config{
            Provider:  "openrouter",
            Model:     "anthropic/claude-sonnet-4",
            Temperature: 0.7,
            MaxTokens:   8192,
        }
    
    case "minimax":
        return Config{
            Provider:  "minimax",
            Model:     "abab6.5s",
            Temperature: 0.7,
            MaxTokens:   4096,
        }
    
    default:
        return DefaultConfig("anthropic")
    }
}

// Resolve API key from environment or config
func ResolveAPIKey(config Config) (string, error) {
    if config.APIKey != "" {
        return os.ExpandEnv(config.APIKey), nil
    }
    
    switch config.Provider {
    case "anthropic":
        return os.Getenv("ANTHROPIC_API_KEY"), nil
    case "openai":
        return os.Getenv("OPENAI_API_KEY"), nil
    case "ollama":
        // Ollama typically doesn't need an API key for local models
        return "", nil
    case "openrouter":
        return os.Getenv("OPENROUTER_API_KEY"), nil
    case "minimax":
        return os.Getenv("MINIMAX_API_KEY"), nil
    default:
        return "", fmt.Errorf("unknown provider: %s", config.Provider)
    }
}
```

### Requirement: Provider Registry

The system SHALL maintain a provider registry that maps provider names to initialized provider instances, with a `Get` method that returns the provider and a `List` method that returns sorted provider names.

#### Scenario: Get provider by name
- GIVEN a provider registry with registered providers
- WHEN `Get` is called with a known provider name
- THEN the system SHALL return the initialized provider instance
- AND the system SHALL ensure default endpoint values are set for Ollama and OpenRouter

#### Scenario: Get unknown provider
- GIVEN a provider registry
- WHEN `Get` is called with an unknown provider name
- THEN the system SHALL return an error indicating the provider is unknown

#### Scenario: List registered providers
- GIVEN a provider registry with registered providers
- WHEN `List` is called
- THEN the system SHALL return a sorted list of provider names

### Requirement: Default Configuration per Provider

The system SHALL provide default configurations for each provider with sensible defaults for model, temperature, and max tokens, defaulting to Anthropic for unknown provider names.

#### Scenario: Default configuration for Anthropic
- GIVEN the provider name "anthropic"
- WHEN `DefaultConfig` is called
- THEN the system SHALL return a config with model `claude-sonnet-4-20250514`, temperature 0.7, and max tokens 8192

#### Scenario: Default configuration for Ollama
- GIVEN the provider name "ollama"
- WHEN `DefaultConfig` is called
- THEN the system SHALL return a config with endpoint `http://localhost:11434`, model `qwen2.5-coder:32b`, temperature 0.7, and max tokens 4096

#### Scenario: Default configuration for unknown provider
- GIVEN an unknown provider name
- WHEN `DefaultConfig` is called
- THEN the system SHALL return the Anthropic default configuration

### Requirement: API Key Resolution

The system SHALL resolve API keys from environment variables by provider, with support for inline environment variable expansion and explicit per-request override.

#### Scenario: API key from environment variable
- GIVEN a provider config without an explicit API key
- WHEN `ResolveAPIKey` is called
- THEN the system SHALL read the appropriate environment variable (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `MINIMAX_API_KEY`)

#### Scenario: API key from config
- GIVEN a provider config with an explicit API key containing environment variable references
- WHEN `ResolveAPIKey` is called
- THEN the system SHALL expand environment variables in the key value

#### Scenario: Ollama API key resolution
- GIVEN an Ollama provider config
- WHEN `ResolveAPIKey` is called
- THEN the system SHALL return an empty string since Ollama typically does not require an API key

---

## 07 Provider Selection and Fallback

### 8.1 Primary + Fallback Configuration

```yaml
# ~/.opennavi/profiles/<name>/providers.yaml
llm:
  primary: anthropic
  fallback: openrouter
  
  # Per-provider configuration
  providers:
    anthropic:
      model: "claude-sonnet-4-20250514"
      api_key: "${ANTHROPIC_API_KEY}"
    
    openrouter:
      model: "anthropic/claude-sonnet-4"  # proxy to Anthropic via OpenRouter
      api_key: "${OPENROUTER_API_KEY}"
      
    ollama:
      endpoint: "http://localhost:11434"
      model: "qwen2.5-coder:32b"
```

### 8.2 Fallback Logic

```go
type ProviderFallback struct {
    primary   Provider
    fallback  Provider
    config    RetryConfig
}

func (f *ProviderFallback) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
    var lastErr error
    
    // Try primary provider
    events, err := f.primary.Stream(ctx, messages, tools, config)
    if err == nil {
        return events, nil
    }
    
    lastErr = err
    
    // Check if retryable on primary
    if f.config.ShouldRetry(err, 0) {
        time.Sleep(f.config.Delay(0))
        
        events, err = f.primary.Stream(ctx, messages, tools, config)
        if err == nil {
            return events, nil
        }
        
        lastErr = err
    }
    
    // Fall back to secondary provider if configured
    if f.fallback != nil {
        log.Printf("primary provider failed (%v), falling back to %s", err, f.fallback.Name())
        fallbackConfig := config
        fallbackConfig.Provider = f.fallback.Name()
        
        return f.fallback.Stream(ctx, messages, tools, fallbackConfig)
    }
    
    return nil, fmt.Errorf("all providers unavailable: %w", lastErr)
}
```

### Requirement: Primary and Fallback Provider Configuration

The system SHALL support a primary-plus-fallback provider configuration where users specify a primary provider and an optional fallback provider in a YAML configuration file.

#### Scenario: Primary provider configuration
- GIVEN a provider configuration with a primary provider specified
- WHEN the system initiates an LLM request
- THEN the system SHALL attempt the request using the primary provider first

#### Scenario: Fallback provider configuration
- GIVEN a provider configuration with both primary and fallback providers
- WHEN the primary provider fails with a non-retryable error or after exhausting retries
- THEN the system SHALL switch to the fallback provider for the request

### Requirement: Fallback Execution Logic

The system SHALL implement fallback execution logic that attempts the primary provider, retries on retryable errors, and falls back to the secondary provider if configured and the primary fails.

#### Scenario: Primary provider succeeds
- GIVEN a primary provider that responds successfully
- WHEN `Stream` is called on the fallback wrapper
- THEN the system SHALL return the stream events from the primary provider

#### Scenario: Primary provider fails with retryable error
- GIVEN a primary provider that returns a retryable error
- WHEN the first attempt fails
- THEN the system SHALL wait for the configured retry delay and retry the primary provider
- AND if the retry succeeds, the system SHALL return the stream events from the primary provider

#### Scenario: Primary provider exhausted, fallback to secondary
- GIVEN a primary provider that fails after exhausting retries
- WHEN a fallback provider is configured
- THEN the system SHALL log the failure and fall back to the secondary provider
- AND the system SHALL update the provider in the config to the fallback provider name

#### Scenario: All providers unavailable
- GIVEN a primary provider that fails and no fallback provider is configured
- WHEN all attempts are exhausted
- THEN the system SHALL return an error indicating all providers are unavailable

---

## 08 Implementation Order

1. **Provider interface** — Define `llm.Provider` contract with Stream/Complete/Ping methods
2. **Anthropic adapter** — Full implementation with streaming and tool calling
3. **OpenAI adapter** — OpenAI-compatible format, tiktoken integration
4. **Ollama adapter** — Local model support via /v1 endpoint
5. **OpenRouter adapter** — Proxy to multiple providers
6. **MiniMax adapter** — Chinese market access
7. **Token counting** — Per-provider strategies with unified interface
8. **Tool calling normalization** — Input/output conversion between formats
9. **Error handling and retry** — Provider-specific error classification and backoff
10. **Fallback configuration** — Primary + fallback provider setup

---

## 09 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Unified stream interface** — All providers return `<-chan StreamEvent` | Consistent handling in agent loop regardless of provider |
| **OpenAI-compatible as default format** — Most providers support OpenAI format | Minimizes conversion logic, especially for Ollama/OpenRouter/MiniMax |
| **Anthropic native format** — Anthropic uses unique tool_use blocks and SSE events | Must handle specially due to different event structure |
| **tiktoken for token counting** — Industry-standard library with model-specific encodings | Accurate counts across OpenAI-compatible models |
| **Heuristic estimation fallback** — ~4 chars/token for non-tiktoken providers | Works well enough for budgeting when exact counts aren't available |
| **Provider-specific retry logic** — Each provider has different rate limit and error patterns | Prevents unnecessary retries on permanent errors (auth, model not found) |
| **Primary + fallback configuration** — Users can specify a fallback provider | Ensures availability when primary is down or rate-limited |
| **Configurable endpoints for Ollama/OpenRouter** — Not all deployments use defaults | Supports custom Ollama instances and OpenRouter with custom routing |


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S070000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |
| 2026-08-15 | S070000 | 0.2.0 | Added LM Studio local provider adapter (070010) |