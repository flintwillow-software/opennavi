package navi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

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

func (p *observingPortal) Attach(string) error                    { return nil }
func (p *observingPortal) Steer(string, string, string) error     { return nil }
func (p *observingPortal) Pause(string, string) error             { return nil }
func (p *observingPortal) Resume(string, string) error            { return nil }
func (p *observingPortal) Cancel(string, string) error            { return nil }
func (p *observingPortal) BumpPriority(string, string, int) error { return nil }
func (p *observingPortal) Retry(string, string) error             { return nil }
func (p *observingPortal) Enqueue(string, *thought.Thought) error { return nil }

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
