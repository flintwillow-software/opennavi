package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/portal"
	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/thought"
)

// allowAllAuth is an authorizer that permits every identity/action:
// the e2e test seam used exactly where the production runtime wires
// nil (fail closed).
type allowAllAuth struct{}

func (a allowAllAuth) Authorize(action portal.PortalAction, identity string) error {
	return nil
}

// portalFreePort grabs an ephemeral port for the portal listener.
func portalFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// portalEvents GETs the portal stream after the cursor and decodes it.
func portalEvents(t *testing.T, url string, after int) (map[string]any, error) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return map[string]any{}, err
	}
	defer resp.Body.Close()
	blob, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]any{}, err
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		return map[string]any{}, fmt.Errorf("bad json: %v: %s", err, string(blob))
	}
	return doc, nil
}

// portalPostControl POSTs a control action and returns the status code.
func portalPostControl(t *testing.T, url string, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST build: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

func happenings(doc map[string]any) ([]string, int) {
	evs, ok := doc["events"].([]any)
	if !ok {
		return []string{}, 0
	}
	kinds := []string{}
	next := 0
	if n, ok := doc["next"].(float64); ok {
		next = int(n)
	}
	for _, e := range evs {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		k, _ := m["kind"].(string)
		c, _ := m["category"].(string)
		kinds = append(kinds, c+"/"+k)
	}
	return kinds, next
}

// TestPortalEndpointServesLiveStream: an assembled runtime with
// PortalAddr set streams the thought lifecycle + per-thought agent
// events over the serve path. Observation is an attachment,
// so the allow-all authorizer seam is wired for the happy path;
// control still fails closed for the unauthorizing metrics-style stub.
func TestPortalEndpointServesLiveStream(t *testing.T) {
	addr := portalFreePort(t)
	rt, err := Assemble(RuntimeParams{
		SessionDB:  t.TempDir() + "/portal-e2e.db",
		Model:      "test-model",
		Workers:    1,
		PortalAddr: addr,
		// Test seam: production wires nil and fails closed (covered by
		// TestPortalObservationFailClosedWithoutAuthorizer).
		OverrideAuthorizer: allowAllAuth{},
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventContentDelta, Content: "portal hello"},
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if rt.Portal == nil {
		t.Fatal("portal not assembled despite PortalAddr")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	base := "http://" + addr
	url := base + "/portal/events?identity=portal-user"
	// Wait for the endpoint to come up.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("portal endpoint never came up: %v", err)
		} else {
			time.Sleep(25 * time.Millisecond)
		}
	}

	// Enqueue a thought through the assembled queue; the pool claims
	// and processes it (mock provider streams one delta).
	if err := rt.Queue.Enqueue(&thought.Thought{
		ID: "portal-e2e-1", Prompt: "say hi", Priority: 4, Status: thought.ThoughtPending,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Poll the stream until the agent's content delta and the
	// completed lifecycle event both arrive (one event delivery each).
	foundDelta, foundStarted, foundCompleted := false, false, false
	allKinds := []string{}
	cursor := 0
	deadline = time.Now().Add(10 * time.Second)
	for {
		doc, gerr := portalEvents(t, fmt.Sprintf("%s?from=%d", url, cursor), cursor)
		if gerr != nil {
			t.Fatalf("GET events: %v", gerr)
		}
		kinds, next := happenings(doc)
		allKinds = append(allKinds, kinds...)
		evs, _ := doc["events"].([]any)
		for _, e := range evs {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			k, _ := m["kind"].(string)
			c, _ := m["category"].(string)
			d, _ := m["detail"].(string)
			switch c + "/" + k {
			case "queue/started":
				foundStarted = true
			case "queue/completed":
				foundCompleted = true
			case "agent/content_delta":
				if strings.Contains(d, "portal hello") {
					foundDelta = true
				}
			}
		}
		if len(kinds) > 0 {
			cursor = next
		}
		if foundStarted && foundCompleted && foundDelta {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream never delivered all events; delta=%v started=%v completed=%v (cursor %d; all: %v)", foundDelta, foundStarted, foundCompleted, cursor, allKinds)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Control is AUTHORIZED in this test (allow-all seam): it passes
	// the authorization gate and reaches the queue — the thought is
	// already complete by now, so the queue rejects the pause, but
	// crucially it is NOT an authorization 403 (fail-closed refusal
	// is covered by TestPortalObservationFailClosedWithoutAuthorizer).
	code := portalPostControl(t, base+"/portal/control?identity=portal-user", `{"action":"pause","thoughtID":"portal-e2e-1"}`)
	if code == http.StatusForbidden {
		t.Error("authorized control was refused 403")
	}

	// Unknown route/method are 404/405, not a crash.
	resp, err := http.Get(base + "/nope")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown path = %d, want 404", resp.StatusCode)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon Run did not shut down")
	}
}

// TestPortalObservationFailClosedWithoutAuthorizer: with NO authorizer
// wired, observation AND control are attachments that fail closed
// — the stream (prompt content, tool-input snippets) is
// never served to an unauthorized identity, and control is never
// defaulted open.
func TestPortalObservationFailClosedWithoutAuthorizer(t *testing.T) {
	addr := portalFreePort(t)
	rt, err := Assemble(RuntimeParams{
		SessionDB:  t.TempDir() + "/portal-failclosed.db",
		Model:      "test-model",
		Workers:    1,
		PortalAddr: addr,
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	streamURL := "http://" + addr + "/portal/events?identity=intruder&from=0"
	// Wait for the endpoint to come up (refused requests still hit it).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if resp, err := http.Get(streamURL); err == nil {
			resp.Body.Close()
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("portal endpoint never came up: %v", err)
		} else {
			time.Sleep(25 * time.Millisecond)
		}
	}

	// Observation is denied: fail closed, never defaulted open.
	resp, err := http.Get(streamURL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unattached observation returned %d, want 403", resp.StatusCode)
	}

	// Control is refused the same way.
	code := portalPostControl(t, "http://"+addr+"/portal/control?identity=intruder", `{"action":"steer","thoughtID":"x","text":"hi"}`)
	if code != http.StatusForbidden {
		t.Errorf("unauthorized control returned %d, want 403", code)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon Run did not shut down")
	}
}
