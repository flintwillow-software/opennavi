package navi

import (
	"errors"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/thought"
)

// objectNavi builds a navi with the built-in types exposed and no datastore
// bound: the surface must serve live values from memory alone.
func objectNavi(t *testing.T) (*Navi, *objectSurface) {
	t.Helper()
	n := New("objects", WithPlanCloseCheck(false))
	return n, newObjectSurface(n.Types)
}

func enqueueThought(t *testing.T, n *Navi, id string, created time.Time) {
	t.Helper()
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: id, Prompt: "prompt " + id, Status: thought.ThoughtPending,
		Priority: 4, CreatedAt: created, UpdatedAt: created,
		// Sensitive working state: the surface must strip these.
		Assembled: "system prompt", Checkpoint: []byte("cp"), LogPath: "/tmp/x.log",
		Metadata: map[string]string{"origin": "self"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestObjectSurfaceExposesBuiltinTypes(t *testing.T) {
	_, s := objectNavi(t)
	names := map[string]bool{}
	for _, ty := range s.Types() {
		names[ty.Name] = true
	}
	for _, want := range []string{TypeThought, TypeEntry, TypeMemory} {
		if !names[want] {
			t.Fatalf("type %q is not exposed; got %v", want, names)
		}
	}
}

func TestObjectSurfaceListsThoughtsFromMemoryWithoutADatastore(t *testing.T) {
	n, s := objectNavi(t)
	base := time.Now().UTC()
	enqueueThought(t, n, "older", base.Add(-time.Minute))
	enqueueThought(t, n, "newer", base)

	objs, next, err := s.List(TypeThought, object.Query{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if next != "" {
		t.Fatalf("next cursor = %q, want empty on the last page", next)
	}
	if len(objs) != 2 || objs[0].ID != "newer" {
		t.Fatalf("List = %+v, want newest first", objs)
	}
	if objs[0].Type != TypeThought {
		t.Fatalf("object type = %q, want %q", objs[0].Type, TypeThought)
	}
	if objs[0].Fields["prompt"] != "prompt newer" || objs[0].Fields["status"] != "pending" {
		t.Fatalf("fields = %v, want prompt/status", objs[0].Fields)
	}
}

func TestObjectSurfaceOmitsSensitiveFields(t *testing.T) {
	n, s := objectNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())

	obj, err := s.Get(TypeThought, "t1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, hidden := range []string{"assembled", "checkpoint", "metadata", "log_path"} {
		if _, present := obj.Fields[hidden]; present {
			t.Fatalf("sensitive field %q was exposed: %v", hidden, obj.Fields)
		}
	}
	if obj.Fields["prompt"] != "prompt t1" {
		t.Fatalf("non-sensitive field missing: %v", obj.Fields)
	}
}

func TestObjectSurfacePagination(t *testing.T) {
	n, s := objectNavi(t)
	base := time.Now().UTC()
	for i, id := range []string{"a", "b", "c"} {
		enqueueThought(t, n, id, base.Add(time.Duration(i)*time.Second))
	}

	first, cursor, err := s.List(TypeThought, object.Query{Limit: 2})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(first) != 2 || cursor == "" {
		t.Fatalf("page 1 = %d objects, cursor %q; want 2 and a cursor", len(first), cursor)
	}
	second, next, err := s.List(TypeThought, object.Query{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(second) != 1 || next != "" {
		t.Fatalf("page 2 = %d objects, next %q; want 1 and no cursor", len(second), next)
	}
	if first[0].ID == second[0].ID {
		t.Fatal("pages overlap")
	}
}

func TestObjectSurfaceUnknownTypeAndDetail(t *testing.T) {
	n, s := objectNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())

	if _, _, err := s.List("ghost", object.Query{}); !errors.Is(err, object.ErrUnknownType) {
		t.Fatalf("List(ghost) = %v, want ErrUnknownType", err)
	}
	if _, err := s.Get(TypeThought, "ghost"); !errors.Is(err, thought.ErrThoughtNotFound) {
		t.Fatalf("Get(ghost) = %v, want ErrThoughtNotFound", err)
	}
	if _, _, err := s.Detail(TypeThought, "t1", "ghost", object.Query{}); !errors.Is(err, object.ErrUnknownDetail) {
		t.Fatalf("Detail(ghost) = %v, want ErrUnknownDetail", err)
	}
}

func TestObjectSurfaceTranscriptDetail(t *testing.T) {
	n, s := objectNavi(t)
	b := inmem.New()
	n.Datastores.Handle().Bind(b)
	n.transcripts = n.Datastores.Handle().Thoughts()

	entries := []datastore.Entry{
		datastore.NewEntry(datastore.RoleUser, "hi"),
		datastore.NewEntry(datastore.RoleAssistant, "hello"),
	}
	if err := n.transcripts.SaveTranscript("t1", entries); err != nil {
		t.Fatal(err)
	}

	objs, next, err := s.Detail(TypeThought, "t1", DetailTranscript, object.Query{})
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if next != "" || len(objs) != 2 {
		t.Fatalf("Detail = %d entries, next %q; want 2 and no cursor", len(objs), next)
	}
	if objs[0].Type != TypeEntry || objs[0].Fields["message"] != "hi" || objs[0].Fields["thought_id"] != "t1" {
		t.Fatalf("entry = %+v, want the user turn linked to t1", objs[0])
	}
}

func TestObjectSurfaceDetailWithoutADatastoreIsEmpty(t *testing.T) {
	n, s := objectNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())

	objs, next, err := s.Detail(TypeThought, "t1", DetailTranscript, object.Query{})
	if err != nil {
		t.Fatalf("Detail without a datastore: %v", err)
	}
	if len(objs) != 0 || next != "" {
		t.Fatalf("Detail = %d entries, want none without a datastore", len(objs))
	}
}

func TestObjectSurfaceActionsAreDeclaredButUnsupported(t *testing.T) {
	n, s := objectNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())

	if err := s.Act(TypeThought, "t1", "ghost", nil); !errors.Is(err, object.ErrUnknownAction) {
		t.Fatalf("Act(ghost) = %v, want ErrUnknownAction", err)
	}
	if err := s.Act(TypeThought, "t1", "pause", nil); !errors.Is(err, object.ErrUnsupported) {
		t.Fatalf("Act(pause) = %v, want ErrUnsupported until NAVI-387", err)
	}
}

func TestTypeRegistrationValidation(t *testing.T) {
	n := New("objects-validation", WithPlanCloseCheck(false))

	bad := TypeRegistration{Type: object.Type{Name: "broken", ID: "id"}} // no fields, no provider
	if err := n.Types.Add(bad); err == nil {
		t.Fatal("Add accepted a type with no provider")
	}
	if err := n.Types.Add(TypeRegistration{Type: object.Type{Name: "noid"}}); err == nil {
		t.Fatal("Add accepted a type with no identity field")
	}
	if err := n.Types.Add(TypeRegistration{Type: thoughtType(), Provider: entryProvider{}}); err == nil {
		t.Fatal("Add accepted a duplicate built-in type name")
	}
}
