package inmem

import (
	"errors"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
)

// The in-memory backend is the reference implementation the datastore
// contract is tested against. Its aggregate round-trips now live in the
// shared conformance suite (conformance_test.go), which every backend runs
// (NAVI-376); what stays here is what is specific to the in-memory backend
// and to the handle's binding. Nothing here imports navi or internal.

func TestCandidateAndViews(t *testing.T) {
	c := Candidate()
	// Not the default: `fs` is (ADR 0011). This backend is the non-durable
	// reference, selected explicitly or when it is the only candidate.
	if c.Name != "inmem" || c.Default || c.Open == nil {
		t.Fatalf("candidate = %+v", c)
	}
	backend, err := c.Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	var _ datastore.Backend = backend
	if backend.Thoughts() == nil || backend.ToolState() == nil || backend.Memory() == nil {
		t.Fatal("every aggregate must expose a view")
	}
}

func TestHandleBindsAndClaims(t *testing.T) {
	h := datastore.NewDatastore()
	if h.Bound() || h.Claimed() {
		t.Fatal("a fresh handle is unbound and unclaimed")
	}
	if _, err := h.Thoughts().LoadTranscript("t1"); !errors.Is(err, datastore.ErrUnbound) {
		t.Fatalf("an unbound view must fail with ErrUnbound, got %v", err)
	}
	if !h.Claimed() {
		t.Fatal("requesting a view claims the handle")
	}
	h.Bind(New())
	if !h.Bound() {
		t.Fatal("Bind must mark the handle bound")
	}
	if _, err := h.Thoughts().LoadTranscript("t1"); !errors.Is(err, datastore.ErrTranscriptNotFound) {
		t.Fatalf("a bound view must serve the backend, got %v", err)
	}
}

// TestConversationReadableThroughTheContractAlone is the acceptance test:
// a completed thought's conversation is read back using only the public
// datastore types — no navi, no internal package.
func TestConversationReadableThroughTheContractAlone(t *testing.T) {
	var backend datastore.Backend = New() // as a harness would receive it

	conversation := []datastore.Entry{
		datastore.NewEntry(datastore.RoleUser, "what is 2+2?"),
		datastore.NewEntry(datastore.RoleAssistant, "4"),
	}
	if err := backend.Thoughts().SaveTranscript("thought-1", conversation); err != nil {
		t.Fatal(err)
	}

	read, err := backend.Thoughts().LoadTranscript("thought-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != len(conversation) {
		t.Fatalf("read %d entries, want %d", len(read), len(conversation))
	}
	for i := range conversation {
		if read[i].Role != conversation[i].Role || read[i].Message != conversation[i].Message {
			t.Fatalf("entry %d = %+v, want %+v", i, read[i], conversation[i])
		}
	}
}
