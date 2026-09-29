// Package metrics implements thought-queue observability (scope,
// #192/NAVI-258): pure-Go Prometheus text exposition — no OTel SDK,
// no client_golang, no new dependencies (repo convention; the text
// format is hand-rolled at this scale).
//
// PARKED (NAVI-354): the package has no in-repo consumer at present.
// It was wired only by the retired `internal/daemon` run path;
// NAVI-357 tracks wiring it onto the reference navi behind
// `--metrics-addr` (the flag is accepted but currently only warns).
// It is deliberately kept — not dead code — because that ticket
// consumes the collector and its tests unchanged.
//
// The collector rides the existing event seam
// (thought.Queue.OnEvent — the same fanout the runtime logger uses):
// counters advance on enqueue/park/resume/persist-
// failure, the in-queue gauge and per-status gauges follow the
// lifecycle by deltas, and processing duration lands in a histogram
// with standard buckets.
//
// Cardinality decision (documented for review): the issue sketch puts
// thought_id on queued/in-queue series. Prometheus text exposition has
// no series expiry, so per-thought labels on cumulative metrics leak
// series forever. Instead:
//   - aggregate series (queued_total, in_queue, by_status, histogram)
//     carry only bounded labels (status);
//   - the per-thought breakdown ships as a CAPPED ring gauge,
//     opennavi_thought_recent_processing_seconds{thought_id}, holding
//     the most recent 100 terminal thoughts (FIFO eviction) — bounded
//     series, per-thought granularity for dashboards.
//
// Accounting note: several queue emits fire while q.mu is held, so
// the collector never re-enters the queue from an event handler —
// in-queue/status gauges are maintained by exact lifecycle deltas and
// seeded once at wire time from a queue snapshot (which is also what
// makes kill-9 → restart recovery immediately visible: recovered
// thoughts appear in by_status as pending at seed time).
package metrics

import (
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
)

// Buckets are the thought-processing histogram buckets (seconds).
var Buckets = []float64{0.1, 0.5, 1, 2.5, 5, 10}

// DefaultRecentCap bounds the per-thought recent-duration ring.
const DefaultRecentCap = 100

// statusCounts mirrors the queue's live thought distribution.
type statusCounts struct {
	pending   float64
	running   float64
	paused    float64
	complete  float64
	failed    float64
	cancelled float64
}

// Collector accumulates queue metrics and renders Prometheus text
// exposition. Zero value is not usable; construct with New. All
// methods are safe for concurrent use.
type Collector struct {
	mu sync.Mutex

	queuedTotal    float64
	parkedTotal    float64
	resumedTotal   float64
	preemptedTotal float64
	persistFailed  float64
	deletedTotal   float64
	inQueue        float64 // pending + paused (claimable or parked work)
	status         statusCounts
	histogram      *histogram
	started        map[string]time.Time // thought_id → claim time (in-flight)
	startedOrder   []string             // FIFO for defensive eviction
	pausedSet      map[string]bool      // thought_ids currently paused (cancel disambiguation)
	recent         []recentSample       // capped FIFO ring
	recentIndex    map[string]int       // thought_id → ring position (update-in-place)
	recentCap      int
	now            func() time.Time
	seeded         bool
}

type recentSample struct {
	thoughtID string
	seconds   float64
}

type histogram struct {
	buckets []float64 // cumulative counts, len == len(Buckets)
	sum     float64
	count   float64
}

// New creates an empty collector.
func New() *Collector {
	return &Collector{
		histogram:   &histogram{buckets: make([]float64, len(Buckets))},
		started:     make(map[string]time.Time),
		pausedSet:   make(map[string]bool),
		recentIndex: make(map[string]int),
		recentCap:   DefaultRecentCap,
		now:         time.Now,
	}
}

// WireQueue subscribes the collector to the queue's event fanout and
// seeds the gauges from a one-time snapshot of current queue state —
// thoughts recovered by crash recovery (kill-9 → restart) are visible
// immediately, before any event fires. The seed reads the queue
// outside any event emission; event handlers themselves never re-enter
// the queue (several queue emits hold q.mu).
func (c *Collector) WireQueue(q *thought.Queue) {
	c.seed(q)
	q.OnEvent(c.observeEvent)
}

