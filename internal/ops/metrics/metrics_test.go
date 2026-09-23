package metrics

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/thought"
)

// send feeds one event to the collector (bypassing the queue).
func send(c *Collector, kind thought.QueueEventKind, id, detail string) {
	c.observeEvent(thought.QueueEvent{Kind: kind, ThoughtID: id, Detail: detail})
}

func findLine(body, name string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name) {
			return line
		}
	}
	return ""
}

func valueOf(t *testing.T, body, name string) float64 {
	t.Helper()
	line := findLine(body, name)
	if line == "" {
		t.Fatalf("metric %q not found in exposition:\n%s", name, body)
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		t.Fatalf("malformed sample line %q", line)
	}
	v, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		t.Fatalf("value of %q: %v (line %q)", name, err, line)
	}
	return v
}

func newTestCollector() *Collector {
	c := New()
	c.now = func() time.Time { return fakeNow() }
	return c
}

var fakeClock struct {
	t time.Time
}

func fakeNow() time.Time { return fakeClock.t }

func advance(d time.Duration) { fakeClock.t = fakeClock.t.Add(d) }

func init() { fakeClock.t = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }

func TestLifecycleAccountingBalances(t *testing.T) {
	c := newTestCollector()

	// Enqueue three; claim one; complete it.
	send(c, thought.EventEnqueued, "a", "")
	send(c, thought.EventEnqueued, "b", "")
	send(c, thought.EventEnqueued, "c", "")
	if got := valueOf(t, c.Render(), "opennavi_thoughts_in_queue"); got != 3 {
		t.Fatalf("in_queue after 3 enqueues = %v, want 3", got)
	}
	send(c, thought.EventStarted, "a", "")
	if got := valueOf(t, c.Render(), "opennavi_thoughts_in_queue"); got != 2 {
		t.Fatalf("in_queue after claim = %v, want 2", got)
	}
	if got := valueOf(t, c.Render(), `opennavi_thoughts_by_status{status="running"}`); got != 1 {
		t.Fatalf("running = %v, want 1", got)
	}

	// Park → resume → re-claim → complete. The 500ms partial run is
	// intentionally not observed (park drops the start entry — the
	// histogram measures terminal runs); the terminal run is 2s.
	send(c, thought.EventPausedEv, "a", "")
	if got := valueOf(t, c.Render(), "opennavi_thoughts_in_queue"); got != 3 {
		t.Fatalf("in_queue after park = %v, want 3 (parked work is queued work)", got)
	}
	send(c, thought.EventResumed, "a", "")
	send(c, thought.EventStarted, "a", "")
	advance(2 * time.Second)
	send(c, thought.EventCompleted, "a", "")
	body := c.Render()
	if got := valueOf(t, body, "opennavi_thoughts_in_queue"); got != 2 {
		t.Fatalf("in_queue after complete = %v, want 2", got)
	}
	if got := valueOf(t, body, "opennavi_thoughts_queued_total"); got != 3 {
		t.Fatalf("queued_total = %v, want 3", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="complete"}`); got != 1 {
		t.Fatalf("complete = %v, want 1", got)
	}
	// Histogram: one observation of 2s (the terminal run; the parked
	// partial run is not counted).
	if got := valueOf(t, body, "opennavi_thought_processing_seconds_count"); got != 1 {
		t.Fatalf("histogram count = %v, want 1", got)
	}
	if got := valueOf(t, body, "opennavi_thought_processing_seconds_sum"); got != 2 {
		t.Fatalf("histogram sum = %v, want 2", got)
	}
	// Bucket cumulative: ≤2.5/≤5/≤10 include it; ≤0.1/≤0.5/≤1 don't.
	if got := valueOf(t, body, `opennavi_thought_processing_seconds_bucket{le="2.5"}`); got != 1 {
		t.Fatalf("le=2.5 bucket = %v, want 1", got)
	}
	if got := valueOf(t, body, `opennavi_thought_processing_seconds_bucket{le="1"}`); got != 0 {
		t.Fatalf("le=1 bucket = %v, want 0", got)
	}
}

func TestFailedAndRetryAccounting(t *testing.T) {
	c := newTestCollector()
	send(c, thought.EventEnqueued, "a", "")
	send(c, thought.EventStarted, "a", "")
	advance(time.Second)
	send(c, thought.EventFailed, "a", "boom")

	body := c.Render()
	// Failed → back to pending awaiting Retry (in_queue grows back).
	if got := valueOf(t, body, "opennavi_thoughts_in_queue"); got != 1 {
		t.Fatalf("in_queue after fail = %v, want 1", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="failed"}`); got != 1 {
		t.Fatalf("failed = %v, want 1", got)
	}
	if got := valueOf(t, body, "opennavi_thought_processing_seconds_sum"); got != 1 {
		t.Fatalf("failed run must still observe duration, sum = %v", got)
	}

	// Retry: failed → pending, queued_total ticks (the queue emits
	// enqueued with Detail "retry").
	send(c, thought.EventEnqueued, "a", "retry")
	body = c.Render()
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="failed"}`); got != 0 {
		t.Fatalf("failed after retry = %v, want 0", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="pending"}`); got != 1 {
		t.Fatalf("pending after retry = %v, want 1", got)
	}
	if got := valueOf(t, body, "opennavi_thoughts_in_queue"); got != 1 {
		t.Fatalf("in_queue after retry = %v, want 1 (not double-counted)", got)
	}
}

