package navi

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// toolStateNavi exposes the todo/note types bound to an in-memory stateful
// view and returns the surface plus that view.
func toolStateNavi(t *testing.T) (*Navi, *objectSurface, datastore.ToolState) {
	t.Helper()
	n := New("actions-toolstate", WithPlanCloseCheck(false))
	n.Datastores.Handle().Bind(inmem.New())
	state := n.Datastores.Handle().Stateful()
	for _, reg := range ToolStateTypes("navi-a", state) {
		if err := n.Types.Add(reg); err != nil {
			t.Fatalf("Add(%s): %v", reg.Type.Name, err)
		}
	}
	return n, newObjectSurface(n.Types, portal.AllowAll(), principal.MustParse("service:test")), state
}

func TestActThoughtLifecycleActions(t *testing.T) {
	n, s := objectNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())

	// Steer a queued thought: the message lands in its context.
	if err := s.Act(TypeThought, "t1", "steer", map[string]any{"message": "new direction"}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if got, _ := n.Thoughts.GetThought("t1"); !strings.Contains(got.Context, "new direction") {
		t.Fatalf("context after steer = %q, want the routed message", got.Context)
	}

	if err := s.Act(TypeThought, "t1", "bump", map[string]any{"priority": 7}); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if got, _ := n.Thoughts.GetThought("t1"); got.Priority != 7 {
		t.Fatalf("priority after bump = %d, want 7", got.Priority)
	}

	// Pause is a running-thought action: claim the thought first.
	if _, ok := n.Thoughts.Dequeue(); !ok {
		t.Fatal("could not claim t1")
	}
	if err := s.Act(TypeThought, "t1", "pause", nil); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got := thoughtStatus(t, n, "t1"); got != thought.ThoughtPaused {
		t.Fatalf("status after pause = %q, want paused", got)
	}

	if err := s.Act(TypeThought, "t1", "resume", nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := thoughtStatus(t, n, "t1"); got == thought.ThoughtPaused {
		t.Fatalf("status after resume = %q, want not paused", got)
	}

	if err := s.Act(TypeThought, "t1", "cancel", nil); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := thoughtStatus(t, n, "t1"); got != thought.ThoughtCancelled {
		t.Fatalf("status after cancel = %q, want cancelled", got)
	}
}

func TestActThoughtRetryRequeues(t *testing.T) {
	n, s := objectNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())
	if _, ok := n.Thoughts.Dequeue(); !ok {
		t.Fatal("could not claim t1")
	}
	if err := n.Thoughts.Fail("t1", "boom"); err != nil {
		t.Fatal(err)
	}

	if err := s.Act(TypeThought, "t1", "retry", nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := thoughtStatus(t, n, "t1"); got != thought.ThoughtPending {
		t.Fatalf("status after retry = %q, want pending", got)
	}
}

func TestActValidatesDeclaredArgs(t *testing.T) {
	n, s := objectNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())

	if err := s.Act(TypeThought, "t1", "bump", nil); !errors.Is(err, object.ErrInvalidArgs) {
		t.Fatalf("bump without priority = %v, want ErrInvalidArgs", err)
	}
	if err := s.Act(TypeThought, "t1", "bump", map[string]any{"priority": 99}); !errors.Is(err, object.ErrInvalidArgs) {
		t.Fatalf("bump with priority 99 = %v, want ErrInvalidArgs", err)
	}
	if err := s.Act(TypeThought, "t1", "steer", map[string]any{}); !errors.Is(err, object.ErrInvalidArgs) {
		t.Fatalf("steer without message = %v, want ErrInvalidArgs", err)
	}
	if got, _ := n.Thoughts.GetThought("t1"); got.Priority == 99 {
		t.Fatal("an invalid action still mutated the object")
	}
}

