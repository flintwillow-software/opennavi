package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	internalportal "github.com/flintwillow-software/opennavi/internal/portal"
	pubportal "github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

type allowAll struct{}

func (allowAll) Authorize(action internalportal.PortalAction, identity string) error { return nil }

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

// newGateway wires a broker + Hub + gateway over a queue, the same shape
// the navi composition root produces.
func newGateway(t *testing.T, auth internalportal.PortalAuthorizer) (*Gateway, *pubportal.Broker, *thought.Queue) {
	t.Helper()
	q := newQueue(t)
	broker := pubportal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(pubportal.QueueEvent(ev)) })
	hub := internalportal.NewHub(auth)
	if err := hub.Start(context.Background(), pubportal.Deps{ThoughtSystem: q, Observation: broker}); err != nil {
		t.Fatal(err)
	}
	gw := New("127.0.0.1:0", hub)
	if err := gw.Start(context.Background(), pubportal.Deps{Observation: broker}); err != nil {
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
		strings.NewReader(`{"action":"enqueue","id":"g1","prompt":"hi","identity":"u"}`))
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
	resp, err = http.Get(base + "/portal/events?identity=u")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", resp.StatusCode)
	}
	var page struct {
		Events []struct {
			Kind      string `json:"kind"`
			ThoughtID string `json:"thoughtID"`
		} `json:"events"`
		Next int `json:"next"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	sawEnqueued := false
	for _, ev := range page.Events {
		if ev.Kind == "enqueued" && ev.ThoughtID == "g1" {
			sawEnqueued = true
		}
	}
	if !sawEnqueued {
		t.Fatalf("enqueued event not served: %+v", page)
	}

	// Cursor resume: reading after Next returns no duplicate of the batch.
	req, _ := http.NewRequest(http.MethodGet, base+"/portal/events?identity=u&from="+strconv.Itoa(page.Next), nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var second struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 0 {
		t.Fatalf("cursor resume re-delivered %d events", len(second.Events))
	}
}

// TestGatewayFailsClosedWithoutAuthorizer pins the security property:
// observation and control both refuse (403) when no authorizer is wired.
func TestGatewayFailsClosedWithoutAuthorizer(t *testing.T) {
	gw, _, _ := newGateway(t, nil)
	base := "http://" + gw.BoundAddr()

	resp, err := http.Post(base+"/portal/control", "application/json",
		strings.NewReader(`{"action":"enqueue","id":"x","prompt":"hi","identity":"u"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("control without authorizer = %d, want 403", resp.StatusCode)
	}

	resp, err = http.Get(base + "/portal/events?identity=u")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("observation without authorizer = %d, want 403", resp.StatusCode)
	}
}
