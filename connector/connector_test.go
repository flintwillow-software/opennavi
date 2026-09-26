package connector

import (
	"context"
	"testing"

	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/thought"
)

// Stub handles proving Deps is assignable from plain structural
// implementations (ADR 0004 rule 2) without naming any runtime type.
type stubTS struct {
	queued []*thought.Thought
}

func (s *stubTS) Enqueue(t *thought.Thought) error       { s.queued = append(s.queued, t); return nil }
func (s *stubTS) RouteMessage(id, text string) error     { return nil }
func (s *stubTS) Pause(id string) error                  { return nil }
func (s *stubTS) Resume(id string) error                 { return nil }
func (s *stubTS) Cancel(id string) error                 { return nil }
func (s *stubTS) BumpPriority(id string, prio int) error { return nil }
func (s *stubTS) Retry(id string) error                  { return nil }
func (s *stubTS) GetThought(id string) (*thought.Thought, error) {
	return nil, thought.ErrThoughtNotFound
}
func (s *stubTS) GetThoughts() []*thought.Thought { return s.queued }
func (s *stubTS) OnEvent(fn func(thought.Event))  {}

type stubConnector struct {
	deps Deps
}

func (c *stubConnector) Start(ctx context.Context) error { return nil }
func (c *stubConnector) Stop() error                     { return nil }

func TestConnectorDeps(t *testing.T) {
	deps := Deps{
		ThoughtSystem: &stubTS{},
		Model:         model.NewRegistry(),
	}
	c := &stubConnector{deps: deps}
	if deps.ThoughtSystem == nil || deps.Model == nil {
		t.Fatal("deps handles not assignable")
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
}
