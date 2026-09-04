package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventBusSubscribeAndFire(t *testing.T) {
	eb := NewEventBus()
	var got atomic.Int32
	unsub := eb.Subscribe(func(ev AgentEvent) {
		if ev.Type != AgentEventTurnStart {
			t.Errorf("event type = %q, want turn_start", ev.Type)
		}
		got.Add(1)
	}, false)

	eb.Fire(AgentEvent{Type: AgentEventTurnStart})
	eb.Fire(AgentEvent{Type: AgentEventTurnStart})

	waitFor(t, func() bool { return got.Load() == 2 }, "subscriber should receive 2 events")

	unsub()
	eb.Fire(AgentEvent{Type: AgentEventTurnStart})
	time.Sleep(50 * time.Millisecond)
	if got.Load() != 2 {
		t.Fatalf("got %d events after unsubscribe, want 2", got.Load())
	}
}

func TestEventBusOnce(t *testing.T) {
	eb := NewEventBus()
	var got atomic.Int32
	eb.Subscribe(func(AgentEvent) { got.Add(1) }, true)

	eb.Fire(AgentEvent{Type: AgentEventStart})
	waitFor(t, func() bool { return got.Load() == 1 }, "once subscriber should fire once")

	eb.Fire(AgentEvent{Type: AgentEventStart})
	eb.Fire(AgentEvent{Type: AgentEventStart})
	time.Sleep(50 * time.Millisecond)
	if got.Load() != 1 {
		t.Fatalf("once subscriber fired %d times, want 1", got.Load())
	}
}

func TestAgentEndBarrier(t *testing.T) {
	b := NewAgentEndBarrier()
	b.Wait() // no pending: returns immediately

	b.Add()
	b.Add()
	if b.Count() != 2 {
		t.Fatalf("count = %d, want 2", b.Count())
	}
	b.Signal()
	b.Signal()
	b.Wait() // should return now
	if b.Count() != 0 {
		t.Fatalf("count = %d, want 0", b.Count())
	}
	// Extra signals must not panic (doneCh already closed).
	b.Signal()
}

func TestAgentSubscribeOnceUsesBarrier(t *testing.T) {
	a := NewAgent(AgentConfig{Model: "test-model", Provider: "mock"})
	a.Provider = &mockProvider{}

	released := make(chan struct{})
	var once sync.Once
	var fired atomic.Int32
	a.Subscribe(func(AgentEvent) {
		fired.Add(1)
		once.Do(func() { close(released) })
	}, true)

	if err := a.Prompt("hello", nil); err != nil {
		t.Fatal(err)
	}

	// The once subscriber holds the barrier; agent_end must not fire
	// until it has processed its first event.
	endFired := make(chan struct{})
	a.EventBus.Subscribe(func(ev AgentEvent) {
		if ev.Type == AgentEventEnd {
			close(endFired)
		}
	}, false)

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("once subscriber never fired")
	}
	select {
	case <-endFired:
	case <-time.After(2 * time.Second):
		t.Fatal("agent_end did not fire after barrier released")
	}
	a.WaitForIdle()
}

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