// seed takes a one-time snapshot and sets the gauge families to the
// queue's current distribution.
func (c *Collector) seed(q *thought.Queue) {
	if q == nil {
		return
	}
	all := q.GetThoughts()
	counts := statusCounts{}
	for _, t := range all {
		switch t.Status {
		case thought.ThoughtPending:
			counts.pending++
		case thought.ThoughtRunning:
			counts.running++
		case thought.ThoughtPaused:
			counts.paused++
		case thought.ThoughtComplete:
			counts.complete++
		case thought.ThoughtFailed:
			counts.failed++
		case thought.ThoughtCancelled:
			counts.cancelled++
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = counts
	c.inQueue = counts.pending + counts.paused
	c.seeded = true
}

// observeEvent applies one queue event to the gauges and counters.
// Never calls back into the queue.
func (c *Collector) observeEvent(ev thought.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch ev.Kind {
	case thought.EventEnqueued:
		c.queuedTotal++
		if ev.Detail == "retry" && c.status.failed > 0 {
			// Retry re-queues a failed thought: failed → pending. The
			// failed thought is already inside in_queue (Fail added it
			// back), so the gauge must not tick again.
			c.status.failed--
			c.status.pending++
		} else {
			c.status.pending++
			c.inQueue++
		}
	case thought.EventStarted:
		c.status.pending--
		c.status.running++
		c.inQueue--
		if c.now != nil {
			id := ev.ThoughtID
			if _, tracked := c.started[id]; !tracked {
				c.startedOrder = append(c.startedOrder, id)
			}
			c.started[id] = c.now()
			c.evictStartedLocked()
		}
		delete(c.pausedSet, ev.ThoughtID)
	case thought.EventPaused:
		c.status.running--
		c.status.paused++
		c.pausedSet[ev.ThoughtID] = true
		c.inQueue++ // parked work returns to the (pending|paused) queue
		c.parkedTotal++
		// Partial run: the start entry is dropped without observing —
		// the histogram measures terminal runs; the resumed run
		// re-times from its own claim.
		c.dropStartLocked(ev.ThoughtID)
	case thought.EventResumed:
		c.status.paused--
		c.status.pending++
		delete(c.pausedSet, ev.ThoughtID)
		c.resumedTotal++
		// paused → pending: both sides of in_queue, net zero.
	case thought.EventCompleted:
		c.status.running--
		c.status.complete++
		c.observeDurationLocked(ev.ThoughtID)
	case thought.EventFailed:
		c.status.running--
		c.status.failed++
		// The queue parks failed thoughts in pending awaiting Retry.
		c.inQueue++
		c.observeDurationLocked(ev.ThoughtID)
	case thought.EventCancelled:
		// Cancel fires from pending or paused — both are inside
		// in_queue and both decrement by 1; by_status disambiguates
		// via the paused set.
		if c.pausedSet[ev.ThoughtID] {
			c.status.paused--
			delete(c.pausedSet, ev.ThoughtID)
		} else {
			c.status.pending--
		}
		c.inQueue--
		c.status.cancelled++
		c.dropStartLocked(ev.ThoughtID)
	case thought.EventPreempted:
		c.preemptedTotal++
	case thought.EventPersistFailed:
		c.persistFailed++
	case thought.EventDeleted:
		// Record removal (Queue.Delete) — the exact-delta
		// compensator: Detail carries the PRIOR status so the right
		// by_status gauge decrements (the collector only sees events).
		// Removal is not cancellation: the audit trail distinguishes
		// them (deleted_total counter + the deleted event's Detail).
		// The status gauge simply returns to its pre-enqueue value.
		switch ev.Detail {
		case string(thought.ThoughtPaused):
			c.status.paused--
			delete(c.pausedSet, ev.ThoughtID)
		case string(thought.ThoughtPending):
			c.status.pending--
		default:
			// Unexpected prior status — clamp keeps gauges honest.
		}
		c.inQueue--
		c.deletedTotal++
		c.dropStartLocked(ev.ThoughtID)
	}
	c.clampStatusLocked()
}

// observeDurationLocked records a terminal run duration into the
// histogram and the recent ring.
func (c *Collector) observeDurationLocked(thoughtID string) {
	start, ok := c.started[thoughtID]
	if !ok {
		return
	}
	c.dropStartLocked(thoughtID)
	seconds := c.now().Sub(start).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	c.histogram.observe(seconds)
	c.pushRecentLocked(thoughtID, seconds)
}

func (c *Collector) dropStartLocked(thoughtID string) {
	if _, ok := c.started[thoughtID]; !ok {
		return
	}
	delete(c.started, thoughtID)
	for i, id := range c.startedOrder {
		if id == thoughtID {
			c.startedOrder = append(c.startedOrder[:i], c.startedOrder[i+1:]...)
			break
		}
	}
}

// evictStartedLocked bounds the in-flight start table (a thought that
// vanishes without a terminal event must not leak its entry).
func (c *Collector) evictStartedLocked() {
	for len(c.startedOrder) > 4096 {
		oldest := c.startedOrder[0]
		c.startedOrder = c.startedOrder[1:]
		delete(c.started, oldest)
	}
}

// pushRecentLocked inserts/updates the thought's recent duration and
// enforces the ring cap (FIFO eviction by first insertion).
func (c *Collector) pushRecentLocked(thoughtID string, seconds float64) {
	if idx, ok := c.recentIndex[thoughtID]; ok {
		c.recent[idx].seconds = seconds
		return
	}
	c.recent = append(c.recent, recentSample{thoughtID: thoughtID, seconds: seconds})
	c.recentIndex[thoughtID] = len(c.recent) - 1
	for len(c.recent) > c.recentCap {
		evicted := c.recent[0]
		c.recent = c.recent[1:]
		delete(c.recentIndex, evicted.thoughtID)
		// Reindex (positions shifted by one).
		for i := range c.recent {
			c.recentIndex[c.recent[i].thoughtID] = i
		}
	}
}

func (c *Collector) clampStatusLocked() {
	c.status.pending = clampZero(c.status.pending)
	c.status.running = clampZero(c.status.running)
	c.status.paused = clampZero(c.status.paused)
	c.status.complete = clampZero(c.status.complete)
	c.status.failed = clampZero(c.status.failed)
	c.status.cancelled = clampZero(c.status.cancelled)
	c.inQueue = clampZero(c.inQueue)
}

func clampZero(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

func (h *histogram) observe(seconds float64) {
	h.sum += seconds
	h.count++
	for i, le := range Buckets {
		if seconds <= le {
			h.buckets[i]++
		}
	}
}