func TestCancelledFromPendingAndPaused(t *testing.T) {
	c := newTestCollector()
	// Cancel from pending.
	send(c, thought.EventEnqueued, "a", "")
	send(c, thought.EventCancelled, "a", "")
	// Cancel from paused.
	send(c, thought.EventEnqueued, "b", "")
	send(c, thought.EventStarted, "b", "")
	send(c, thought.EventPausedEv, "b", "")
	send(c, thought.EventCancelled, "b", "")

	body := c.Render()
	if got := valueOf(t, body, "opennavi_thoughts_in_queue"); got != 0 {
		t.Fatalf("in_queue after cancels = %v, want 0", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="cancelled"}`); got != 2 {
		t.Fatalf("cancelled = %v, want 2", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="paused"}`); got != 0 {
		t.Fatalf("paused after cancel = %v, want 0", got)
	}
}

func TestPreemptedAndPersistFailed(t *testing.T) {
	c := newTestCollector()
	send(c, thought.EventPreempted, "a", "by b (priority 1)")
	send(c, thought.EventPersistFailed, "a", "disk full")
	body := c.Render()
	if got := valueOf(t, body, "opennavi_thoughts_preempted_total"); got != 1 {
		t.Fatalf("preempted_total = %v, want 1", got)
	}
	if got := valueOf(t, body, "opennavi_thought_persist_failures_total"); got != 1 {
		t.Fatalf("persist_failures = %v, want 1", got)
	}
}

func TestRecentRingCapAndReplace(t *testing.T) {
	c := newTestCollector()
	c.recentCap = 3
	// Five terminal thoughts — ring keeps the last 3.
	ids := []string{"t1", "t2", "t3", "t4", "t5"}
	for _, id := range ids {
		send(c, thought.EventEnqueued, id, "")
		send(c, thought.EventStarted, id, "")
		advance(floatSeconds(1))
		send(c, thought.EventCompleted, id, "")
	}
	body := c.Render()
	for _, gone := range []string{"t1", "t2"} {
		if strings.Contains(body, `thought_id="`+gone+`"`) {
			t.Fatalf("evicted thought %s still in recent ring", gone)
		}
	}
	for _, kept := range []string{"t3", "t4", "t5"} {
		if !strings.Contains(body, `thought_id="`+kept+`"`) {
			t.Fatalf("kept thought %s missing from recent ring", kept)
		}
	}
	// Update-in-place: re-running a kept thought updates its value
	// without growing the ring.
	send(c, thought.EventEnqueued, "t5", "retry")
	send(c, thought.EventStarted, "t5", "")
	advance(floatSeconds(7))
	send(c, thought.EventCompleted, "t5", "")
	body = c.Render()
	if got := valueOf(t, body, `opennavi_thought_recent_processing_seconds{thought_id="t5"}`); got != 7 {
		t.Fatalf("t5 recent = %v, want 7 (updated in place)", got)
	}
	if got := valueOf(t, body, "opennavi_thought_processing_seconds_count"); got != 6 {
		t.Fatalf("histogram count = %v, want 6", got)
	}
}

