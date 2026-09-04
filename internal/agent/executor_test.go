package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecutorSequentialOrder(t *testing.T) {
	ex := NewToolExecutor("sequential", 4)
	var mu sync.Mutex
	var order []string
	for _, name := range []string{"t1", "t2", "t3"} {
		n := name
		ex.RegisterTool(AgentTool{Name: n}, func(map[string]any) (string, error) {
			mu.Lock()
			order = append(order, n)
			mu.Unlock()
			return n + " done", nil
		})
	}
	results := ex.Execute([]ToolCall{
		{Name: "t1"}, {Name: "t2"}, {Name: "t3"},
	})
	for i, want := range []string{"t1", "t2", "t3"} {
		if order[i] != want {
			t.Fatalf("execution order = %v, want sequential %v", order, []string{"t1", "t2", "t3"})
		}
		if results[i].Result != want+" done" || results[i].IsError {
			t.Fatalf("result[%d] = %+v, want %q ok", i, results[i], want+" done")
		}
	}
}

func TestExecutorParallelBounded(t *testing.T) {
	const maxP = 2
	ex := NewToolExecutor("parallel", maxP)
	var current, peak int64
	release := make(chan struct{})
	for i := 0; i < 6; i++ {
		ex.RegisterTool(AgentTool{Name: fmt.Sprintf("t%d", i)}, func(map[string]any) (string, error) {
			cur := atomic.AddInt64(&current, 1)
			for {
				p := atomic.LoadInt64(&peak)
				if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
					break
				}
			}
			<-release
			atomic.AddInt64(&current, -1)
			return "ok", nil
		})
	}
	var calls []ToolCall
	for i := 0; i < 6; i++ {
		calls = append(calls, ToolCall{Name: fmt.Sprintf("t%d", i)})
	}

	done := make(chan []ToolResult)
	go func() { done <- ex.Execute(calls) }()

	// Wait until the executor is saturated (or fail on timeout).
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&peak) < maxP {
		select {
		case <-deadline:
			t.Fatalf("peak concurrency = %d, never reached %d", atomic.LoadInt64(&peak), maxP)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if p := atomic.LoadInt64(&peak); p > maxP {
		t.Fatalf("peak concurrency = %d, exceeded maxParallel %d", p, maxP)
	}
	close(release)
	results := <-done
	if len(results) != 6 {
		t.Fatalf("got %d results, want 6", len(results))
	}
	for _, r := range results {
		if r.IsError || r.Result != "ok" {
			t.Fatalf("bad result: %+v", r)
		}
	}
}

func TestExecutorParallelDefaultLimit(t *testing.T) {
	ex := NewToolExecutor("parallel", 0)
	if ex.MaxParallel() != 4 {
		t.Fatalf("MaxParallel() = %d, want default 4", ex.MaxParallel())
	}
}

func TestExecutorUnknownTool(t *testing.T) {
	ex := NewToolExecutor("parallel", 2)
	results := ex.Execute([]ToolCall{{Name: "nope"}})
	if !results[0].IsError {
		t.Fatal("unknown tool should be an error result")
	}
	if results[0].Result == "" {
		t.Fatal("error result should carry a message")
	}
}

func TestExecutorInvalidArguments(t *testing.T) {
	ex := NewToolExecutor("parallel", 1)
	ex.RegisterTool(AgentTool{Name: "t"}, func(map[string]any) (string, error) {
		return "ran", nil
	})
	results := ex.Execute([]ToolCall{{Name: "t", Arguments: json.RawMessage(`not-json`)}})
	if !results[0].IsError {
		t.Fatal("invalid arguments should be an error result")
	}
}

func TestExecutorToolError(t *testing.T) {
	ex := NewToolExecutor("parallel", 1)
	ex.RegisterTool(AgentTool{Name: "t"}, func(map[string]any) (string, error) {
		return "partial output", fmt.Errorf("boom")
	})
	results := ex.Execute([]ToolCall{{Name: "t"}})
	if !results[0].IsError {
		t.Fatal("handler error should be an error result")
	}
	if results[0].Result != "partial output" {
		t.Fatalf("partial output should be preserved, got %q", results[0].Result)
	}
}

func TestExecutorToolsDescriptors(t *testing.T) {
	ex := NewToolExecutor("parallel", 2)
	ex.RegisterTool(AgentTool{Name: "b", Description: "second"}, func(map[string]any) (string, error) { return "", nil })
	ex.RegisterTool(AgentTool{Name: "a", Description: "first"}, func(map[string]any) (string, error) { return "", nil })
	tools := ex.Tools()
	if len(tools) != 2 {
		t.Fatalf("got %d descriptors, want 2", len(tools))
	}
	if tools[0].Name != "b" || tools[0].Description != "second" {
		t.Fatalf("descriptors not in registration order or missing description: %+v", tools)
	}
}
