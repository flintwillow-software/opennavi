package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/object"
)

// This file is the queue page's filter and sort model (NAVI-592, ADR 0038
// decision 4). It is deliberately separate from the model so the translation
// from a reader's filter to the surface's object.Query is one reviewable
// function, and so a test can assert on the query a filter produces without
// standing up the whole TUI.
//
// The surface already implements equality and $in on Query.Filter
// (navi/types.go filterObjects/objectMatches), and it filters BEFORE paging
// (ADR 0028) so a filtered page stays full and the cursor indexes the
// filtered list. The TUI previously passed only a limit and never a filter;
// this is the wiring.

// queueFilter is the reader's active filter on the queue page. The zero value
// is "no filter": every field empty or unset.
type queueFilter struct {
	// statuses is a set of lifecycle statuses to keep. Empty = all. It maps
	// to a Query.Filter $in on the status field, which is the operator the
	// surface already implements.
	statuses map[string]bool
	// priorityMin and priorityMax bound the priority field, inclusive. Zero
	// for both means unbounded (priority 0 is the HIGHEST per ADR 0035, so a
	// range is [min,max] with min <= max and 0 meaning "no bound").
	priorityMin int
	priorityMax int
	// model keeps only thoughts pinned to this model. Empty = all.
	model string
	// text is a substring to match against the prompt. Empty = all. It is
	// applied client-side because the surface's Filter is a declared
	// per-field equality/$in, not a substring operator (see below).
	text string
}

// allStatuses is the lifecycle vocabulary the status filter cycles over. It
// mirrors the thought type's declared status enum.
var allStatuses = []string{"pending", "running", "paused", "complete", "failed", "cancelled"}

// active reports whether any filter is set — the condition for showing the
// filter indicator in the header. A filter that is silently in force is the
// bug this exists to prevent.
func (f queueFilter) active() bool {
	return len(f.statuses) > 0 || f.priorityMin > 0 || f.priorityMax > 0 ||
		strings.TrimSpace(f.model) != "" || strings.TrimSpace(f.text) != ""
}

// clear resets every axis.
func (f *queueFilter) clear() { *f = queueFilter{} }

// toggleStatus adds or removes one status from the set, so multi-select is a
// sequence of toggles rather than a modal.
func (f *queueFilter) toggleStatus(s string) {
	if f.statuses == nil {
		f.statuses = make(map[string]bool)
	}
	if f.statuses[s] {
		delete(f.statuses, s)
	} else {
		f.statuses[s] = true
	}
	if len(f.statuses) == 0 {
		f.statuses = nil
	}
}

// statusList returns the selected statuses in the canonical vocabulary order,
// so the header text is stable regardless of toggle order.
func (f queueFilter) statusList() []string {
	if len(f.statuses) == 0 {
		return nil
	}
	out := make([]string, 0, len(f.statuses))
	for _, s := range allStatuses {
		if f.statuses[s] {
			out = append(out, s)
		}
	}
	return out
}

// query builds the surface query the filter implies. The status and model
// axes go into Query.Filter (equality and $in); the priority range does too
// when it is a single value, and the prompt text is NOT pushed down (see
// matchesText) because the surface's Filter has no substring operator.
func (f queueFilter) query(limit int) object.Query {
	q := object.Query{Limit: limit}
	filter := map[string]any{}

	if statuses := f.statusList(); len(statuses) == 1 {
		filter["status"] = statuses[0]
	} else if len(statuses) > 1 {
		// $in is the operator the surface implements for a set.
		vals := make([]any, len(statuses))
		for i, s := range statuses {
			vals[i] = s
		}
		filter["status"] = map[string]any{"$in": vals}
	}

	if m := strings.TrimSpace(f.model); m != "" {
		filter["model"] = m
	}

	// A priority range is an equality only when the bounds coincide; a true
	// range has no $in form and the surface's Filter has no $gte/$lte, so it
	// is applied client-side by matchesPriority. Pushing a single value down
	// keeps the page full for the common "only p0" case.
	if f.priorityMin > 0 && f.priorityMin == f.priorityMax {
		filter["priority"] = f.priorityMin
	}

	if len(filter) > 0 {
		q.Filter = filter
	}
	return q
}

