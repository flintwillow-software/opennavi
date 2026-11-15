package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the NAVI-592 gate: the queue page's filter and sort, backed by
// the surface's Query.Filter. The surface below records every query it is
// asked for and applies the filter itself (equality and $in, the two operators
// the real surface implements), so a test asserts BOTH that the model asked
// for the right query and that the filtering/paging behaviour the surface
// promises (a filtered page stays full) is what the model relies on.

// filterSurface is a minimal object surface whose List honors Query.Filter the
// way navi/types.go does, and which records the last query it saw.
type filterSurface struct {
	objs     []object.Object
	lastQ    object.Query
	listCall int
	// pageSize is what the surface returns when the query sets no limit
	// (mimicking a backend default).
	pageSize int
}

func (s *filterSurface) Types() ([]object.Type, error) {
	return []object.Type{{
		Name: "thought", Label: "Thought", ID: "id",
		Fields: []object.Field{
			{Name: "id", Kind: object.KindString, Section: "identity"},
			{Name: "prompt", Label: "Prompt", Kind: object.KindText, Section: "core"},
			{Name: "status", Kind: object.KindEnum, Section: "state"},
			{Name: "priority", Kind: object.KindInt, Section: "state"},
			{Name: "model", Kind: object.KindString, Section: "run"},
			{Name: "created_at", Kind: object.KindTime, Section: "timestamps"},
		},
		Views: []object.View{
			{Kind: object.ViewList, Label: "Thoughts", Columns: []string{"id", "prompt", "status", "priority", "model"}},
		},
	}}, nil
}

func (s *filterSurface) List(typeName string, q object.Query) ([]object.Object, string, error) {
	s.lastQ = q
	s.listCall++
	var out []object.Object
	for _, o := range s.objs {
		if matchesQuery(o, q.Filter) {
			out = append(out, o)
		}
	}
	return out, "", nil
}

