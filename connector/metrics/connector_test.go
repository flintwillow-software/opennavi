package metrics

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/thought"
)

// newTestQueue builds a real queue over the in-memory store, so the
// connector test exercises the actual event fanout rather than a stub.
func newTestQueue(t *testing.T) *thought.Queue {
	t.Helper()
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 1}, newMemStore())
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

func TestConnectorServesMetrics(t *testing.T) {
	q := newTestQueue(t)
	c := New("127.0.0.1:0")
	deps := connector.Deps{ThoughtSystem: q}
	if err := c.Start(context.Background(), deps); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	addr := c.BoundAddr()
	if addr == "" {
		t.Fatal("BoundAddr empty after a successful Start")
	}

	// Enqueue a thought; the exposition must show it.
	if err := q.Enqueue(&thought.Thought{ID: "m1", Prompt: "x", Priority: 4}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "opennavi_thoughts_queued_total 1") {
		t.Fatalf("exposition missing the enqueued thought:\n%s", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", ct)
	}
}

func TestConnectorListenerFailureReturnsError(t *testing.T) {
	// Occupy a port so the connector's bind fails.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	defer occupied.Close()

	c := New(occupied.Addr().String())
	err = c.Start(context.Background(), connector.Deps{ThoughtSystem: newTestQueue(t)})
	if err == nil {
		t.Fatal("Start on a busy port must report the bind failure")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Fatalf("error = %v, want a listen error", err)
	}
	if c.BoundAddr() != "" {
		t.Fatalf("BoundAddr = %q, want empty after a failed Start", c.BoundAddr())
	}
	// A failed Start must release everything it acquired; Stop is not
	// called by the composition root, so it must be a no-op here too.
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop after a failed Start: %v", err)
	}
}

func TestConnectorStopClosesListener(t *testing.T) {
	c := New("127.0.0.1:0")
	if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: newTestQueue(t)}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := c.BoundAddr()
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if c.BoundAddr() != "" {
		t.Fatalf("BoundAddr = %q after Stop, want empty", c.BoundAddr())
	}
	// A connection queued in the accept backlog may complete once, so
	// require the listener to be gone within a bounded window rather than
	// on the very next dial.
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/metrics")
		if err != nil {
			return // listener closed
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still serving after Stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConnectorRequiresThoughtSystem(t *testing.T) {
	c := New("127.0.0.1:0")
	if err := c.Start(context.Background(), connector.Deps{}); err == nil {
		t.Fatal("Start without a thought system must fail")
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop after a failed Start: %v", err)
	}
}
