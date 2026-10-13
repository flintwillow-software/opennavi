package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	pubportal "github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

type allowAll struct{}

func (allowAll) Authorize(pubportal.Action, pubportal.Actor) error { return nil }

func newQueue(t *testing.T) *thought.Queue {
	t.Helper()
	q, err := thought.NewQueue(thought.Config{
		MaxConcurrent:  4,
		SlotExhaustion: thought.PolicyQueue,
	}, thought.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// newGateway wires a broker + control portal + gateway over a queue, the
// same shape the navi composition root produces.
func newGateway(t *testing.T, auth pubportal.Authorizer) (*Gateway, *pubportal.Broker, *thought.Queue) {
	t.Helper()
	q := newQueue(t)
	broker := pubportal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(pubportal.QueueEvent(ev)) })
	hub := pubportal.NewControl(auth)
	if err := hub.Start(context.Background(), pubportal.Deps{ThoughtSystem: q, Observation: broker}); err != nil {
		t.Fatal(err)
	}
	gw := New("127.0.0.1:0", hub)
	if err := gw.Start(context.Background(), pubportal.Deps{Observation: broker, Authenticator: pubportal.AllowAllAuthenticator(principal.MustParse("service:test"))}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gw.Stop() })
	return gw, broker, q
}

// TestGatewayServesObservationAndControl is the gateway-as-portal gate:
// it serves the bounded observation stream and applies control through
// the wrapped portal.
func TestGatewayServesObservationAndControl(t *testing.T) {
	gw, _, q := newGateway(t, allowAll{})
	base := "http://" + gw.BoundAddr()

	// Control: enqueue through the HTTP channel.
	resp, err := http.Post(base+"/portal/control", "application/json",
		strings.NewReader(`{"action":"enqueue","id":"g1","prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("enqueue status = %d: %s", resp.StatusCode, body)
	}
	if got, err := q.GetThought("g1"); err != nil || got.Prompt != "hi" {
		t.Fatalf("enqueued thought = %+v (%v)", got, err)
	}

	// Observation: the lifecycle event is on the served stream.
	resp, frames := openSSE(t, base+"/portal/stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	ev := waitFrame(t, frames, func(f pubportal.Frame) bool {
		return f.Kind == pubportal.FrameEvent && f.Event != nil && f.Event.ThoughtID == "g1"
	})
	if ev.Event.Kind != "enqueued" {
		t.Fatalf("stream event = %+v, want the enqueued lifecycle event", ev.Event)
	}
}

// TestGatewayFailsClosedWithoutAuthorizer pins the security property:
// observation and control both refuse (403) when no authorizer is wired.
func TestGatewayFailsClosedWithoutAuthorizer(t *testing.T) {
	gw, _, _ := newGateway(t, nil)
	base := "http://" + gw.BoundAddr()

	resp, err := http.Post(base+"/portal/control", "application/json",
		strings.NewReader(`{"action":"enqueue","id":"x","prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("control without authorizer = %d, want 403", resp.StatusCode)
	}

	resp, err = http.Get(base + "/portal/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stream without authorizer = %d, want 403", resp.StatusCode)
	}
}