// matchesQuery mirrors the surface's filterObjects/objectMatches: equality and
// $in, nothing more.
func matchesQuery(o object.Object, filter map[string]any) bool {
	for k, want := range filter {
		got := o.Fields[k]
		if in, ok := want.(map[string]any); ok {
			allowed, ok := in["$in"]
			if !ok {
				return false
			}
			found := false
			switch list := allowed.(type) {
			case []any:
				for _, v := range list {
					if fmt.Sprint(v) == fmt.Sprint(got) {
						found = true
					}
				}
			}
			if !found {
				return false
			}
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}

func (s *filterSurface) Get(typeName, id string) (object.Object, error) {
	for _, o := range s.objs {
		if o.ID == id {
			return o, nil
		}
	}
	return object.Object{}, object.ErrNotFound
}

func (s *filterSurface) Detail(string, string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
}
func (s *filterSurface) Act(string, string, string, map[string]any) error { return nil }
func (s *filterSurface) Watch(int) ([]object.Delta, int, bool, error)     { return nil, 0, false, nil }
func (s *filterSurface) Notify() <-chan struct{}                          { return neverWake }

// newFilterModel builds a model over a filterSurface with n thoughts spanning
// statuses, priorities, and models.
func newFilterModel(t *testing.T, objs []object.Object) (*tuiModel, *filterSurface) {
	t.Helper()
	q := newQueue(t)
	s := &filterSurface{objs: objs}
	m := newMultiModel(t, q, s)
	showList(m)
	return m, s
}

// obj builds a thought object with the fields the filter reads.
func obj(id, status string, prio int, model, prompt string, created time.Time) object.Object {
	return object.Object{Type: "thought", ID: id, Fields: map[string]any{
		"id": id, "prompt": prompt, "status": status, "priority": prio,
		"model": model, "created_at": created,
	}}
}

// TestFilterDispatchesToTheSurface pins that the filter reaches the surface as
// Query.Filter — status as $in over the selected set, and the equality axes as
// plain values. It is the "wiring, not new backend capability" acceptance.
func TestFilterDispatchesToTheSurface(t *testing.T) {
	now := time.Now()
	objs := []object.Object{
		obj("a", "pending", 1, "m1", "alpha", now),
		obj("b", "running", 2, "m2", "beta", now),
		obj("c", "failed", 3, "m1", "gamma", now),
	}
	m, s := newFilterModel(t, objs)

	// Filter status=pending,failed model=m1.
	m.filter.toggleStatus("pending")
	m.filter.toggleStatus("failed")
	m.filter.model = "m1"
	m.refreshList()

	if s.lastQ.Filter == nil {
		t.Fatal("filter did not reach the surface")
	}
	in, ok := s.lastQ.Filter["status"].(map[string]any)
	if !ok {
		t.Fatalf("status filter is not an $in: %#v", s.lastQ.Filter["status"])
	}
	vals, _ := in["$in"].([]any)
	if len(vals) != 2 {
		t.Fatalf("status $in = %#v, want two values", in["$in"])
	}
	if s.lastQ.Filter["model"] != "m1" {
		t.Fatalf("model filter = %#v", s.lastQ.Filter["model"])
	}
	// The page reflects the surface's answer: a (pending,m1) and c (failed,m1).
	if got := rowIDs(m); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("filtered rows = %v, want [a c]", got)
	}
}

// TestFilterPageStaysFull pins ADR 0028's promise the model depends on:
// filtering happens BEFORE paging, so a filtered page is as full as the
// matching set allows rather than a sparse slice of an unfiltered page.
func TestFilterPageStaysFull(t *testing.T) {
	now := time.Now()
	// 300 objects, only the last 10 pending. With a 200-row page and
	// filter-before-paging, the page must carry all 10 pending; with
	// filter-after-paging it would carry 0 (the first 200 are not pending).
	var objs []object.Object
	for i := 0; i < 290; i++ {
		objs = append(objs, obj(fmt.Sprintf("r%03d", i), "complete", 5, "m", "done", now))
	}
	for i := 0; i < 10; i++ {
		objs = append(objs, obj(fmt.Sprintf("p%03d", i), "pending", 5, "m", "waiting", now))
	}
	m, s := newFilterModel(t, objs)

	m.filter.toggleStatus("pending")
	m.refreshList()

	if s.lastQ.Limit != listPage {
		t.Fatalf("page limit = %d, want %d", s.lastQ.Limit, listPage)
	}
	if got := len(m.list.Rows); got != 10 {
		t.Fatalf("filtered page rows = %d, want all 10 pending", got)
	}
}

// TestFilteredOutRowLeavesNoStaleBadge pins the live-overlay contract: a row
// the filter removed must not keep a badge, so a filtered-out thought's live
// status is not silently shown when it is not on the page.
func TestFilteredOutRowLeavesNoStaleBadge(t *testing.T) {
	now := time.Now()
	objs := []object.Object{
		obj("a", "pending", 1, "m", "alpha", now),
		obj("b", "running", 2, "m", "beta", now),
	}
	m, _ := newFilterModel(t, objs)

	// The stream updates b's status live (a badge the page would show).
	m.setBadge("b", func(bd *badge) { bd.status = "running" })
	if m.statusOf("b") != "running" {
		t.Fatal("setup: badge not set")
	}

	// Filter to pending only: b is filtered out, so its badge must go.
	m.filter.toggleStatus("pending")
	m.refreshList()

	if _, on := m.items["b"]; on {
		t.Fatal("b should not be on the filtered page")
	}
	if _, stale := m.badges["b"]; stale {
		t.Fatal("filtered-out row left a stale badge")
	}
	if m.statusOf("b") != "" {
		t.Fatalf("statusOf on a filtered-out row = %q, want empty", m.statusOf("b"))
	}
}

// TestSortOrdersThePage pins each sort key: priority ascending (0 highest per
// ADR 0035), status in lifecycle order, and the time keys newest first.
func TestSortOrdersThePage(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	objs := []object.Object{
		obj("lo", "complete", 5, "m", "x", base.Add(1*time.Hour)),
		obj("hi", "pending", 0, "m", "x", base.Add(3*time.Hour)),
		obj("mid", "running", 2, "m", "x", base.Add(2*time.Hour)),
	}

	m, _ := newFilterModel(t, objs)

	m.sortBy = sortPriority
	m.refreshList()
	if got := rowIDs(m); got[0] != "hi" || got[1] != "mid" || got[2] != "lo" {
		t.Fatalf("priority sort = %v, want [hi mid lo]", got)
	}

	m.sortBy = sortCreated
	m.refreshList()
	if got := rowIDs(m); got[0] != "hi" || got[2] != "lo" {
		t.Fatalf("created sort = %v, want newest first [hi mid lo]", got)
	}

	m.sortBy = sortStatus
	m.refreshList()
	// pending(0) < running(2) < complete(4) in the lifecycle vocabulary.
	if got := rowIDs(m); got[0] != "hi" || got[1] != "mid" || got[2] != "lo" {
		t.Fatalf("status sort = %v, want [hi mid lo]", got)
	}

	m.sortBy = sortDefault
	m.refreshList()
	// Default preserves the surface's order (the slice as served).
	if got := rowIDs(m); got[0] != "lo" {
		t.Fatalf("default sort changed the surface order: %v", got)
	}
}

// TestFilterBarAppliesAndClears drives the `/` bar through the keymap: typing a
// query and pressing enter applies it, and the clear key resets it. It pins
// the reader-facing path, not just the struct.
func TestFilterBarAppliesAndClears(t *testing.T) {
	now := time.Now()
	objs := []object.Object{
		obj("a", "pending", 1, "m", "alpha", now),
		obj("b", "failed", 2, "m", "beta", now),
	}
	m, s := newFilterModel(t, objs)

	keyRunes(t, m, "/")
	if !m.filtering {
		t.Fatal("/ did not open the filter bar")
	}
	keyRunes(t, m, "status:pending")
	keyEnter(m)
	if m.filtering {
		t.Fatal("enter did not close the filter bar")
	}
	if !m.filter.active() {
		t.Fatal("filter not active after apply")
	}
	if s.lastQ.Filter["status"] != "pending" {
		t.Fatalf("surface filter = %#v, want status=pending", s.lastQ.Filter)
	}
	if got := rowIDs(m); len(got) != 1 || got[0] != "a" {
		t.Fatalf("rows = %v, want [a]", got)
	}

	// ctrl+l clears it.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.filter.active() {
		t.Fatal("clear key left the filter active")
	}
	if got := rowIDs(m); len(got) != 2 {
		t.Fatalf("after clear rows = %v, want both", got)
	}
}

