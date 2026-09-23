package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Metric name prefix (issue spec): opennavi_*.
const prefix = "opennavi"

// renderOptions tunes exposition (subset for tests).
type renderOptions struct {
	// includePerThought emits the capped recent ring as per-thought
	// series (opennavi_thought_recent_processing_seconds).
	includePerThought bool
}

// Handler returns an http.Handler serving the Prometheus text
// exposition (content-type per the format spec) on GET.
func (c *Collector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body := c.Render()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(body))
	})
}

// Render produces the full Prometheus text exposition: HELP/TYPE
// headers, then sample lines, one metric family at a time. The output
// round-trips through standard Prometheus parsers (format 0.0.4).
func (c *Collector) Render() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var b strings.Builder

	// ── counters ─────────────────────────────────────────────
	writeCounter(&b, prefix+"_thoughts_queued_total",
		"Total number of thoughts queued.",
		series{labels: nil, value: c.queuedTotal})
	writeCounter(&b, prefix+"_thoughts_parked_total",
		"Total number of thoughts parked (running → paused; preemption or cancellation).",
		series{labels: nil, value: c.parkedTotal})
	writeCounter(&b, prefix+"_thoughts_resumed_total",
		"Total number of thought resumes (paused → pending).",
		series{labels: nil, value: c.resumedTotal})
	writeCounter(&b, prefix+"_thoughts_preempted_total",
		"Total number of thought preemptions (high-priority claim against a running low-priority thought).",
		series{labels: nil, value: c.preemptedTotal})
	writeCounter(&b, prefix+"_thought_persist_failures_total",
		"Total number of thought-state persistence failures (in-memory and stored state may have diverged).",
		series{labels: nil, value: c.persistFailed})
	writeCounter(&b, prefix+"_thoughts_deleted_total",
		"Total number of thought records removed via delete (audit-relevant: distinguishes work-disappeared-via-delete from work-cancelled).",
		series{labels: nil, value: c.deletedTotal})

	// ── by_status: one family, one series per status ─────────
	statuses := []struct {
		name  string
		value float64
	}{
		{"pending", c.status.pending},
		{"running", c.status.running},
		{"paused", c.status.paused},
		{"complete", c.status.complete},
		{"failed", c.status.failed},
		{"cancelled", c.status.cancelled},
	}
	fmt.Fprintf(&b, "# HELP %s_thoughts_by_status Count of thoughts per lifecycle status.\n", prefix)
	fmt.Fprintf(&b, "# TYPE %s_thoughts_by_status gauge\n", prefix)
	for _, s := range statuses {
		fmt.Fprintf(&b, "%s_thoughts_by_status{status=%q} %s\n", prefix, s.name, formatValue(s.value))
	}

	// ── in_queue gauge ───────────────────────────────────────
	writeGauge(&b, prefix+"_thoughts_in_queue",
		"Current number of thoughts awaiting or resuming processing (pending + paused).",
		series{labels: nil, value: c.inQueue})

	// ── processing duration histogram ────────────────────────
	name := prefix + "_thought_processing_seconds"
	fmt.Fprintf(&b, "# HELP %s Duration of thought processing (claim → terminal state), in seconds.\n", name)
	fmt.Fprintf(&b, "# TYPE %s histogram\n", name)
	var cumulative float64
	for i, le := range Buckets {
		cumulative = c.histogram.buckets[i]
		fmt.Fprintf(&b, "%s_bucket{le=%q} %s\n", name, formatLE(le), formatValue(cumulative))
	}
	fmt.Fprintf(&b, "%s_bucket{le=\"+Inf\"} %s\n", name, formatValue(c.histogram.count))
	fmt.Fprintf(&b, "%s_sum %s\n", name, formatValue(c.histogram.sum))
	fmt.Fprintf(&b, "%s_count %s\n", name, formatValue(c.histogram.count))

	// ── per-thought recent durations (capped ring) ───────────
	if len(c.recent) > 0 {
		name := prefix + "_thought_recent_processing_seconds"
		fmt.Fprintf(&b, "# HELP %s Most recent terminal processing duration per thought (capped at %d thoughts, FIFO eviction).\n", name, c.recentCap)
		fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
		// Deterministic order for stable scrapes.
		ids := make([]string, 0, len(c.recent))
		for _, s := range c.recent {
			ids = append(ids, s.thoughtID)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(&b, "%s{thought_id=%q} %s\n", name, id, formatValue(c.recent[c.recentIndex[id]].seconds))
		}
	}

	return b.String()
}

type series struct {
	labels []labelPair
	value  float64
}

type labelPair struct {
	name  string
	value string
}

func writeCounter(b *strings.Builder, name, help string, s series) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s counter\n", name)
	if len(s.labels) == 0 {
		fmt.Fprintf(b, "%s %s\n", name, formatValue(s.value))
		return
	}
	fmt.Fprintf(b, "%s%s %s\n", name, renderLabels(s.labels), formatValue(s.value))
}

func writeGauge(b *strings.Builder, name, help string, s series) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s gauge\n", name)
	if len(s.labels) == 0 {
		fmt.Fprintf(b, "%s %s\n", name, formatValue(s.value))
		return
	}
	fmt.Fprintf(b, "%s%s %s\n", name, renderLabels(s.labels), formatValue(s.value))
}

func renderLabels(labels []labelPair) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for _, l := range labels {
		parts = append(parts, fmt.Sprintf("%s=%q", l.name, l.value))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// formatLE renders the le label: Prometheus requires float formatting
// for bucket boundaries (0.1, 0.5, 1, 2.5, 5, 10, +Inf).
func formatLE(le float64) string {
	return strconv.FormatFloat(le, 'g', -1, 64)
}

// formatValue renders a sample value (NaN/±Inf per the text format).
func formatValue(v float64) string {
	switch {
	case v != v: // NaN
		return "NaN"
	case v > math.MaxFloat64/2:
		return "+Inf"
	default:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}
