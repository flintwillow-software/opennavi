package agent

import (
	"encoding/json"
	"sync"
)

// ── Tool Executor ────────────────────────────────────────────────────────

// ToolExecutor executes tool calls in parallel or sequential mode.
type ToolExecutor struct {
	mu          sync.RWMutex
	tools       map[string]func(map[string]any) (string, error)
	mode        string
	maxParallel int
}

// NewToolExecutor creates a ToolExecutor with the given mode and parallel limit.
func NewToolExecutor(mode string, maxParallel int) *ToolExecutor {
	return &ToolExecutor{
		tools:       make(map[string]func(map[string]any) (string, error)),
		mode:        mode,
		maxParallel: maxParallel,
	}
}

// RegisterTool adds a tool implementation by name.
func (e *ToolExecutor) RegisterTool(name string, handler func(map[string]any) (string, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tools[name] = handler
}

// Tools returns a snapshot of registered tool descriptors.
func (e *ToolExecutor) Tools() []AgentTool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	tools := make([]AgentTool, 0, len(e.tools))
	for name := range e.tools {
		tools = append(tools, AgentTool{Name: name, Description: name})
	}
	return tools
}

// Execute calls the registered tools according to the mode.
func (e *ToolExecutor) Execute(calls []ToolCall) []ToolResult {
	results := make([]ToolResult, len(calls))
	if e.mode == "sequential" {
		for i, call := range calls {
			results[i] = e.runTool(call)
		}
		return results
	}
	// parallel
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

func (e *ToolExecutor) runTool(call ToolCall) ToolResult {
	e.mu.RLock()
	fn, ok := e.tools[call.Name]
	e.mu.RUnlock()
	if !ok {
		return ToolResult{ToolCallID: call.Name, IsError: true, Result: "unknown tool"}
	}
	input := make(map[string]any)
	if call.Arguments != nil {
		if err := json.Unmarshal(call.Arguments, &input); err != nil {
			return ToolResult{ToolCallID: call.Name, IsError: true, Result: "invalid arguments: " + err.Error()}
		}
	}
	result, err := fn(input)
	if err != nil {
		return ToolResult{ToolCallID: call.Name, Result: result, IsError: true}
	}
	return ToolResult{ToolCallID: call.Name, Result: result}
}
