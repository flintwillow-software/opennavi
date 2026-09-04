package agent

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// collectEvents subscribes a recorder for non-once events and returns a
// func yielding the event types observed so far.
func collectEvents(a *Agent) func() []AgentEventType {
	var mu sync.Mutex
	var types []AgentEventType
	a.Subscribe(func(ev AgentEvent) {
		mu.Lock()
		types = append(types, ev.Type)
		mu.Unlock()
	}, false)
	return func() []AgentEventType {
		mu.Lock()
		defer mu.Unlock()
		return append([]AgentEventType(nil), types...)
	}
}

func waitForCondition(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestRunLoopTextTurn(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	a.Provider = &mockProvider{responses: [][]Event{{
		{Type: EventMessageStart},
		{Type: EventContentDelta, Content: "Hello "},
		{Type: EventContentDelta, Content: "world"},
		{Type: EventMessageEnd},
		{Type: EventStop},
	}}}

	var deltas []string
	var mu sync.Mutex
	a.Subscribe(func(ev AgentEvent) {
		if ev.Type == AgentEventContentDelta && ev.Message != nil {
			mu.Lock()
			deltas = append(deltas, ev.Message.Content)
			mu.Unlock()
		}
	}, false)

	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	st := a.State()
	if st.Status != StatusIdle {
		t.Fatalf("status = %q, want idle", st.Status)
	}
	if len(st.Messages) != 2 {
		t.Fatalf("got %d messages, want 2 (user + assistant)", len(st.Messages))
	}
	last := st.Messages[1]
	if last.Role != RoleAssistant || last.Content != "Hello world" || last.IsStreaming {
		t.Fatalf("assistant message = %+v, want 'Hello world' not streaming", last)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deltas) != 2 || deltas[0] != "Hello " || deltas[1] != "Hello world" {
		t.Fatalf("content_delta events carried cumulative content: %v", deltas)
	}
}

func TestRunLoopToolCallTurn(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	mp := &mockProvider{responses: [][]Event{
		{ // turn 1: model requests a tool
			{Type: EventMessageStart},
			{Type: EventToolCall, Tool: &ToolUpdate{Name: "test_tool", Input: []byte(`{"key":"value"}`)}},
			{Type: EventMessageEnd},
			{Type: EventStop},
		},
		{ // turn 2: model responds to the observation
			{Type: EventContentDelta, Content: "observation received"},
			{Type: EventStop},
		},
	}}
	a.Provider = mp

	var toolRan bool
	a.Executor.RegisterTool(AgentTool{Name: "test_tool"}, func(args map[string]any) (string, error) {
		toolRan = args["key"] == "value"
		return "tool output 42", nil
	})

	var toolResultEvents []ToolUpdate
	var mu sync.Mutex
	a.Subscribe(func(ev AgentEvent) {
		if ev.Type == AgentEventToolResult && ev.Tool != nil {
			mu.Lock()
			toolResultEvents = append(toolResultEvents, *ev.Tool)
			mu.Unlock()
		}
	}, false)

	if err := a.Prompt("do the thing", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	if !toolRan {
		t.Fatal("tool did not run or received wrong arguments")
	}
	st := a.State()
	if st.Status != StatusIdle {
		t.Fatalf("status = %q, want idle", st.Status)
	}
	// user, assistant(tool calls), tool result, assistant final
	if len(st.Messages) != 4 {
		t.Fatalf("got %d messages, want 4: %+v", len(st.Messages), st.Messages)
	}
	if len(st.Messages[1].ToolCalls) != 1 || st.Messages[1].ToolCalls[0].Name != "test_tool" {
		t.Fatalf("assistant tool calls = %+v", st.Messages[1].ToolCalls)
	}
	if st.Messages[2].Role != RoleTool || st.Messages[2].ToolResult != "tool output 42" {
		t.Fatalf("tool result message = %+v", st.Messages[2])
	}
	if st.Messages[3].Content != "observation received" {
		t.Fatalf("final assistant message = %+v", st.Messages[3])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(toolResultEvents) != 1 || toolResultEvents[0].Result != "tool output 42" {
		t.Fatalf("tool_result events = %+v", toolResultEvents)
	}
}

func TestRunLoopProviderErrorEvent(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	a.Provider = &mockProvider{responses: [][]Event{{
		{Type: EventError, Error: "model exploded"},
	}}}

	errCh := make(chan error, 1)
	a.Subscribe(func(ev AgentEvent) {
		if ev.Type == AgentEventError {
			errCh <- ev.Error
		}
	}, false)

	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "model exploded") {
			t.Fatalf("error = %v, want provider error surfaced", err)
		}
	default:
		t.Fatal("error event not fired")
	}
	if st := a.State(); st.Status != StatusError {
		t.Fatalf("status = %q, want error", st.Status)
	}
}

func TestRunLoopStreamError(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	a.Provider = &mockProvider{streamErr: errTest{}}

	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()
	if st := a.State(); st.Status != StatusError {
		t.Fatalf("status = %q, want error", st.Status)
	}
}

type errTest struct{}

func (errTest) Error() string { return "connection refused" }

func TestRunLoopSteeringInjected(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	mp := &mockProvider{responses: [][]Event{{
		{Type: EventContentDelta, Content: "ok"},
		{Type: EventStop},
	}}}
	a.Provider = mp

	a.Steer("mid-run correction")
	if err := a.Prompt("start", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	req := mp.lastRequest()
	// Both the prompt and the steering message should reach the LLM.
	if len(req) != 2 || FlattenContent(req[0].Content) != "start" || FlattenContent(req[1].Content) != "mid-run correction" {
		t.Fatalf("LLM messages = %+v, want steering injected after prompt", req)
	}
}

func TestRunLoopFollowUpProcessed(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	mp := &mockProvider{responses: [][]Event{
		{{Type: EventContentDelta, Content: "first done"}, {Type: EventStop}},
		{{Type: EventContentDelta, Content: "second done"}, {Type: EventStop}},
	}}
	a.Provider = mp

	a.FollowUp("now do this")
	if err := a.Prompt("go", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	if mp.callCount() != 2 {
		t.Fatalf("provider called %d times, want 2 (follow-up drains after turn)", mp.callCount())
	}
	req := mp.lastRequest()
	if len(req) == 0 || FlattenContent(req[len(req)-1].Content) != "now do this" {
		t.Fatalf("follow-up not appended to second request: %+v", req)
	}
	if st := a.State(); st.Status != StatusIdle {
		t.Fatalf("status = %q, want idle", st.Status)
	}
}

func TestRunLoopAbort(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	bp := &blockingProvider{entered: make(chan struct{})}
	a.Provider = bp

	turnEnd := make(chan struct{}, 1)
	a.Subscribe(func(ev AgentEvent) {
		if ev.Type == AgentEventTurnEnd {
			turnEnd <- struct{}{}
		}
	}, false)

	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	<-bp.entered
	a.Abort()

	select {
	case <-turnEnd:
	case <-time.After(2 * time.Second):
		t.Fatal("turn_end not fired after abort")
	}
	a.WaitForIdle()
	if st := a.State(); st.Status != StatusIdle {
		t.Fatalf("status = %q, want idle after abort", st.Status)
	}
}

func TestPromptWhileRunning(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	bp := &blockingProvider{entered: make(chan struct{})}
	a.Provider = bp

	if err := a.Prompt("first", nil); err != nil {
		t.Fatal(err)
	}
	<-bp.entered
	if err := a.Prompt("second", nil); err == nil {
		t.Fatal("Prompt while running should fail")
	}
	a.Abort()
	a.WaitForIdle()
}

func TestRunLoopSystemPromptSent(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model", SystemPrompt: "be brief"})
	mp := &mockProvider{responses: [][]Event{{
		{Type: EventContentDelta, Content: "ok"},
		{Type: EventStop},
	}}}
	a.Provider = mp

	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	req := mp.lastRequest()
	if len(req) == 0 || req[0].Role != "system" || FlattenContent(req[0].Content) != "be brief" {
		t.Fatalf("system prompt not first LLM message: %+v", req)
	}
}

func TestRunLoopEmitsLifecycleEvents(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	a.Provider = &mockProvider{responses: [][]Event{{
		{Type: EventMessageStart},
		{Type: EventContentDelta, Content: "hey"},
		{Type: EventStop},
	}}}

	get := collectEvents(a)
	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	types := get()
	var sawStart, sawTurnStart, sawEnd bool
	for _, ty := range types {
		switch ty {
		case AgentEventStart:
			sawStart = true
		case AgentEventTurnStart:
			sawTurnStart = true
		case AgentEventEnd:
			sawEnd = true
		}
	}
	if !sawStart || !sawTurnStart || !sawEnd {
		t.Fatalf("missing lifecycle events: %v", types)
	}
}

func TestAgentSubscribeUnsubscribe(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	a.Provider = &mockProvider{}

	called := 0
	var mu sync.Mutex
	unsub := a.Subscribe(func(AgentEvent) {
		mu.Lock()
		called++
		mu.Unlock()
	}, false)

	unsub()
	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()
	mu.Lock()
	defer mu.Unlock()
	if called != 0 {
		t.Fatalf("unsubscribed handler called %d times", called)
	}
}

// ── Regression: PR #217 review items ─────────────────────────────────────

func TestRunLoopMaxTurnsCap(t *testing.T) {
	// A model that always issues a tool call must not loop forever:
	// the run stops after MaxTurns provider round-trips with an error.
	a := NewAgent(AgentConfig{Provider: "mock", Model: "m", MaxTurns: 3})
	// Turns 1-3 each issue a tool call; the cap then fires at the top of
	// turn 4, before the provider is called a 4th time.
	mp := &mockProvider{responses: [][]Event{
		{{Type: EventToolCall, Tool: &ToolUpdate{Name: "loop_tool"}}, {Type: EventStop}},
		{{Type: EventToolCall, Tool: &ToolUpdate{Name: "loop_tool"}}, {Type: EventStop}},
		{{Type: EventToolCall, Tool: &ToolUpdate{Name: "loop_tool"}}, {Type: EventStop}},
	}}
	a.Provider = mp
	a.Executor.RegisterTool(AgentTool{Name: "loop_tool"}, func(map[string]any) (string, error) {
		return "again", nil
	})

	errCh := make(chan error, 1)
	a.Subscribe(func(ev AgentEvent) {
		if ev.Type == AgentEventError && ev.Error != nil {
			errCh <- ev.Error
		}
	}, false)

	if err := a.Prompt("loop forever", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "MaxTurns") {
			t.Fatalf("error = %v, want MaxTurns exceeded", err)
		}
	default:
		t.Fatal("no error event fired on MaxTurns exhaustion")
	}
	if got := mp.callCount(); got != 3 {
		t.Fatalf("provider called %d times, want exactly MaxTurns (3)", got)
	}
	if st := a.State(); st.Status != StatusError {
		t.Fatalf("status = %q, want error", st.Status)
	}
}

func TestRunLoopAbortMarksTurnEnd(t *testing.T) {
	// Subscribers can distinguish aborted runs: turn_end carries
	// Aborted=true when the run was cancelled.
	a := NewAgent(AgentConfig{Provider: "mock", Model: "m"})
	bp := &blockingProvider{entered: make(chan struct{})}
	a.Provider = bp

	var aborted bool
	turnEnd := make(chan struct{}, 1)
	a.Subscribe(func(ev AgentEvent) {
		if ev.Type == AgentEventTurnEnd {
			aborted = ev.Aborted
			turnEnd <- struct{}{}
		}
	}, false)

	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	<-bp.entered
	a.Abort()
	<-turnEnd
	a.WaitForIdle()

	if !aborted {
		t.Fatal("turn_end after Abort does not set Aborted=true")
	}
}
