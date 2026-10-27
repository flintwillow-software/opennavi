package thought

import (
	"testing"
)

func TestEffectivePriority(t *testing.T) {
	// 0 is the highest band (ADR 0035 decision 1) and must NOT be promoted
	// to 1 — a clamp to 1 would collapse the cancel/steer distinction.
	lo := &Thought{Priority: 0}
	if lo.EffectivePriority() != 0 {
		t.Errorf("prio 0 should stay 0, got %d", lo.EffectivePriority())
	}
	if neg := (&Thought{Priority: -5}).EffectivePriority(); neg != 0 {
		t.Errorf("negative priority should clamp to 0, got %d", neg)
	}
	if hi := (&Thought{Priority: 99}).EffectivePriority(); hi != 10 {
		t.Errorf("prio 99 should clamp to 10")
	}
	if (&Thought{Priority: 4}).EffectivePriority() != 4 {
		t.Error("mid priority changed")
	}
}

func TestEventKinds(t *testing.T) {
	if string(EventMessageRouted) != "message_routed" || string(EventEnqueued) != "enqueued" {
		t.Errorf("event kinds wrong")
	}
}

// stubTS proves structural satisfaction (ADR 0004 rule 2): a plain
// struct implementing ThoughtSystem's methods conforms — the interface
// never names the runtime.
type stubTS struct {
	queued []*Thought
}

func (s *stubTS) Enqueue(t *Thought) error {
	s.queued = append(s.queued, t)
	return nil
}
func (s *stubTS) RouteMessage(id, text string) error     { return nil }
func (s *stubTS) Pause(id string) error                  { return nil }
func (s *stubTS) Resume(id string) error                 { return nil }
func (s *stubTS) Cancel(id string) error                 { return nil }
func (s *stubTS) BumpPriority(id string, prio int) error { return nil }
func (s *stubTS) Retry(id string) error                  { return nil }
func (s *stubTS) Complete(id, result string) error       { return nil }
func (s *stubTS) Fail(id, detail string) error           { return nil }
func (s *stubTS) GetThought(id string) (*Thought, error) { return nil, ErrThoughtNotFound }
func (s *stubTS) GetThoughts() []*Thought                { return s.queued }
func (s *stubTS) OnEvent(fn func(Event))                 {}

func TestThoughtSystemStructuralConformance(t *testing.T) {
	var ts ThoughtSystem = &stubTS{}
	if ts == nil {
		t.Fatal("stub does not conform")
	}
	if err := ts.Enqueue(&Thought{ID: "t1", Prompt: "hi", Priority: 4, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if len(ts.GetThoughts()) != 1 {
		t.Errorf("enqueue did not land")
	}
}
