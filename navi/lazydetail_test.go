package navi

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/thought"
)

// NAVI-377 acceptance: reads are lazy, detail is collectable, writes
// materialize through the ordered writer, and never-accessed history costs
// no detail RAM. The in-memory backend implements conformance.ReadReporter,
// so body loads are countable; ResidentTranscripts reports what the
// lazy-transcript layer holds.

const lazyBody = "body-entry-content"

// readReporter is conformance.ReadReporter's shape; inmem implements it.
type readReporter interface {
	ResetReads()
	BodyReads() int
}

// seedTranscript persists one thought row and a transcript body directly
// into the backend, standing in for history written by a previous process.
func seedTranscript(t *testing.T, b datastore.Backend, id string, messages int) {
	t.Helper()
	v := b.Thoughts()
	if err := v.SaveThought(&thought.Thought{
		ID: id, Prompt: "history " + id, Status: thought.ThoughtComplete,
	}); err != nil {
		t.Fatalf("SaveThought(%s): %v", id, err)
	}
	entries := make([]datastore.Entry, 0, messages)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < messages; i++ {
		entries = append(entries, datastore.Entry{
			ID:      fmt.Sprintf("%s-e%d", id, i),
			Time:    base.Add(time.Duration(i) * time.Second),
			Role:    datastore.RoleAssistant,
			Message: lazyBody,
		})
	}
	if err := v.SaveTranscript(id, entries); err != nil {
		t.Fatalf("SaveTranscript(%s): %v", id, err)
	}
}

