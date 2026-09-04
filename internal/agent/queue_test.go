package agent

import (
	"testing"
	"time"
)

func TestSteeringQueueOneAtATime(t *testing.T) {
	q := NewSteeringQueue(QueueModeOneAtATime)
	if q.Mode() != QueueModeOneAtATime {
		t.Fatalf("mode = %q, want one-at-a-time", q.Mode())
	}
	q.Add("first")
	q.Add("second")
	q.Add("third")

	got, ok := q.Next()
	if !ok || got != "third" {
		t.Fatalf("Next() = %q,%v; want third (last replaced earlier entries)", got, ok)
	}
	if _, ok := q.Next(); ok {
		t.Fatal("queue should be empty after draining replacement")
	}
}

func TestSteeringQueueOneAtATimeSingle(t *testing.T) {
	q := NewSteeringQueue("")
	q.Add("only")
	got, ok := q.Next()
	if !ok || got != "only" {
		t.Fatalf("Next() = %q,%v; want only", got, ok)
	}
}

func TestSteeringQueueAll(t *testing.T) {
	q := NewSteeringQueue(QueueModeAll)
	q.Add("a")
	q.Add("b")
	q.Add("c")

	for i, want := range []string{"a", "b", "c"} {
		got, ok := q.Next()
		if !ok || got != want {
			t.Fatalf("Next() #%d = %q,%v; want %q", i, got, ok, want)
		}
	}
	if _, ok := q.Next(); ok {
		t.Fatal("queue should be empty")
	}
}

func TestSteeringQueueClear(t *testing.T) {
	q := NewSteeringQueue(QueueModeAll)
	q.Add("a")
	q.Add("b")
	q.Clear()
	if _, ok := q.Next(); ok {
		t.Fatal("queue should be empty after Clear")
	}
}

func TestSteeringQueueChan(t *testing.T) {
	q := NewSteeringQueue(QueueModeAll)
	q.Add("sig")
	select {
	case msg := <-q.Chan():
		if msg != "sig" {
			t.Fatalf("chan received %q, want sig", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("Chan() did not receive signal after Add")
	}
}

func TestFollowUpQueue(t *testing.T) {
	q := NewFollowUpQueue()
	if _, ok := q.Next(); ok {
		t.Fatal("empty queue should not return a message")
	}
	q.Add("f1")
	q.Add("f2")
	got, ok := q.Next()
	if !ok || got != "f1" {
		t.Fatalf("Next() = %q,%v; want f1 (FIFO)", got, ok)
	}
	q.Clear()
	if _, ok := q.Next(); ok {
		t.Fatal("queue should be empty after Clear")
	}
}
