package agent

import (
	"encoding/json"
	"sync"
)

// toolEntry pairs a tool descriptor with its implementation.
type toolEntry struct {
	descriptor AgentTool
	run        func(map[string]any) (string, error)
}

// ToolExecutor executes tool calls in parallel or sequential mode.
type ToolExecutor struct {
	mu          sync.RWMutex
	tools       map[string]*toolEntry
	order       []string
	mode        string
	maxParallel int
}

// NewToolExecutor creates a ToolExecutor with the given mode and parallel limit.
func NewToolExecutor(mode string, maxParallel int) *ToolExecutor {
	if maxParallel <= 0 {
		maxParallel = 4 // spec default
	}
	return &ToolExecutor{
		tools:       make(map[string]*toolEntry),
		mode:        mode,
		maxParallel: maxParallel,
	}
}

// RegisterTool adds a tool descriptor and its implementation by name.
func (e *ToolExecutor) RegisterTool(tool AgentTool, handler func(map[string]any) (string, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.tools[tool.Name]; !exists {
		e.order = append(e.order, tool.Name)
	}
	e.tools[tool.Name] = &toolEntry{descriptor: tool, run: handler}
}

// Tools returns the registered tool descriptors in registration order.
func (e *ToolExecutor) Tools() []AgentTool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	tools := make([]AgentTool, 0, len(e.order))
	for _, name := range e.order {
		tools = append(tools, e.tools[name].descriptor)
	}
	return tools
}

// Execute calls the registered tools according to the mode: sequential
// tools run one at a time in order, parallel tools run concurrently
// bounded by the semaphore.
func (e *ToolExecutor) Execute(calls []ToolCall) []ToolResult {
	results := make([]ToolResult, len(calls))
	if e.mode == "sequential" {
		for i, call := range calls {
			results[i] = e.runTool(call)
		}
		return results
	}
	// parallel with bounded concurrency
	var wg sync.WaitGroup
	sem := make(chan struct{}, e.maxParallel)
	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = e.runTool(c)
		}(i, call)
	}
	wg.Wait()
	return results
}

// MaxParallel returns the configured parallel limit.
func (e *ToolExecutor) MaxParallel() int {
	return e.maxParallel
}

// Mode returns the execution mode ("parallel" or "sequential").
func (e *ToolExecutor) Mode() string {
	return e.mode
}

func (e *ToolExecutor) runTool(call ToolCall) ToolResult {
	e.mu.RLock()
	entry, ok := e.tools[call.Name]
	e.mu.RUnlock()
	if !ok {
		return ToolResult{ToolCallID: call.ToolCallID, IsError: true, Result: "unknown tool: " + call.Name}
	}
	input := make(map[string]any)
	if call.Arguments != nil {
		if err := json.Unmarshal(call.Arguments, &input); err != nil {
			return ToolResult{ToolCallID: call.ToolCallID, IsError: true, Result: "invalid arguments: " + err.Error()}
		}
	}
	result, err := entry.run(input)
	if err != nil {
		if result == "" {
			result = err.Error()
		}
		return ToolResult{ToolCallID: call.ToolCallID, Result: result, IsError: true}
	}
	return ToolResult{ToolCallID: call.ToolCallID, Result: result}
}
