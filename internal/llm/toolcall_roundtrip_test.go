package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestOllamaConversationUniqueCallIDs: two turns of tool calls must
// produce conversation-unique synthesized IDs (no call_0 collision
// when the conversation is re-sent with prior turns).
func TestOllamaConversationUniqueCallIDs(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob := new(strings.Builder)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			blob.Write(buf[:n])
			if err != nil {
				break
			}
		}
		mu.Lock()
		bodies = append(bodies, blob.String())
		mu.Unlock()
		var reqBody map[string]any
		if err := json.Unmarshal([]byte(blob.String()), &reqBody); err == nil {
			if msgs, _ := reqBody["messages"].([]any); len(msgs) >= 1 {
				// Turn 1: two tool calls (synthesized call_0, call_1).
				// Turn 2 (history present): TWO MORE tool calls — their
				// synthesized IDs must be call_2/call_3, NOT a reset to
				// call_0/call_1 (the mutation-killed vacuous version of
				// this test answered without calls).
				hasHistory := false
				for _, m := range msgs {
					if mm, _ := m.(map[string]any); mm["role"] == "assistant" {
						hasHistory = true
					}
				}
				if !hasHistory {
					w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"f1","arguments":{"a":1}}},{"function":{"name":"f2","arguments":{"b":2}}}]},"done":false}`))
					return
				}
				w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"f3","arguments":{"c":3}}},{"function":{"name":"f4","arguments":{"d":4}}}]},"done":true}`))
				return
			}
		}
		w.Write([]byte(`{"message":{"role":"assistant","content":"done"},"done":true}`))
	}))
	defer srv.Close()

	p := NewOllamaProviderAt(srv.URL)
	_, err := p.Stream(context.Background(),
		[]Message{{Role: RoleUser, Content: "turn 1"}}, nil, Config{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	// Turn 2: the conversation re-sent with turn 1's calls in history —
	// the synthesized IDs for THIS stream's new calls must be call_2
	// and call_3 (base offset by the 2 prior calls).
	ch, err := p.Stream(context.Background(),
		[]Message{
			{Role: RoleUser, Content: "turn 1"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "call_0", Name: "f1", Arguments: []byte(`{"a":1}`)},
				{ID: "call_1", Name: "f2", Arguments: []byte(`{"b":2}`)},
			}},
			{Role: RoleTool, Content: "r", ToolResults: []ToolResult{{ToolCallID: "call_0", Content: "r"}}},
		}, nil, Config{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	var endIDs []string
	for ev := range ch {
		if ev.Type == StreamEventToolCallEnd && ev.ToolCall != nil {
			endIDs = append(endIDs, ev.ToolCall.ID)
		}
	}
	if len(endIDs) != 2 {
		t.Fatalf("tool_call_end IDs = %v, want [call_2 call_3]", endIDs)
	}
	if endIDs[0] != "call_2" || endIDs[1] != "call_3" {
		t.Fatalf("synthesized IDs = %v, want [call_2 call_3] — conversation-unique synthesis broken", endIDs)
	}
}

func TestOllamaSynthesizedIDsOffsetByHistory(t *testing.T) {
	// countPriorToolCalls offsets the synthesis base by prior calls.
	msgs := []Message{
		{Role: RoleUser, Content: "t1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "call_0", Name: "f"}, {ID: "call_1", Name: "g"},
		}},
		{Role: RoleTool, ToolResults: []ToolResult{{ToolCallID: "call_0", Content: "r"}}},
	}
	if got := countPriorToolCalls(msgs); got != 2 {
		t.Fatalf("countPriorToolCalls = %d, want 2", got)
	}
}

// TestOpenAICompatToolCallIDRoundTrip: the full round trip — tool_call
// ID in the response → state message → tool result → NEXT REQUEST
// carries the same ID on both the assistant tool_calls and the tool
// result message.
func TestOpenAICompatToolCallIDRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var secondRequest string
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		turn++
		thisTurn := turn
		blob := new(strings.Builder)
		buf := make([]byte, 8192)
		for {
			n, err := r.Body.Read(buf)
			blob.Write(buf[:n])
			if err != nil {
				break
			}
		}
		body := blob.String()
		if thisTurn == 2 {
			secondRequest = body
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if thisTurn == 1 {
			w.Write([]byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_color","arguments":"{}"}}]}}]}` + "\n\n"))
			w.Write([]byte("data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"))
			w.Write([]byte("data: [DONE]" + "\n\n"))
			return
		}
		// Turn 2: model sees the result, answers.
		w.Write([]byte("data: " + `{"choices":[{"delta":{"content":"the color is blue"},"finish_reason":"stop"}]}` + "\n\n"))
		w.Write([]byte("data: [DONE]" + "\n\n"))
	}))
	defer srv.Close()

	p := &OpenRouterProvider{BaseURL: srv.URL, APIKey: "k", client: srv.Client()}

	// Turn 1: tool call with a real provider-supplied ID.
	ch, err := p.Stream(context.Background(),
		[]Message{{Role: RoleUser, Content: "what color?"}},
		[]Tool{{Name: "get_color", Description: "d", Parameters: []byte(`{}`)}},
		Config{Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	var callID string
	for ev := range ch {
		if ev.Type == StreamEventToolCallEnd && ev.ToolCall != nil {
			callID = ev.ToolCall.ID
		}
	}
	if callID != "call_abc" {
		t.Fatalf("call ID = %q", callID)
	}

	// Build turn 2 state exactly as the agent loop would: assistant
	// message with the call + tool result keyed by that ID.
	messages := []Message{
		{Role: RoleUser, Content: "what color?"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: callID, Name: "get_color", Arguments: []byte(`{}`)}}},
		{Role: RoleTool, ToolResults: []ToolResult{{ToolCallID: callID, Content: "blue"}}},
	}

	// Turn 2: the model must receive the SAME ID on both the assistant
	// tool_calls and the tool result.
	_, err = p.Stream(context.Background(), messages,
		[]Tool{{Name: "get_color", Description: "d", Parameters: []byte(`{}`)}},
		Config{Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if secondRequest == "" {
		t.Fatal("second request not captured")
	}
	if !strings.Contains(secondRequest, `"tool_call_id":"call_abc"`) {
		t.Fatalf("second request missing tool_call_id call_abc: %s", secondRequest)
	}
	if !strings.Contains(secondRequest, `"id":"call_abc"`) {
		t.Fatalf("second request missing assistant tool_calls id call_abc: %s", secondRequest)
	}
}