func lazyTestNavi(t *testing.T, b datastore.Backend) *Navi {
	t.Helper()
	n := New("lazy-detail", WithProvider(llm.NewMockProvider(streamEvents("ok"))), WithWorkers(1))
	if err := n.Datastores.Add(datastore.Candidate{
		Name:    "lazy",
		Default: true,
		Open:    func(datastore.Params) (datastore.Backend, error) { return b, nil },
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestColdStartLoadsMetadataOnly: a navi starting over a datastore full of
// persisted history performs no transcript-body reads, nothing is resident,
// and the bulk metadata enumeration answers in one pass without touching a
// conversation (ADR 0011 decision 5).
func TestColdStartLoadsMetadataOnly(t *testing.T) {
	b := inmem.New()
	seedTranscript(t, b, "t-cold-1", 30)
	seedTranscript(t, b, "t-cold-2", 40)
	seedTranscript(t, b, "t-cold-3", 50)

	n := lazyTestNavi(t, b)
	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	reporter := readReporter(b)
	reporter.ResetReads() // drop setup reads; count only what the navi does

	if got := n.Datastores.Handle().ResidentTranscripts(); got != 0 {
		t.Fatalf("after cold start %d transcript body/bodies are resident, want 0: cold start must load metadata only", got)
	}

	// The bulk enumeration answers for every transcript, still without a
	// body read.
	infos, err := n.Datastores.Handle().Thoughts().TranscriptInfos()
	if err != nil {
		t.Fatalf("TranscriptInfos: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("TranscriptInfos returned %d entries, want 3", len(infos))
	}
	counts := map[string]int{}
	for _, info := range infos {
		counts[info.ThoughtID] = info.MessageCount
	}
	for id, want := range map[string]int{"t-cold-1": 30, "t-cold-2": 40, "t-cold-3": 50} {
		if counts[id] != want {
			t.Errorf("%s: MessageCount = %d, want %d", id, counts[id], want)
		}
	}
	if n := reporter.BodyReads(); n != 0 {
		t.Errorf("metadata reads loaded %d transcript body/bodies, want 0", n)
	}
	if got := n.Datastores.Handle().ResidentTranscripts(); got != 0 {
		t.Errorf("after enumeration %d bodies resident, want 0", got)
	}

	// Access is what makes detail resident.
	entries, err := n.Datastores.Handle().Thoughts().LoadTranscript("t-cold-2")
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if len(entries) != 40 {
		t.Fatalf("hydrated %d entries, want 40", len(entries))
	}
	if got := reporter.BodyReads(); got != 1 {
		t.Errorf("body reads after access = %d, want 1", got)
	}
	if got := n.Datastores.Handle().ResidentTranscripts(); got != 1 {
		t.Errorf("resident bodies after access = %d, want 1", got)
	}
}

// TestUnaccessedDetailIsCollectable: a hydrated body survives while a reader
// holds it and is reclaimed once nothing is touching it — the next read
// re-hydrates from the datastore rather than being pinned (ADR 0011
// decision 5).
func TestUnaccessedDetailIsCollectable(t *testing.T) {
	b := inmem.New()
	seedTranscript(t, b, "t-collect", 25)

	n := lazyTestNavi(t, b)
	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	reporter := readReporter(b)
	reporter.ResetReads()

	view := n.Datastores.Handle().Thoughts()
	entries, err := view.LoadTranscript("t-collect")
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if len(entries) != 25 {
		t.Fatalf("hydrated %d entries, want 25", len(entries))
	}
	entries = nil // the reader lets go

	// Two GC rounds: one may queue the weak reference's clearing, the
	// next observes it. The body has no strong references left, so it
	// becomes collectable rather than pinned for the process's life.
	runtime.GC()
	runtime.GC()
	if got := n.Datastores.Handle().ResidentTranscripts(); got != 0 {
		t.Fatalf("%d transcript body/bodies still resident after the reader let go and GC ran, want 0", got)
	}

	// The next read re-hydrates: detail is reloadable, not lost.
	again, err := view.LoadTranscript("t-collect")
	if err != nil {
		t.Fatalf("LoadTranscript after collection: %v", err)
	}
	if len(again) != 25 {
		t.Fatalf("re-hydrated %d entries, want 25", len(again))
	}
	if got := reporter.BodyReads(); got != 2 {
		t.Errorf("body reads = %d, want 2 (the collected body must be re-loaded from the datastore)", got)
	}
}

// TestLazyWriteMaterializesAndFlushes: a write to lazily-loaded detail
// lands in RAM immediately — readers see it without a backend round-trip,
// even before the ordered writer drains — and the write persists through
// the writer (ADR 0011 decisions 1, 6, 7).
func TestLazyWriteMaterializesAndFlushes(t *testing.T) {
	b := inmem.New()
	seedTranscript(t, b, "t-lazywrite", 2)

	n := lazyTestNavi(t, b)
	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	reporter := readReporter(b)
	reporter.ResetReads()

	handle := n.Datastores.Handle()
	view := handle.Thoughts()

	// Hydrate first: the write then targets lazily-loaded detail.
	if _, err := view.LoadTranscript("t-lazywrite"); err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if got := reporter.BodyReads(); got != 1 {
		t.Fatalf("body reads after hydration = %d, want 1", got)
	}

	// SubmitTranscript goes through the navi's own path (the runner's).
	next := []datastore.Entry{
		{ID: "nw-1", Time: time.Now().UTC(), Role: datastore.RoleUser, Message: "fresh"},
		{ID: "nw-2", Time: time.Now().UTC(), Role: datastore.RoleAssistant, Message: "reply"},
	}
	handle.SubmitTranscript("t-lazywrite", next)

	// Materialized: the read sees the new state with NO backend read.
	got, err := view.LoadTranscript("t-lazywrite")
	if err != nil {
		t.Fatalf("LoadTranscript after write: %v", err)
	}
	if len(got) != 2 || got[0].Message != "fresh" {
		t.Fatalf("read after write = %d entries (first %q), want the materialized write", len(got), got[0].Message)
	}
	if reads := reporter.BodyReads(); reads != 1 {
		t.Errorf("body reads after write = %d, want 1 (the read must be served from RAM)", reads)
	}
	if resident := handle.ResidentTranscripts(); resident != 1 {
		t.Errorf("resident bodies while the write is staged = %d, want 1 (the dirty body must be held)", resident)
	}

	// Metadata is updated without a body read.
	info, err := view.TranscriptInfo("t-lazywrite")
	if err != nil {
		t.Fatalf("TranscriptInfo: %v", err)
	}
	if info.MessageCount != 2 {
		t.Errorf("TranscriptInfo.MessageCount = %d, want 2", info.MessageCount)
	}

	// The ordered writer persists it.
	if err := handle.FlushWrites(); err != nil {
		t.Fatalf("FlushWrites: %v", err)
	}
	persisted, err := b.Thoughts().LoadTranscript("t-lazywrite")
	if err != nil {
		t.Fatalf("LoadTranscript from the backend: %v", err)
	}
	if len(persisted) != 2 || persisted[0].Message != "fresh" {
		t.Fatalf("backend has %d entries (first %q), want the flushed write", len(persisted), persisted[0].Message)
	}

	// Once flushed the strong hold drops: the body is collectable again.
	got = nil
	runtime.GC()
	runtime.GC()
	if resident := handle.ResidentTranscripts(); resident != 0 {
		t.Errorf("resident bodies after flush and GC = %d, want 0", resident)
	}
}

// TestRAMNotGrownByNeverAccessedTranscripts: enumeration over a large
// history — the cold-start path — leaves nothing in RAM; growth is bounded
// by access, not by retention.
func TestRAMNotGrownByNeverAccessedTranscripts(t *testing.T) {
	b := inmem.New()
	const count = 60
	for i := 0; i < count; i++ {
		seedTranscript(t, b, fmt.Sprintf("t-many-%02d", i), 5)
	}

	n := lazyTestNavi(t, b)
	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	reporter := readReporter(b)
	reporter.ResetReads()

	infos, err := n.Datastores.Handle().Thoughts().TranscriptInfos()
	if err != nil {
		t.Fatalf("TranscriptInfos: %v", err)
	}
	if len(infos) != count {
		t.Fatalf("TranscriptInfos returned %d entries, want %d", len(infos), count)
	}
	if reads := reporter.BodyReads(); reads != 0 {
		t.Errorf("enumeration loaded %d transcript body/bodies, want 0", reads)
	}
	if resident := n.Datastores.Handle().ResidentTranscripts(); resident != 0 {
		t.Errorf("%d bodies resident after enumerating %d transcripts, want 0: never-accessed detail must cost no RAM", resident, count)
	}
}
