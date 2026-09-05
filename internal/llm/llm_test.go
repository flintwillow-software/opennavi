package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStreamEventConstantsMatchSpec(t *testing.T) {
	// Spec §01 names these exactly; renaming breaks the wire contract.
	want := map[StreamEventType]string{
		StreamEventMessageStart:  "message_start",
		StreamEventContentDelta:  "content_delta",
		StreamEventToolCallStart: "tool_call_start",
		StreamEventToolCallEnd:   "tool_call_end",
		StreamEventMessageEnd:    "message_end",
		StreamEventStop:          "stop",
		StreamEventError:         "error",
	}
	for ev, name := range want {
		if string(ev) != name {
			t.Errorf("event %q has value %q", name, ev)
		}
	}
}

func TestMockStreamScriptedResponses(t *testing.T) {
	m := NewMockProvider(
		[]StreamEvent{
			{Type: StreamEventMessageStart},
			{Type: StreamEventContentDelta, Content: "Hello "},
			{Type: StreamEventContentDelta, Content: "world"},
			{Type: StreamEventToolCallStart, ToolCall: &ToolCallDelta{ID: "call_1", Name: "f", Arguments: `{"x":1}`}},
			{Type: StreamEventToolCallEnd, ToolCall: &ToolCallDelta{ID: "call_1", Name: "f", Arguments: `{"x":1}`}},
			{Type: StreamEventStop},
		},
		[]StreamEvent{{Type: StreamEventStop}},
	)

	ctx := context.Background()
	ch, err := m.Stream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil, Config{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	var got []StreamEvent
	for ev := range ch {
		got = append(got, ev)
	}
	if len(got) != 6 {
		t.Fatalf("got %d events, want 6", len(got))
	}
	// #219 shape: tool-call deltas carry provider IDs.
	if got[3].ToolCall == nil || got[3].ToolCall.ID != "call_1" {
		t.Fatalf("tool call delta = %+v", got[3].ToolCall)
	}
	if m.Calls() != 1 {
		t.Fatalf("calls = %d", m.Calls())
	}
}

func TestMockStreamErrorScripted(t *testing.T) {
	m := NewMockProvider().WithStreamError(errors.New("unreachable"))
	if _, err := m.Stream(context.Background(), nil, nil, Config{}); err == nil {
		t.Fatal("expected scripted stream error")
	}
}

func TestMockCompleteScripted(t *testing.T) {
	resp := &Response{Message: Message{Role: RoleAssistant, Content: "answer"}}
	m := NewMockProvider().WithResponses(resp)

	got, err := m.Complete(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Content != "answer" {
		t.Fatalf("response = %+v", got)
	}
	// Second call without a scripted response errors.
	if _, err := m.Complete(context.Background(), nil, nil, Config{}); err == nil {
		t.Fatal("unscripted Complete should error")
	}
}

func TestMockPing(t *testing.T) {
	m := NewMockProvider()
	if err := m.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.WithPingError(errors.New("down"))
	if err := m.Ping(context.Background()); err == nil {
		t.Fatal("scripted ping error not surfaced")
	}
}

func TestHeuristicTokenCounter(t *testing.T) {
	tc := HeuristicTokenCounter{}
	if got := tc.CountText(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	if got := tc.CountText(strings.Repeat("a", 8)); got != 2 {
		t.Fatalf("8 chars = %d, want 2", got)
	}

	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},                                                            // 4 + 1
		{Role: RoleUser, Content: strings.Repeat("a", 8)},                                             // 3 + 2
		{Role: RoleAssistant, Content: "ok", ToolCalls: []ToolCall{{Arguments: []byte(`{"k":"v"}`)}}}, // 3 + 1 + 3
		{Role: RoleTool, ToolResults: []ToolResult{{Content: strings.Repeat("b", 12)}}},               // 3 + 3
	}
	// 4+1 + 3+2 + 3+1+3 + 3+3 = 23
	if got := tc.CountTokens(msgs); got != 23 {
		t.Fatalf("CountTokens = %d, want 23", got)
	}
}

// The Provider contract: anything implementing it must satisfy the
// full interface surface (compile-time check with the mock).
var _ Provider = (*MockProvider)(nil)
