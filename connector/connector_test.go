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

// stubConnector records what it is handed at Start. That is the point:
// a connector cannot receive Deps at construction, because nothing has
// assembled it that early — the composition root only builds Deps in
// Run, after every collection is declared.
type stubConnector struct {
	got     Deps
	stopped bool
}

func (c *stubConnector) Start(ctx context.Context, deps Deps) error {
	c.got = deps
	return nil
}

func (c *stubConnector) Stop() error {
	c.stopped = true
	return nil
}

func TestConnectorDepsAtStart(t *testing.T) {
	ts := &stubTS{}
	deps := Deps{ThoughtSystem: ts, Model: model.NewRegistry()}

	var c Connector = &stubConnector{}
	if err := c.Start(context.Background(), deps); err != nil {
		t.Fatal(err)
	}

	sc, ok := c.(*stubConnector)
	if !ok {
		t.Fatal("unexpected connector type")
	}
	if sc.got.ThoughtSystem == nil || sc.got.Model == nil {
		t.Fatal("connector did not receive Deps at Start")
	}

	// The handles must be usable, not merely non-nil: a connector does
	// its job by driving the thought system through Deps.
	if err := sc.got.ThoughtSystem.Enqueue(&thought.Thought{ID: "t1", Prompt: "inbound work"}); err != nil {
		t.Fatal(err)
	}
	if len(ts.queued) != 1 {
		t.Fatalf("connector enqueue through Deps: want 1 thought, got %d", len(ts.queued))
	}

	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if !sc.stopped {
		t.Fatal("Stop did not reach the connector")
	}
}