// matchesPriority reports whether a priority passes the range. It is applied
// client-side because the surface's Filter has no range operator; the issue is
// explicit that a filter the surface cannot express should extend portal/view
// rather than special-case the TUI, and a range is the one axis with no
// representation today (tracked for that extension rather than silently
// mishandled here).
func (f queueFilter) matchesPriority(prio int) bool {
	if f.priorityMin > 0 && prio < f.priorityMin {
		return false
	}
	if f.priorityMax > 0 && prio > f.priorityMax {
		return false
	}
	return true
}

// matchesText reports whether a prompt passes the substring. It is
// client-side for the same reason as the priority range: the surface's Filter
// is equality/$in, not a text search.
func (f queueFilter) matchesText(prompt string) bool {
	t := strings.TrimSpace(f.text)
	if t == "" {
		return true
	}
	return strings.Contains(strings.ToLower(prompt), strings.ToLower(t))
}

// describe renders the filter for the header: "status=pending,failed ·
// priority 1-3 · model=… · text=…". It is the visible half of the contract —
// a reader must be able to see why rows are missing.
func (f queueFilter) describe() string {
	var parts []string
	if s := f.statusList(); len(s) > 0 {
		parts = append(parts, "status="+strings.Join(s, ","))
	}
	if f.priorityMin > 0 || f.priorityMax > 0 {
		parts = append(parts, "priority "+f.priorityRange())
	}
	if m := strings.TrimSpace(f.model); m != "" {
		parts = append(parts, "model="+m)
	}
	if t := strings.TrimSpace(f.text); t != "" {
		parts = append(parts, "text="+t)
	}
	return strings.Join(parts, " · ")
}

// priorityRange renders the priority bounds compactly: "2" for an exact
// value, "1-3" for a range, ">=2" / "<=3" for one-sided.
func (f queueFilter) priorityRange() string {
	switch {
	case f.priorityMin > 0 && f.priorityMax > 0 && f.priorityMin == f.priorityMax:
		return fmt.Sprintf("%d", f.priorityMin)
	case f.priorityMin > 0 && f.priorityMax > 0:
		return fmt.Sprintf("%d-%d", f.priorityMin, f.priorityMax)
	case f.priorityMin > 0:
		return fmt.Sprintf(">=%d", f.priorityMin)
	case f.priorityMax > 0:
		return fmt.Sprintf("<=%d", f.priorityMax)
	}
	return ""
}

// parseFilter reads the filter bar's text into a filter. The syntax is a
// space-separated list of `key:value` terms, with bare words treated as prompt
// text so the common case (`/ migration`) is just typing. Keys: status (comma
// list), priority (a value, a-b range, or >=a / <=a), model, text.
//
// It returns an error message for an unknown key or an unparsable value, so
// the bar can say what it did not understand rather than silently ignoring a
// term — a filter that quietly drops half its terms is the same class of
// defect as one that is invisible.
func parseFilter(s string) (queueFilter, string) {
	var f queueFilter
	for _, term := range strings.Fields(s) {
		key, val, ok := strings.Cut(term, ":")
		if !ok {
			// A bare word is prompt text (the common case).
			if f.text != "" {
				f.text += " "
			}
			f.text += term
			continue
		}
		switch strings.ToLower(key) {
		case "status", "state":
			for _, v := range strings.Split(val, ",") {
				v = strings.ToLower(strings.TrimSpace(v))
				if v == "" {
					continue
				}
				if !knownStatus(v) {
					return f, "unknown status " + v
				}
				f.toggleStatus(v)
			}
		case "priority", "prio", "p":
			lo, hi, err := parsePriorityRange(val)
			if err != "" {
				return f, err
			}
			f.priorityMin, f.priorityMax = lo, hi
		case "model":
			f.model = val
		case "text", "prompt":
			f.text = val
		default:
			return f, "unknown filter key " + key
		}
	}
	return f, ""
}

