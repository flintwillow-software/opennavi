package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFragmentedToolCallArgumentsAreAccumulated pins NAVI-572: an
// OpenAI-compatible provider that streams tool-call arguments in fragments
// must yield the FULLY ACCUMULATED arguments on the terminal event.
//
// This is the shape DeepSeek V4.1 Flash (via OpenRouter) actually sends, and
// the shape the previous tests missed: they sent the whole argument object in
// one chunk, so a parser that emitted the first fragment and discarded the
// rest passed CI while invoking every tool with empty arguments in production.
//
// Captured live:
//
//	{"index":0,"id":"chatcmpl-tool-9244...","function":{"name":"f","arguments":""}}
//	{"index":0,"function":{"arguments":"{\"issue\": \"NAVI-571"}}
//	{"index":0,"function":{"arguments":"\"}"}}
func TestFragmentedToolCallArgumentsAreAccumulated(t *testing.T) {
	p := newOpenAISSEParser("openrouter")

	// Fragment 1: id + name, EMPTY arguments.
	evts, _ := p.feed(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"huly_issue_description","arguments":""}}]},"finish_reason":null}]}`)
	if len(evts) == 0 {
		t.Fatal("no start event for the first fragment")
	}
	// The start event may carry empty arguments — that is the provider's
	// first fragment, and it is why the terminal event must be authoritative.
	var start *StreamEvent
	for i := range evts {
		if evts[i].Type == StreamEventToolCallStart {
			start = &evts[i]
		}
	}
	if start == nil {
		t.Fatalf("no tool_call_start among %d events", len(evts))
	}

	// Fragments 2..n: the JSON body, split.
	p.feed(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"issue\": \"NAVI-571"}}]},"finish_reason":null}]}`)
	p.feed(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]},"finish_reason":null}]}`)

	// The terminal event must carry the whole object.
	endEvts, _ := p.feed(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	var end *StreamEvent
	for i := range endEvts {
		if endEvts[i].Type == StreamEventToolCallEnd {
			end = &endEvts[i]
		}
	}
	if end == nil {
		t.Fatal("no tool_call_end event on finish_reason")
	}
	if end.ToolCall == nil {
		t.Fatal("tool_call_end carries no call")
	}
	if got := end.ToolCall.Arguments; got != `{"issue": "NAVI-571"}` {
		t.Fatalf("accumulated arguments = %q, want the whole JSON object", got)
	}
	if end.ToolCall.Name != "huly_issue_description" {
		t.Errorf("name = %q", end.ToolCall.Name)
	}
	if end.ToolCall.ID != "call_1" {
		t.Errorf("id = %q", end.ToolCall.ID)
	}
}

// TestFragmentedArgumentsAreValidJSON is the end-to-end assertion: whatever
// the fragments were, the terminal arguments parse. The bug this guards
// surfaced as `invalid arguments: unexpected end of JSON input`.
func TestFragmentedArgumentsAreValidJSON(t *testing.T) {
	p := newOpenAISSEParser("openrouter")
	p.feed(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":""}}]},"finish_reason":null}]}`)
	for _, frag := range []string{`{"a":`, `"b`, `",`, `"n":`, `42}`} {
		p.feed(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + jsonString(frag) + `}}]},"finish_reason":null}]}`)
	}
	evts, _ := p.feed(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	for _, ev := range evts {
		if ev.Type != StreamEventToolCallEnd {
			continue
		}
		if !strings.Contains(ev.ToolCall.Arguments, `"n":42`) {
			t.Fatalf("arguments = %q, want the assembled object", ev.ToolCall.Arguments)
		}
		if !json.Valid([]byte(ev.ToolCall.Arguments)) {
			t.Fatalf("arguments are not valid JSON: %q", ev.ToolCall.Arguments)
		}
		return
	}
	t.Fatal("no terminal tool call event")
}

// jsonString encodes s as a JSON string literal for embedding in a test frame.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
