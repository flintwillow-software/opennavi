package daemon

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/thought"
	"github.com/flintwillow-software/opennavi/llm"
)

// getFreePort grabs an ephemeral port for the metrics listener.
func getFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func getMetrics(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(body)
}

func metricValue(t *testing.T, body, name string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name) {
			parts := strings.Fields(line)
			if len(parts) < 2 {
				t.Fatalf("malformed line %q", line)
			}
			v, err := strconv.ParseFloat(parts[len(parts)-1], 64)
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			return v
		}
	}
	t.Fatalf("metric %q not found in:\n%s", name, body)
	return 0
}

// TestMetricsEndpointServesLiveAccounting: a fully assembled runtime
// with --metrics-addr set processes a thought; the endpoint reflects
// the enqueue → claim → complete lifecycle.
func TestMetricsEndpointServesLiveAccounting(t *testing.T) {
	addr := getFreePort(t)
	rt, err := Assemble(RuntimeParams{
		SessionDB:   dbFile(t),
		Model:       "test-model",
		Workers:     1,
		MetricsAddr: addr,
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventContentDelta, Content: "metrics hello"},
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if rt.Metrics == nil {
		t.Fatal("metrics collector not assembled despite MetricsAddr")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	// Wait for the endpoint to come up.
	url := "http://" + addr + "/metrics"
	var body string
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			blob, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(blob)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics endpoint never came up: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Seeded before any events: nothing queued yet.
	if got := metricValue(t, body, "opennavi_thoughts_in_queue"); got != 0 {
		t.Fatalf("seeded in_queue = %v, want 0", got)
	}

	// Enqueue a thought through the assembled queue; the pool claims
	// and completes it.
	if err := rt.Queue.Enqueue(&thought.Thought{
		ID: "m1", Prompt: "say hi", Priority: 4, Status: thought.ThoughtPending,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	deadline = time.Now().Add(10 * time.Second)
	for {
		body = getMetrics(t, url)
		if metricValue(t, body, "opennavi_thought_processing_seconds_count") >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("thought never processed; last body:\n%s", body)
		}
		time.Sleep(25 * time.Millisecond)
	}

	if got := metricValue(t, body, "opennavi_thoughts_queued_total"); got != 1 {
		t.Fatalf("queued_total = %v, want 1", got)
	}
	if got := metricValue(t, body, `opennavi_thoughts_by_status{status="complete"}`); got != 1 {
		t.Fatalf("complete = %v, want 1", got)
	}
	if got := metricValue(t, body, "opennavi_thought_processing_seconds_sum"); got <= 0 {
		t.Fatalf("processing sum = %v, want > 0", got)
	}

	// Graceful shutdown path also shuts the metrics server down.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	// Server must refuse connections post-shutdown.
	if _, err := http.Get(url); err == nil {
		t.Fatal("metrics server still accepting after shutdown")
	}
}

// TestMetricsDisabledByDefault: without MetricsAddr the collector is
// nil and nothing listens — zero cost when unset.
func TestMetricsDisabledByDefault(t *testing.T) {
	rt, err := Assemble(testParams(t))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if rt.Metrics != nil {
		t.Fatal("collector assembled without MetricsAddr")
	}
}

// TestMetricsConcurrentScrapesWhileProcessing: parallel scrapes racing
// event handling must never produce inconsistent output (counts within
// buckets ≤ count, valid exposition at every snapshot).
func TestMetricsConcurrentScrapesWhileProcessing(t *testing.T) {
	rt, err := Assemble(RuntimeParams{
		SessionDB:   dbFile(t),
		Model:       "test-model",
		Workers:     2,
		MetricsAddr: "127.0.0.1:0", // assembled collector exists; we scrape the collector directly
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventContentDelta, Content: "concurrent"},
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if rt.Metrics == nil {
		t.Fatal("collector nil with MetricsAddr set")
	}

	// Drive many queue events while scrapers render concurrently.
	go func() {
		for i := 0; i < 50; i++ {
			id := "c" + strconv.Itoa(i)
			_ = rt.Queue.Enqueue(&thought.Thought{ID: id, Prompt: "x", Priority: 4, Status: thought.ThoughtPending})
		}
	}()

	done := make(chan struct{})
	for s := 0; s < 4; s++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 20; i++ {
				body := rt.Metrics.Render()
				// Every scrape must be parseable: value line per sample.
				for _, line := range strings.Split(body, "\n") {
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					fields := strings.Fields(line)
					if len(fields) < 2 {
						t.Errorf("bad sample line %q", line)
						return
					}
					if _, err := strconv.ParseFloat(fields[len(fields)-1], 64); err != nil {
						t.Errorf("unparseable value %q: %v", line, err)
						return
					}
				}
			}
		}()
	}
	for s := 0; s < 4; s++ {
		<-done
	}

	// Terminal accounting consistency: buckets cumulative ≤ count.
	body := rt.Metrics.Render()
	count := metricValue(t, body, "opennavi_thought_processing_seconds_count")
	for _, le := range []string{"0.1", "0.5", "1", "2.5", "5", "10", "+Inf"} {
		v := metricValue(t, body, `opennavi_thought_processing_seconds_bucket{le="`+le+`"}`)
		if v > count {
			t.Fatalf("bucket le=%s (%v) exceeds count (%v)", le, v, count)
		}
	}
}

// dbFile returns a temp DB path (name distinct from testParams' to
// keep these tests self-contained).
func dbFile(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/metrics-e2e.db"
}
