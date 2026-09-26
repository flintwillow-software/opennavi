package portal

import (
	"testing"

	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/thought"
)

type stubTS struct{}

func (s *stubTS) Enqueue(t *thought.Thought) error       { return nil }
func (s *stubTS) RouteMessage(id, text string) error     { return nil }
func (s *stubTS) Pause(id string) error                  { return nil }
func (s *stubTS) Resume(id string) error                 { return nil }
func (s *stubTS) Cancel(id string) error                 { return nil }
func (s *stubTS) BumpPriority(id string, prio int) error { return nil }
func (s *stubTS) Retry(id string) error                  { return nil }
func (s *stubTS) GetThought(id string) (*thought.Thought, error) {
	return nil, thought.ErrThoughtNotFound
}
func (s *stubTS) GetThoughts() []*thought.Thought { return []*thought.Thought{} }
func (s *stubTS) OnEvent(fn func(thought.Event))  {}

type stubPortal struct{}

func (p *stubPortal) Attach(identity string) error                      { return nil }
func (p *stubPortal) Steer(identity, thoughtID, message string) error   { return nil }
func (p *stubPortal) Pause(identity, thoughtID string) error            { return nil }
func (p *stubPortal) Resume(identity, thoughtID string) error           { return nil }
func (p *stubPortal) Cancel(identity, thoughtID string) error           { return nil }
func (p *stubPortal) Enqueue(identity string, t *thought.Thought) error { return nil }

func TestPortalDepsAndConformance(t *testing.T) {
	deps := Deps{
		ThoughtSystem: &stubTS{},
		Model:         model.NewRegistry(),
	}
	if deps.ThoughtSystem == nil || deps.Model == nil {
		t.Fatal("deps not assignable")
	}
	var p Portal = &stubPortal{}
	if p == nil {
		t.Fatal("stub portal does not conform to the contract")
	}
	if err := p.Steer("u", "t1", "hi"); err != nil {
		t.Fatal(err)
	}
}