func floatSeconds(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

func TestRenderFormatParseBack(t *testing.T) {
	// Round-trip: every non-comment line parses as a Prometheus
	// sample; HELP/TYPE headers are well-formed; bucket ordering and
	// +Inf/sum/count trailer present.
	c := newTestCollector()
	send(c, thought.EventEnqueued, "a", "")
	send(c, thought.EventStarted, "a", "")
	advance(1200 * time.Millisecond)
	send(c, thought.EventCompleted, "a", "")
	body := c.Render()

	seenTypes := map[string]string{}
	lastLE := -1.0
	bucketLines := 0
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			parts := strings.SplitN(line, " ", 4)
			if len(parts) < 4 {
				t.Fatalf("malformed HELP: %q", line)
			}
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			parts := strings.SplitN(line, " ", 4)
			if len(parts) < 4 {
				t.Fatalf("malformed TYPE: %q", line)
			}
			seenTypes[parts[2]] = parts[3]
			continue
		}
		// Sample line: name{labels} value | name value
		name := line
		if idx := strings.Index(line, "{"); idx >= 0 {
			end := strings.Index(line[idx:], "}")
			if end < 0 {
				t.Fatalf("unbalanced braces: %q", line)
			}
			name = line[:idx]
		} else if idx := strings.LastIndex(line, " "); idx >= 0 {
			name = line[:idx]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("sample line needs name+value: %q", line)
		}
		if _, err := strconv.ParseFloat(fields[len(fields)-1], 64); err != nil {
			t.Fatalf("unparseable value in %q: %v", line, err)
		}
		if strings.HasSuffix(name, "_bucket") {
			leStart := strings.Index(line, `le="`) + 4
			leEnd := strings.Index(line[leStart:], `"`)
			leStr := line[leStart : leStart+leEnd]
			if leStr == "+Inf" {
				if got := valueOf(t, body, name+`{le="+Inf"}`); got != valueOf(t, body, "opennavi_thought_processing_seconds_count") {
					t.Fatalf("+Inf bucket must equal count")
				}
			} else {
				le, err := strconv.ParseFloat(leStr, 64)
				if err != nil {
					t.Fatalf("bad le %q: %v", leStr, err)
				}
				if le <= lastLE {
					t.Fatalf("buckets not ascending: %v after %v", le, lastLE)
				}
				lastLE = le
			}
			bucketLines++
		}
	}
	if bucketLines != len(Buckets)+1 {
		t.Fatalf("expected %d bucket lines (+Inf), got %d", len(Buckets)+1, bucketLines)
	}
	for _, want := range []string{"opennavi_thoughts_queued_total", "opennavi_thoughts_in_queue", "opennavi_thoughts_by_status", "opennavi_thought_processing_seconds"} {
		if _, ok := seenTypes[want]; !ok {
			t.Fatalf("missing TYPE header for %s", want)
		}
	}
}

func TestHandlerServesExposition(t *testing.T) {
	c := newTestCollector()
	send(c, thought.EventEnqueued, "a", "")

	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Fatalf("content-type = %q", ct)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "opennavi_thoughts_queued_total") {
		t.Fatalf("body missing queued_total: %q", string(buf[:n]))
	}

	// Non-GET rejected.
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", resp2.StatusCode)
	}
}

