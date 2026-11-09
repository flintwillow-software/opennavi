package memory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/tool"
)

// backfillArchive builds an archive with a pre-embedder entry and returns the
// ltm_backfill tool over it.
func backfillArchive(t *testing.T) tool.Tool {
	t.Helper()
	backend := inmem.New()
	if _, err := backend.Memory().Remember(datastore.MemoryRecord{
		Scope: datastore.ScopeMemories, Subject: testSubject, Content: "an old note",
	}); err != nil {
		t.Fatal(err)
	}
	lt, err := NewLongTerm(backend.Memory(), testSubject, LongTermConfig{
		Embedder: &countingEmbedder{dims: 3},
		Vectors:  &memVectors{},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := Tools(nil, lt)
	if err != nil {
		t.Fatal(err)
	}
	return toolNamed(t, tools, "ltm_backfill")
}

// TestBackfillToolReportsProgress: the envelope tells the caller what it did
// and how much is left, so "the archive is fully embedded" is distinguishable
// from "there is more" without a second call.
func TestBackfillToolReportsProgress(t *testing.T) {
	tl := backfillArchive(t)
	out, err := tl.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("ltm_backfill: %v", err)
	}
	var res struct {
		Embedded  int `json:"embedded"`
		Skipped   int `json:"skipped"`
		Remaining int `json:"remaining"`
		Entries   []struct {
			ID      string `json:"id"`
			Excerpt string `json:"excerpt"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if res.Embedded != 1 {
		t.Fatalf("embedded = %d, want 1", res.Embedded)
	}
	if res.Remaining != 0 {
		t.Fatalf("remaining = %d, want 0", res.Remaining)
	}
	if len(res.Entries) != 1 || res.Entries[0].Excerpt == "" {
		t.Fatalf("entries = %+v, want one with an excerpt", res.Entries)
	}
}

// TestBackfillToolEnforcesTheLimit: the cap is enforced server-side, not
// trusted from the caller, for the same reason ltm_search's is.
func TestBackfillToolEnforcesTheLimit(t *testing.T) {
	tl := backfillArchive(t)
	// A request beyond the cap is honoured at the cap rather than refused.
	if _, err := tl.Handler(context.Background(), map[string]any{"limit": float64(1e9)}); err != nil {
		t.Fatalf("an over-cap limit must be clamped, not refused: %v", err)
	}
	// A non-positive limit selects the default.
	if _, err := tl.Handler(context.Background(), map[string]any{"limit": float64(0)}); err != nil {
		t.Fatalf("a zero limit must select the default: %v", err)
	}
}

// TestBackfillToolRejectsANonIntegerLimit.
func TestBackfillToolRejectsANonIntegerLimit(t *testing.T) {
	tl := backfillArchive(t)
	if _, err := tl.Handler(context.Background(), map[string]any{"limit": "twenty"}); err == nil {
		t.Fatal("a non-integer limit must be refused")
	}
}

// TestBackfillToolWithoutAnEmbedderIsAnError: the tool is declared alongside
// the tier, so a navi with no embedder that calls it gets a clear reason rather
// than a silent no-op.
func TestBackfillToolWithoutAnEmbedderIsAnError(t *testing.T) {
	lt, err := NewLongTerm(inmem.New().Memory(), testSubject, LongTermConfig{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := Tools(nil, lt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = toolNamed(t, tools, "ltm_backfill").Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("backfill without an embedder must be an error")
	}
}
