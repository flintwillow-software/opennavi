package navi

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// TestAgentDetailPreservesDeltaSpacingAndToolOutput pins NAVI-380: a content
// delta is a fragment (whitespace preserved), and a tool result reports the
// tool's output rather than its input.
func TestAgentDetailPreservesDeltaSpacingAndToolOutput(t *testing.T) {
	delta := agent.AgentEvent{Type: agent.AgentEventContentDelta, Message: &agent.AgentMessage{Content: " world"}}
	if got := agentDetail(delta); got != " world" {
		t.Fatalf("a content delta must keep its whitespace: %q", got)
	}

	result := agent.AgentEvent{Type: agent.AgentEventToolResult, Tool: &agent.ToolUpdate{
		Name: "shell_exec", Input: json.RawMessage(`{"command":"false"}`), Result: "boom", IsError: true,
	}}
	got := agentDetail(result)
	if !strings.Contains(got, "boom") || strings.Contains(got, "command") {
		t.Fatalf("a tool result must report the output, not the input: %q", got)
	}
	if !strings.Contains(got, "error:") {
		t.Fatalf("an errored tool result must be marked: %q", got)
	}

	call := agent.AgentEvent{Type: agent.AgentEventToolCall, Tool: &agent.ToolUpdate{Name: "shell_exec", Input: json.RawMessage(`{"command":"ls"}`)}}
	if got := agentDetail(call); !strings.Contains(got, "ls") {
		t.Fatalf("a tool call must show its input: %q", got)
	}
}

// observingPortal is a minimal portal component built on the public
// interface alone (NAVI-352): it subscribes to the observation stream it
// receives through Deps and records the events it sees. It imports
// neither internal/ nor the navi runtime.
type observingPortal struct {
	sub portal.Subscription

	mu    sync.Mutex
	kinds []string
}

func (p *observingPortal) Start(ctx context.Context, deps portal.Deps) error {
	p.sub = deps.Observation.Subscribe(256)
	go func() {
		cursor := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			evs, next, _ := p.sub.ReadAfter(cursor)
			if len(evs) > 0 {
				cursor = next
				p.mu.Lock()
				for _, ev := range evs {
					p.kinds = append(p.kinds, string(ev.Category)+"/"+ev.Kind)
				}
				p.mu.Unlock()
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return nil
}

func (p *observingPortal) Stop() error {
	if p.sub != nil {
		p.sub.Close()
	}
	return nil
}

func (p *observingPortal) Attach(portal.Actor) error                    { return nil }
func (p *observingPortal) Steer(portal.Actor, string, string) error     { return nil }
func (p *observingPortal) Pause(portal.Actor, string) error             { return nil }
func (p *observingPortal) Resume(portal.Actor, string) error            { return nil }
func (p *observingPortal) Cancel(portal.Actor, string) error            { return nil }
func (p *observingPortal) BumpPriority(portal.Actor, string, int) error { return nil }
func (p *observingPortal) Retry(portal.Actor, string) error             { return nil }
func (p *observingPortal) Enqueue(portal.Actor, *thought.Thought) error { return nil }

func (p *observingPortal) saw(want string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.kinds {
		if k == want {
			return true
		}
	}
	return false
}

// TestPortalObservesRunningThoughtOutput is the NAVI-352 acceptance: a
// component observes a running thought's output through the public
// interface alone, with no runtime hook and no internal import.
func TestPortalObservesRunningThoughtOutput(t *testing.T) {
	n := mockNavi(t, llm.NewMockProvider(streamEvents("portal output")))

	obs := &observingPortal{}
	if err := n.Portals.Add(obs); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if obs.saw("agent/content_delta") && obs.saw("queue/completed") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	t.Fatalf("portal did not observe the running thought; saw %v", obs.kinds)
}