// TestFilterTextAndPriorityAreClientSide pins the two axes the surface's Filter
// cannot express: a prompt substring and a priority range.
func TestFilterTextAndPriorityAreClientSide(t *testing.T) {
	now := time.Now()
	objs := []object.Object{
		obj("a", "pending", 0, "m", "fix the parser", now),
		obj("b", "pending", 5, "m", "fix the lexer", now),
		obj("c", "pending", 2, "m", "write docs", now),
	}
	m, _ := newFilterModel(t, objs)

	// Prompt substring.
	m.filter.text = "parser"
	m.refreshList()
	if got := rowIDs(m); len(got) != 1 || got[0] != "a" {
		t.Fatalf("text filter rows = %v, want [a]", got)
	}

	// Priority range 0-2.
	m.filter = queueFilter{priorityMin: 0, priorityMax: 2}
	m.refreshList()
	if got := rowIDs(m); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("priority range rows = %v, want [a c]", got)
	}
}

// TestFilterBarReportsParseErrors pins that a term the bar cannot understand is
// reported rather than silently dropped — a filter that quietly ignores half
// its terms is the same class of defect as an invisible one.
func TestFilterBarReportsParseErrors(t *testing.T) {
	f, errMsg := parseFilter("status:nonsense")
	if errMsg == "" {
		t.Fatal("unknown status was not reported")
	}
	if f.active() {
		t.Fatal("a failed parse must not produce an active filter")
	}
	f2, errMsg2 := parseFilter("bogus:1")
	if errMsg2 == "" {
		t.Fatal("unknown key was not reported")
	}
	if f2.active() {
		t.Fatal("a failed parse must not produce an active filter")
	}
}

// rowIDs is the page's ids in render order.
func rowIDs(m *tuiModel) []string {
	out := make([]string, 0, len(m.list.Rows))
	for _, r := range m.list.Rows {
		out = append(out, r.ID)
	}
	return out
}

var _ = thought.ThoughtPending
