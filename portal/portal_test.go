package portal

import (
	"context"
	"testing"

	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/thought"
)

type stubTS struct {
	subs []func(thought.Event)
}

func (s *stubTS) Enqueue(t *thought.Thought) error       { return nil }
func (s *stubTS) RouteMessage(id, text string) error     { return nil }
func (s *stubTS) Pause(id string) error                  { return nil }
func (s *stubTS) Resume(id string) error                 { return nil }
func (s *stubTS) Cancel(id string) error                 { return nil }
func (s *stubTS) BumpPriority(id string, prio int) error { return nil }
func (s *stubTS) Retry(id string) error                  { return nil }
func (s *stubTS) Complete(id, result string) error       { return nil }
func (s *stubTS) Fail(id, detail string) error           { return nil }
func (s *stubTS) GetThought(id string) (*thought.Thought, error) {
	return nil, thought.ErrThoughtNotFound
}
func (s *stubTS) GetThoughts() []*thought.Thought { return []*thought.Thought{} }
func (s *stubTS) OnEvent(fn func(thought.Event))  { s.subs = append(s.subs, fn) }

// stubPortal subscribes on Start — which is exactly the thing a portal
// could not do before Deps reached it through the lifecycle call.
type stubPortal struct {
	got Deps
}

func (p *stubPortal) Start(ctx context.Context, deps Deps) error {
	p.got = deps
	deps.ThoughtSystem.OnEvent(func(thought.Event) {})
	return nil
}

func (p *stubPortal) Stop() error                                     { return nil }
func (p *stubPortal) Attach(identity string) error                    { return nil }
func (p *stubPortal) Steer(identity, thoughtID, message string) error { return nil }
func (p *stubPortal) Pause(identity, thoughtID string) error          { return nil }
func (p *stubPortal) Resume(identity, thoughtID string) error         { return nil }
func (p *stubPortal) Cancel(identity, thoughtID string) error         { return nil }
func (p *stubPortal) BumpPriority(identity, thoughtID string, prio int) error {
	return nil
}
func (p *stubPortal) Retry(identity, thoughtID string) error            { return nil }
func (p *stubPortal) Enqueue(identity string, t *thought.Thought) error { return nil }

func TestPortalDepsAtStartAndConformance(t *testing.T) {
	ts := &stubTS{}
	deps := Deps{ThoughtSystem: ts, Model: model.NewRegistry()}
	if deps.ThoughtSystem == nil || deps.Model == nil {
		t.Fatal("deps not assignable from plain structural implementations")
	}

	var p Portal = &stubPortal{}
	if err := p.Start(context.Background(), deps); err != nil {
		t.Fatal(err)
	}

	sp, ok := p.(*stubPortal)
	if !ok {
		t.Fatal("unexpected portal type")
	}
	if sp.got.ThoughtSystem == nil || sp.got.Model == nil {
		t.Fatal("portal did not receive Deps at Start")
	}
	if len(ts.subs) != 1 {
		t.Fatalf("portal did not subscribe to the event stream: want 1 subscriber, got %d", len(ts.subs))
	}

	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := p.Steer("u", "t1", "hi"); err != nil {
		t.Fatal(err)
	}
}