func TestWireQueueSeedsRecoveredState(t *testing.T) {
	// kill-9 → restart: recovered thoughts are pending at wire time —
	// the seed must show them before any event fires.
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 2}, newMemStore())
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	// A crashed-then-recovered thought sits in pending after NewQueue's
	// recovery path; enqueue three normally.
	for _, id := range []string{"p1", "p2", "p3-seed"} {
		if err := q.Enqueue(&thought.Thought{ID: id, Prompt: "x", Priority: 4, Status: thought.ThoughtPending}); err != nil {
			t.Fatalf("Enqueue %s: %v", id, err)
		}
	}

	c := newTestCollector()
	c.WireQueue(q)
	body := c.Render()
	if got := valueOf(t, body, "opennavi_thoughts_in_queue"); got != 3 {
		t.Fatalf("seeded in_queue = %v, want 3", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="pending"}`); got != 3 {
		t.Fatalf("seeded pending = %v, want 3", got)
	}

	// Events after seeding keep accounting consistent.
	if err := q.Enqueue(&thought.Thought{ID: "p4", Prompt: "x", Priority: 4, Status: thought.ThoughtPending}); err != nil {
		t.Fatalf("Enqueue p4: %v", err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("Dequeue should claim p4")
	}
	body = c.Render()
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="running"}`); got != 1 {
		t.Fatalf("running after dequeue = %v, want 1", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="pending"}`); got != 3 {
		t.Fatalf("pending after dequeue = %v, want 3", got)
	}
}

// memStore is the queue's in-memory Store (thought.MemoryStore wraps
// it; use directly to avoid import cycles in tests).
type memStore struct {
	thoughts map[string]*thought.Thought
}

func newMemStore() *memStore { return &memStore{thoughts: map[string]*thought.Thought{}} }

func (m *memStore) SaveThought(t *thought.Thought) error {
	m.thoughts[t.ID] = t.Clone()
	return nil
}

func (m *memStore) LoadThoughts() ([]*thought.Thought, error) {
	out := make([]*thought.Thought, 0, len(m.thoughts))
	for _, t := range m.thoughts {
		out = append(out, t.Clone())
	}
	return out, nil
}

func (m *memStore) LoadThought(id string) (*thought.Thought, error) {
	t, ok := m.thoughts[id]
	if !ok {
		return nil, thought.ErrThoughtNotFound
	}
	return t.Clone(), nil
}

func (m *memStore) DeleteThought(id string) error {
	delete(m.thoughts, id)
	return nil
}

func TestDeletedEventAccounting(t *testing.T) {
	// Delete is an exact-delta compensator: in_queue/by_status return
	// to their pre-enqueue values (S130600 §04 review catch).
	c := newTestCollector()

	// Pending delete: queued → deleted nets to zero.
	send(c, thought.EventEnqueued, "p1", "")
	send(c, thought.EventDeleted, "p1", string(thought.ThoughtPending))
	body := c.Render()
	if got := valueOf(t, body, "opennavi_thoughts_in_queue"); got != 0 {
		t.Fatalf("in_queue after enqueue+delete = %v, want 0", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="pending"}`); got != 0 {
		t.Fatalf("pending after enqueue+delete = %v, want 0", got)
	}
	if got := valueOf(t, body, "opennavi_thoughts_queued_total"); got != 1 {
		t.Fatalf("queued_total = %v, want 1 (the enqueue happened)", got)
	}

	// Paused delete: pausedSet disambiguation (paused work is in_queue).
	send(c, thought.EventEnqueued, "p2", "")
	send(c, thought.EventStarted, "p2", "")
	send(c, thought.EventPausedEv, "p2", "")
	send(c, thought.EventDeleted, "p2", string(thought.ThoughtPaused))
	body = c.Render()
	if got := valueOf(t, body, "opennavi_thoughts_in_queue"); got != 0 {
		t.Fatalf("in_queue after paused delete = %v, want 0", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="paused"}`); got != 0 {
		t.Fatalf("paused after delete = %v, want 0", got)
	}
	if got := valueOf(t, body, `opennavi_thoughts_by_status{status="cancelled"}`); got != 0 {
		t.Fatalf("delete must not tick cancelled = %v", got)
	}

	// clampZero: a delete without a matching enqueue cannot push
	// gauges negative.
	send(c, thought.EventDeleted, "ghost", string(thought.ThoughtPending))
	body = c.Render()
	if got := valueOf(t, body, "opennavi_thoughts_in_queue"); got != 0 {
		t.Fatalf("ghost delete drove in_queue negative: %v", got)
	}
}

func TestDeletedEventUnknownPriorStatusClamps(t *testing.T) {
	// Unexpected prior status (defensive): clamps keep gauges honest.
	c := newTestCollector()
	send(c, thought.EventDeleted, "ghost", "running")
	if got := valueOf(t, c.Render(), "opennavi_thoughts_in_queue"); got != 0 {
		t.Fatalf("unknown-prior delete drove in_queue: %v", got)
	}
}