func TestActFailsClosedWithoutAnAuthorizer(t *testing.T) {
	n := New("actions-noauth", WithPlanCloseCheck(false))
	enqueueThought(t, n, "t1", time.Now().UTC())
	s := newObjectSurface(n.Types, nil, principal.Ref{})

	if err := s.Act(TypeThought, "t1", "pause", nil); !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("Act without an authorizer = %v, want ErrUnauthorized", err)
	}
	if got := thoughtStatus(t, n, "t1"); got == thought.ThoughtPaused {
		t.Fatal("an unauthorized action still mutated the object")
	}
}

func TestActRefusalFromAuthorizerStopsTheAction(t *testing.T) {
	n := New("actions-denied", WithPlanCloseCheck(false))
	enqueueThought(t, n, "t1", time.Now().UTC())
	deny := portal.AuthorizerFunc(func(action portal.Action, who principal.Ref) error {
		if who.String() != "service:operator" {
			t.Fatalf("authorizer saw principal %q, want service:operator", who)
		}
		return portal.ErrUnauthorized
	})
	s := newObjectSurface(n.Types, deny, principal.MustParse("service:operator"))

	if err := s.Act(TypeThought, "t1", "pause", nil); !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("Act with a refusing authorizer = %v, want ErrUnauthorized", err)
	}
	if got := thoughtStatus(t, n, "t1"); got == thought.ThoughtPaused {
		t.Fatal("a refused action still mutated the object")
	}
}

func TestActUnknownTypeAndAction(t *testing.T) {
	_, s := objectNavi(t)
	if err := s.Act("ghost-type", "x", "poke", nil); !errors.Is(err, object.ErrUnknownType) {
		t.Fatalf("Act on an unknown type = %v, want ErrUnknownType", err)
	}
	if err := s.Act(TypeThought, "t1", "poke", nil); !errors.Is(err, object.ErrUnknownAction) {
		t.Fatalf("Act with an undeclared action = %v, want ErrUnknownAction", err)
	}
}

func TestActTodoActions(t *testing.T) {
	_, s, state := toolStateNavi(t)
	todos, err := state.ReplaceTodos("navi-a", []datastore.Todo{{Text: "ship it", Priority: 1}})
	if err != nil {
		t.Fatal(err)
	}
	id := todos[0].ID

	if err := s.Act(TypeTodo, id, "start", nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	got, err := s.Get(TypeTodo, id)
	if err != nil || got.Fields["status"] != string(datastore.TodoInProgress) {
		t.Fatalf("todo after start = %+v (%v), want in_progress", got.Fields, err)
	}

	if err := s.Act(TypeTodo, id, "complete", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, err = s.Get(TypeTodo, id)
	if err != nil || got.Fields["status"] != string(datastore.TodoDone) {
		t.Fatalf("todo after complete = %+v (%v), want done", got.Fields, err)
	}

	if err := s.Act(TypeTodo, id, "remove", nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := s.Get(TypeTodo, id); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("todo after remove = %v, want ErrNotFound", err)
	}

	if err := s.Act(TypeTodo, "ghost", "complete", nil); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("complete on a missing todo = %v, want ErrNotFound", err)
	}
	if err := s.Act(TypeTodo, id, "resume", nil); !errors.Is(err, object.ErrUnknownAction) {
		t.Fatalf("undeclared todo action = %v, want ErrUnknownAction", err)
	}
}

func TestActNoteRemove(t *testing.T) {
	_, s, state := toolStateNavi(t)
	note, err := state.UpsertNote("navi-a", datastore.Note{Title: "runbook", Body: "steps"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Act(TypeNote, note.ID, "remove", nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := s.Get(TypeNote, note.ID); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("note after remove = %v, want ErrNotFound", err)
	}
	if err := s.Act(TypeNote, "ghost", "remove", nil); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("remove on a missing note = %v, want ErrNotFound", err)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────

func thoughtStatus(t *testing.T, n *Navi, id string) thought.ThoughtStatus {
	t.Helper()
	got, err := n.Thoughts.GetThought(id)
	if err != nil {
		t.Fatalf("GetThought(%s): %v", id, err)
	}
	return got.Status
}
