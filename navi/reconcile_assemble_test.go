package navi

import (
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/thought"
)

// TestExternallyEnqueuedThoughtGetsAssembledContext is the end-to-end guard for
// the adoption path: a thought written to the store by an EXTERNAL process —
// `navi enqueue -db`, the CLI mailbox — must run with assembled context, and
// therefore its transcript must begin with a system entry.
//
// Found on navi-dock. Two thoughts enqueued via the CLI completed with
// `assembled` empty and a two-entry transcript (user, assistant), where a real
// run has the system entry (personality + skills + memory) first. The cause was
// the queue's reconcile loop adopting externally-persisted rows without running
// the creation-time assembler, which the in-process Enqueue path does run
// (ADR 0013 decision 2). The runner takes the transcript's system entry from
// the thought's Assembled field, so the visible symptom was a transcript with
// no personality in it at all.
func TestExternallyEnqueuedThoughtGetsAssembledContext(t *testing.T) {
	shared := inmem.New()
	cand := datastore.Candidate{
		Name:    "shared",
		Default: true,
		Open:    func(datastore.Params) (datastore.Backend, error) { return shared, nil },
	}

	// Write the row directly, as a separate process would: no in-process
	// Enqueue, so no assembler runs for it here.
	if err := shared.Thoughts().SaveThought(&thought.Thought{
		ID:       "external",
		Prompt:   "hi from the CLI",
		Priority: 4,
		Status:   thought.ThoughtPending,
		// Assembled is deliberately empty: nobody has assembled it.
	}); err != nil {
		t.Fatalf("seed external thought: %v", err)
	}

	n := New("assemble-adopted",
		WithProvider(llm.NewMockProvider(streamEvents("ok"))),
		WithWorkers(1),
		WithPersonality("you are the test navi"),
	)
	if err := n.Datastores.Add(cand); err != nil {
		t.Fatal(err)
	}
	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	waitStatusWithin(t, n, "external", "complete", 5*time.Second)

	// The thought carries the assembled context...
	got, err := n.Thoughts.GetThought("external")
	if err != nil {
		t.Fatal(err)
	}
	if got.Assembled == "" {
		t.Fatal("adopted thought ran with no assembled context")
	}
	if want := "you are the test navi"; !strings.Contains(got.Assembled, want) {
		t.Fatalf("assembled context is missing the personality: %q", got.Assembled)
	}

	// ...and the transcript begins with it, which is what a reader sees.
	entries, err := n.Datastores.Handle().Thoughts().LoadTranscript("external")
	if err != nil {
		t.Fatalf("load transcript: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no transcript persisted")
	}
	if entries[0].Role != datastore.RoleSystem {
		t.Fatalf("transcript does not begin with a system entry: role=%q body=%q",
			entries[0].Role, truncateForTest(entries[0].Message))
	}
	if !strings.Contains(entries[0].Message, "you are the test navi") {
		t.Fatalf("system entry is not the assembled context: %q", truncateForTest(entries[0].Message))
	}
}

func truncateForTest(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
