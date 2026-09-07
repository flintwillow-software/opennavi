// Package metrics implements Prometheus-compatible metrics for the
// opennavi thought queue (spec #192): counters, gauges, and histograms
// with per-thought labels, exposed in Prometheus text exposition
// format for Alloy scraping.
//
// Design: pure Go, stdlib only, no OTel SDK (MVP decision — the
// counter surface is designed so an OTel exporter can be added later
// without breaking the metric names). Thread-safe.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Labels is the label set for a metric sample.
type Labels map[string]string

// key renders sorted label=value pairs for storage as a time series key.
func (l Labels) key() string {
	if len(l) == 0 {
		return ""
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+l[k])
	}
	return strings.Join(pairs, ",")
}

// formatLabels renders labels as Prometheus label pairs.
func formatLabels(l Labels) string {
	if len(l) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(l))
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%q", k, l[k]))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

// ── Counter ──────────────────────────────────────────────────────────────

// counterData is the shared storage for a counter.
type counterData struct {
	mu     sync.Mutex
	help   string
	values map[string]float64
}

// Counter is a monotonically increasing metric.
type Counter struct {
	data *counterData
}

// Inc increments the counter for the given label values.
func (c *Counter) Inc(labels Labels) {
	c.data.mu.Lock()
	defer c.data.mu.Unlock()
	key := labels.key()
	c.data.values[key]++
}

// ── Gauge ────────────────────────────────────────────────────────────────

// gaugeData is the shared storage for a gauge.
type gaugeData struct {
	mu     sync.Mutex
	help   string
	values map[string]float64
}

// Gauge is a metric that can go up and down.
type Gauge struct {
	data *gaugeData
}

// Set sets the gauge value for the given label values.
func (g *Gauge) Set(labels Labels, value float64) {
	g.data.mu.Lock()
	defer g.data.mu.Unlock()
	key := labels.key()
	g.data.values[key] = value
}

// ── Histogram ────────────────────────────────────────────────────────────

// histogramData is the shared storage for a histogram.
type histogramData struct {
	mu     sync.Mutex
	help   string
	bounds []float64
	counts []uint64
	sum    float64
	count  uint64
}

// Histogram is a cumulative histogram with fixed buckets.
type Histogram struct {
	data *histogramData
}

// Observe records a value.
func (h *Histogram) Observe(v float64) {
	h.data.mu.Lock()
	defer h.data.mu.Unlock()
	h.data.sum += v
	h.data.count++
	for i, bound := range h.data.bounds {
		if v <= bound {
			h.data.counts[i]++
		}
	}
}

// ── Registry ─────────────────────────────────────────────────────────────

// Registry is the metrics surface: holds all metrics and renders
// Prometheus text exposition format.
type Registry struct {
	mu         sync.Mutex
	counters   map[string]*counterData
	gauges     map[string]*gaugeData
	histograms map[string]*histogramData
}

// NewRegistry creates an empty metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*counterData),
		gauges:     make(map[string]*gaugeData),
		histograms: make(map[string]*histogramData),
	}
}

// Counter returns the named counter (creating it if needed).
func (r *Registry) Counter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.counters[name]
	if !ok {
		c = &counterData{help: help, values: make(map[string]float64)}
		r.counters[name] = c
	}
	return &Counter{data: c}
}

// Gauge returns the named gauge (creating it if needed).
func (r *Registry) Gauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gauges[name]
	if !ok {
		g = &gaugeData{help: help, values: make(map[string]float64)}
		r.gauges[name] = g
	}
	return &Gauge{data: g}
}

// Histogram returns the named histogram with the given bounds
// (creating it if needed). Bounds must be ascending; +Inf is implicit.
func (r *Registry) Histogram(name, help string, bounds []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.histograms[name]
	if !ok {
		counts := make([]uint64, len(bounds))
		h = &histogramData{help: help, bounds: bounds, counts: counts}
		r.histograms[name] = h
	}
	return &Histogram{data: h}
}

// WritePrometheus renders all metrics in Prometheus text exposition
// format (compatible with Alloy /metrics scraping).
func (r *Registry) WritePrometheus(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Counters (sorted by name).
	cNames := make([]string, 0, len(r.counters))
	for name := range r.counters {
		cNames = append(cNames, name)
	}
	sort.Strings(cNames)
	for _, name := range cNames {
		c := r.counters[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, c.help, name)
		c.mu.Lock()
		keys := sortedKeys(c.values)
		for _, k := range keys {
			fmt.Fprintf(w, "%s%s %v\n", name, formatLabels(parseKey(k)), c.values[k])
		}
		c.mu.Unlock()
	}

	// Gauges (sorted by name).
	gNames := make([]string, 0, len(r.gauges))
	for name := range r.gauges {
		gNames = append(gNames, name)
	}
	sort.Strings(gNames)
	for _, name := range gNames {
		g := r.gauges[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, g.help, name)
		g.mu.Lock()
		keys := sortedKeys(g.values)
		for _, k := range keys {
			fmt.Fprintf(w, "%s%s %v\n", name, formatLabels(parseKey(k)), g.values[k])
		}
		g.mu.Unlock()
	}

	// Histograms (sorted by name).
	hNames := make([]string, 0, len(r.histograms))
	for name := range r.histograms {
		hNames = append(hNames, name)
	}
	sort.Strings(hNames)
	for _, name := range hNames {
		h := r.histograms[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, h.help, name)
		h.mu.Lock()
		cumulative := uint64(0)
		for i, bound := range h.bounds {
			cumulative += h.counts[i]
			fmt.Fprintf(w, "%s_bucket{le=\"%v\"} %d\n", name, bound, cumulative)
		}
		fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", name, h.count)
		fmt.Fprintf(w, "%s_sum %v\n", name, h.sum)
		fmt.Fprintf(w, "%s_count %d\n", name, h.count)
		h.mu.Unlock()
	}
}

// sortedKeys returns the sorted keys of a float64-valued map.
func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// parseKey converts a stored key back into Labels for rendering.
func parseKey(key string) Labels {
	if key == "" {
		return nil
	}
	labels := Labels{}
	for _, pair := range strings.Split(key, ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}
	return labels
}