// knownStatus reports whether s is in the lifecycle vocabulary.
func knownStatus(s string) bool {
	for _, v := range allStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// parsePriorityRange reads "3", "1-3", ">=2", or "<=3" into bounds.
func parsePriorityRange(v string) (int, int, string) {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(v, ">="):
		n, err := strconv.Atoi(strings.TrimPrefix(v, ">="))
		if err != nil {
			return 0, 0, "bad priority " + v
		}
		return n, 0, ""
	case strings.HasPrefix(v, "<="):
		n, err := strconv.Atoi(strings.TrimPrefix(v, "<="))
		if err != nil {
			return 0, 0, "bad priority " + v
		}
		return 0, n, ""
	case strings.Contains(v, "-"):
		lo, hi, _ := strings.Cut(v, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil {
			return 0, 0, "bad priority range " + v
		}
		if a > b {
			a, b = b, a
		}
		return a, b, ""
	default:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, 0, "bad priority " + v
		}
		return n, n, ""
	}
}

// ── sort ────────────────────────────────────────────────────────────────

// sortKey names a queue ordering. The zero value is the surface's own order
// (newest first), so an unconfigured queue is unchanged.
type sortKey string

const (
	sortDefault  sortKey = ""
	sortCreated  sortKey = "created"
	sortUpdated  sortKey = "updated"
	sortPriority sortKey = "priority"
	sortStatus   sortKey = "status"
)

// sortCycle is the order `S` cycles through.
var sortCycle = []sortKey{sortDefault, sortPriority, sortCreated, sortUpdated, sortStatus}

// sortLabel is the header text for a key.
func sortLabel(k sortKey) string {
	switch k {
	case sortPriority:
		return "priority"
	case sortCreated:
		return "created"
	case sortUpdated:
		return "updated"
	case sortStatus:
		return "status"
	}
	return "default"
}

// nextSort returns the key after k in the cycle.
func nextSort(k sortKey) sortKey {
	for i, c := range sortCycle {
		if c == k {
			return sortCycle[(i+1)%len(sortCycle)]
		}
	}
	return sortPriority
}

// sortRows orders a page's objects for display. The surface serves newest
// first and has no sort parameter, so this is a stable client-side sort with
// the surface's order preserved as the tiebreak (ADR 0038 decision 4 says
// server-side where supported, otherwise stable client-side).
func sortRows(objs []object.Object, key sortKey) []object.Object {
	if key == sortDefault || len(objs) < 2 {
		return objs
	}
	out := append([]object.Object(nil), objs...)
	sort.SliceStable(out, func(i, j int) bool {
		return lessBy(out[i], out[j], key)
	})
	return out
}

// lessBy is the per-key comparison. Priority is ascending (0 is highest per
// ADR 0035), status follows the lifecycle vocabulary order, and the time keys
// sort newest first to match the surface's own default.
func lessBy(a, b object.Object, key sortKey) bool {
	switch key {
	case sortPriority:
		return intField(a.Fields["priority"]) < intField(b.Fields["priority"])
	case sortStatus:
		return statusRank(stringField(a, "status")) < statusRank(stringField(b, "status"))
	case sortCreated:
		return timeField(a, "created_at").After(timeField(b, "created_at"))
	case sortUpdated:
		return timeField(a, "updated_at").After(timeField(b, "updated_at"))
	}
	return false
}

// statusRank orders the lifecycle vocabulary for the status sort, so
// "running" sorts before "complete" rather than alphabetically.
func statusRank(s string) int {
	for i, v := range allStatuses {
		if v == s {
			return i
		}
	}
	return len(allStatuses)
}

// timeField reads a time field from an object, tolerating both shapes it
// arrives in: a time.Time in-process and an RFC3339 string over the gateway
// (the JSON decode of the same field). A missing or unparsable value is the
// zero time, which sorts last rather than failing.
func timeField(o object.Object, name string) time.Time {
	switch v := o.Fields[name].(type) {
	case time.Time:
		return v
	case string:
		t, _ := time.Parse(time.RFC3339Nano, v)
		return t
	}
	return time.Time{}
}
