package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/tool"
)

func longTermFixture(t *testing.T) (*Service, *LongTerm, *inmem.Backend) {
	t.Helper()
	svc, backend := newService(t)
	return svc, newLongTerm(t, backend), backend
}

func ltmTool(t *testing.T, svc *Service, lt *LongTerm, name string) tool.Tool {
	t.Helper()
	tools, err := Tools(svc, lt)
	if err != nil {
		t.Fatal(err)
	}
	return toolNamed(t, tools, name)
}

// TestLongTermWriteAndSearch is the core of NAVI-578: an entry written to the
// archive is findable by search and is NOT injected into context — which is the
// tier boundary, not an optimisation.
func TestLongTermWriteAndSearch(t *testing.T) {
	svc, lt, _ := longTermFixture(t)
	write := ltmTool(t, svc, lt, "ltm_write")
	search := ltmTool(t, svc, lt, "ltm_search")

	out, err := write.Handler(context.Background(), map[string]any{
		"content": "the vendor contract renews in March",
		"scope":   "memories",
		"tags":    []any{"vendor", "contract"},
	})
	if err != nil {
		t.Fatalf("ltm_write: %v", err)
	}
	if !strings.Contains(out, "archived") {
		t.Fatalf("ltm_write output = %q", out)
	}

	// The tier boundary: a long-term entry must NOT reach the injected context.
	if block := svc.Inject(); strings.Contains(block, "vendor contract") {
		t.Fatalf("a long-term entry leaked into the always-injected tier:\n%s", block)
	}

	found, err := search.Handler(context.Background(), map[string]any{"query": "vendor"})
	if err != nil {
		t.Fatalf("ltm_search: %v", err)
	}
	if !strings.Contains(found, "vendor contract renews") {
		t.Fatalf("ltm_search did not find the entry: %s", found)
	}
	// Tag filter.
	found, err = search.Handler(context.Background(), map[string]any{"tags": []any{"contract"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(found, "vendor contract") {
		t.Fatalf("tag filter missed the entry: %s", found)
	}
	found, err = search.Handler(context.Background(), map[string]any{"tags": []any{"nonexistent"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(found, "vendor contract") {
		t.Fatalf("tag filter matched an entry it should not: %s", found)
	}
}

// TestSTMCannotWriteTheArchiveAndViceVersa is the split's whole point: the
// wrong tier is unrepresentable, not merely documented.
func TestSTMCannotWriteTheArchiveAndViceVersa(t *testing.T) {
	svc, lt, _ := longTermFixture(t)
	stm := ltmTool(t, svc, lt, "stm_write")
	ltm := ltmTool(t, svc, lt, "ltm_write")

	// stm_write refuses a long-term scope.
	if _, err := stm.Handler(context.Background(), map[string]any{
		"content": "x", "scope": "memories",
	}); err == nil {
		t.Fatal("stm_write accepted a long-term scope")
	}
	if _, err := stm.Handler(context.Background(), map[string]any{
		"content": "x", "scope": "navi",
	}); err == nil {
		t.Fatal("stm_write accepted a long-term scope")
	}
	// ltm_write refuses a short-term scope.
	if _, err := ltm.Handler(context.Background(), map[string]any{
		"content": "x", "scope": "session",
	}); err == nil {
		t.Fatal("ltm_write accepted a short-term scope")
	}
	if _, err := ltm.Handler(context.Background(), map[string]any{
		"content": "x", "scope": "user",
	}); err == nil {
		t.Fatal("ltm_write accepted a short-term scope")
	}
}

// TestLTMReadReturnsTheWholeEntry: a search returns an excerpt to judge
// relevance; ltm_read is the second deliberate call for the full text.
func TestLTMReadReturnsTheWholeEntry(t *testing.T) {
	svc, lt, _ := longTermFixture(t)
	write := ltmTool(t, svc, lt, "ltm_write")
	search := ltmTool(t, svc, lt, "ltm_search")
	read := ltmTool(t, svc, lt, "ltm_read")

	long := strings.Repeat("detail ", 100) // well past the excerpt cap
	if _, err := write.Handler(context.Background(), map[string]any{
		"content": "prefix " + long,
	}); err != nil {
		t.Fatal(err)
	}

	found, err := search.Handler(context.Background(), map[string]any{"query": "prefix"})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Entries []struct {
			ID      string `json:"id"`
			Excerpt string `json:"excerpt"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(found), &env); err != nil {
		t.Fatalf("decode search: %v (%s)", err, found)
	}
	if len(env.Entries) != 1 {
		t.Fatalf("search returned %d entries, want 1", len(env.Entries))
	}
	if len(env.Entries[0].Excerpt) > ltmExcerptMax+8 {
		t.Fatalf("search excerpt is %d chars, want it bounded near %d", len(env.Entries[0].Excerpt), ltmExcerptMax)
	}
	if !strings.HasSuffix(env.Entries[0].Excerpt, "…") {
		t.Fatalf("a truncated excerpt should say so: %q", env.Entries[0].Excerpt)
	}

	got, err := read.Handler(context.Background(), map[string]any{"id": env.Entries[0].ID})
	if err != nil {
		t.Fatalf("ltm_read: %v", err)
	}
	var full struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(got), &full); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.Content, strings.Repeat("detail ", 100)) {
		t.Fatalf("ltm_read did not return the whole entry (len %d)", len(full.Content))
	}
	// Reading an unknown id is an error, not an empty result.
	if _, err := read.Handler(context.Background(), map[string]any{"id": "nope"}); err == nil {
		t.Fatal("ltm_read of an unknown id must fail")
	}
}

// TestLTMSearchIsBounded is the acceptance criterion: a bounded, ranked read.
// The bound is enforced server-side, not trusted from the caller.
func TestLTMSearchIsBounded(t *testing.T) {
	svc, lt, _ := longTermFixture(t)
	write := ltmTool(t, svc, lt, "ltm_write")
	search := ltmTool(t, svc, lt, "ltm_search")

	for i := 0; i < ltmSearchMaxLimit+10; i++ {
		if _, err := write.Handler(context.Background(), map[string]any{
			"content": "entry about widgets",
		}); err != nil {
			t.Fatal(err)
		}
	}

	type envelope struct {
		Total     int  `json:"total"`
		Returned  int  `json:"returned"`
		Truncated bool `json:"truncated"`
		Entries   []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}

	// Default limit applies.
	out, err := search.Handler(context.Background(), map[string]any{"query": "widgets"})
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Returned != ltmSearchDefaultLimit || len(env.Entries) != ltmSearchDefaultLimit {
		t.Fatalf("default returned %d, want %d", len(env.Entries), ltmSearchDefaultLimit)
	}
	if env.Total != ltmSearchMaxLimit+10 {
		t.Fatalf("total = %d, want %d (what matched, before the bound)", env.Total, ltmSearchMaxLimit+10)
	}
	if !env.Truncated {
		t.Fatal("truncated should be true when the bound cut the result")
	}

	// Over-cap is honoured AT the cap, not refused and not obeyed.
	out, err = search.Handler(context.Background(), map[string]any{"query": "widgets", "limit": float64(10000)})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Returned != ltmSearchMaxLimit || len(env.Entries) != ltmSearchMaxLimit {
		t.Fatalf("limit=10000 returned %d, want the cap %d", len(env.Entries), ltmSearchMaxLimit)
	}
}

// TestLTMPruneIsDeliberateAndRefusesAnUnconstrainedCall: retention is an
// explicit act (ADR 0037 decision 7), and "prune everything" is never what a
// deliberate act means.
func TestLTMPruneIsDeliberateAndRefusesAnUnconstrainedCall(t *testing.T) {
	svc, lt, _ := longTermFixture(t)
	write := ltmTool(t, svc, lt, "ltm_write")
	prune := ltmTool(t, svc, lt, "ltm_prune")
	search := ltmTool(t, svc, lt, "ltm_search")

	if _, err := prune.Handler(context.Background(), map[string]any{}); err == nil {
		t.Fatal("an unconstrained prune must be refused — it would empty the archive")
	}

	for _, c := range []struct{ content, tag string }{
		{"keep this one", "keep"},
		{"drop this one", "drop"},
	} {
		if _, err := write.Handler(context.Background(), map[string]any{
			"content": c.content, "tags": []any{c.tag},
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := prune.Handler(context.Background(), map[string]any{"tags": []any{"drop"}})
	if err != nil {
		t.Fatalf("ltm_prune: %v", err)
	}
	var res struct {
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 {
		t.Fatalf("pruned %d entries, want 1", res.Removed)
	}
	// The other entry survives.
	found, err := search.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(found, "drop this one") {
		t.Fatalf("pruned entry still present: %s", found)
	}
	if !strings.Contains(found, "keep this one") {
		t.Fatalf("prune removed an entry it should not have: %s", found)
	}
}

// TestLTMSearchNeverReturnsShortTermEntries: the tier boundary holds on the
// read path too, not only the write path.
func TestLTMSearchNeverReturnsShortTermEntries(t *testing.T) {
	svc, lt, _ := longTermFixture(t)
	stm := ltmTool(t, svc, lt, "stm_write")
	search := ltmTool(t, svc, lt, "ltm_search")

	if _, err := stm.Handler(context.Background(), map[string]any{
		"content": "a short-term note about widgets", "scope": "session",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := search.Handler(context.Background(), map[string]any{"query": "widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "short-term note") {
		t.Fatalf("an archive search returned a short-term entry: %s", out)
	}
}

// TestLongTermWriteForcesTheNavisOwnSubject: an archive entry is the navi's own
// knowledge; a caller must not be able to write into another principal's notes.
func TestLongTermWriteForcesTheNavisOwnSubject(t *testing.T) {
	backend := inmem.New()
	lt, err := NewLongTerm(backend.Memory(), principal.MustParse("navi:mine"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := lt.Write(datastore.MemoryRecord{
		Scope:   datastore.ScopeMemories,
		Subject: principal.MustParse("navi:someone-else"),
		Content: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Subject.String() != "navi:mine" {
		t.Fatalf("subject = %q, want the navi's own", stored.Subject.String())
	}
}
