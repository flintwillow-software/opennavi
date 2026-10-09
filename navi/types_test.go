package navi

import (
	"errors"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// objectNavi builds a navi with the built-in types exposed and no datastore
// bound: the surface must serve live values from memory alone. The surface
// acts as a permissive test operator so action tests exercise dispatch.
func objectNavi(t *testing.T) (*Navi, *objectSurface) {
	t.Helper()
	n := New("objects", WithPlanCloseCheck(false))
	return n, newObjectSurface(n.Types, portal.AllowAll(), "test")
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

func TestObjectSurfaceUnknownActionIsRefused(t *testing.T) {
	n, s := objectNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())

	if err := s.Act(TypeThought, "t1", "ghost", nil); !errors.Is(err, object.ErrUnknownAction) {
		t.Fatalf("Act(ghost) = %v, want ErrUnknownAction", err)
	}
}

// TestObjectSurfaceDeclaredActionWithoutHandlerIsUnsupported pins the
// contract for a type that declares an action before its handler exists: the
// action is recognised (not unknown) but refused.
func TestObjectSurfaceDeclaredActionWithoutHandlerIsUnsupported(t *testing.T) {
	n, s := objectNavi(t)
	if err := n.Types.Add(TypeRegistration{
		Type: object.Type{
			Name: "widget", ID: "id",
			Fields:  []object.Field{{Name: "id", Kind: object.KindString}},
			Actions: []object.Action{{Name: "poke"}},
		},
		Provider: emptyObjectProvider{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Act("widget", "w1", "poke", nil); !errors.Is(err, object.ErrUnsupported) {
		t.Fatalf("Act(poke) = %v, want ErrUnsupported", err)
	}
}

// emptyObjectProvider serves nothing; it exists to register a type shape.
type emptyObjectProvider struct{}

func (emptyObjectProvider) List(object.Query) ([]object.Object, string, error) { return nil, "", nil }
func (emptyObjectProvider) Get(string) (object.Object, error) {
	return object.Object{}, object.ErrNotFound
}
func (emptyObjectProvider) Detail(string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
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

func TestToolStateTypesReadThroughTheBoundView(t *testing.T) {
	n := New("objects-toolstate", WithPlanCloseCheck(false))
	n.Datastores.Handle().Bind(inmem.New())
	state := n.Datastores.Handle().Stateful()

	for _, reg := range ToolStateTypes("navi-a", state) {
		if err := n.Types.Add(reg); err != nil {
			t.Fatalf("Add(%s): %v", reg.Type.Name, err)
		}
	}
	if _, err := state.ReplaceTodos("navi-a", []datastore.Todo{{Text: "ship it", Priority: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReplaceTodos("navi-b", []datastore.Todo{{Text: "someone else's"}}); err != nil {
		t.Fatal(err)
	}
	note, err := state.UpsertNote("navi-a", datastore.Note{Title: "runbook", Body: "steps"})
	if err != nil {
		t.Fatal(err)
	}

	s := newObjectSurface(n.Types, portal.AllowAll(), "test")
	todos, _, err := s.List(TypeTodo, object.Query{})
	if err != nil {
		t.Fatalf("List(todo): %v", err)
	}
	if len(todos) != 1 || todos[0].Fields["text"] != "ship it" {
		t.Fatalf("todo list = %+v, want only navi-a's todo", todos)
	}
	got, err := s.Get(TypeNote, note.ID)
	if err != nil || got.Fields["title"] != "runbook" {
		t.Fatalf("Get(note) = %+v (%v)", got, err)
	}
	if _, err := s.Get(TypeNote, "ghost"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("Get(ghost note) = %v, want ErrNotFound", err)
	}
}

// TestObjectSurfaceExposesDerivedVerification pins the one derived field the
// thought type adds: a plan's verification is distinguishable without reading
// the free-form metadata map it is recorded in (which stays omitted).
func TestObjectSurfaceExposesDerivedVerification(t *testing.T) {
	n, s := objectNavi(t)
	now := time.Now().UTC()
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "v1", Prompt: "verify the plan", Status: thought.ThoughtPending,
		Priority: 4, Metadata: map[string]string{thought.MetadataRole: thought.RoleVerification},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	obj, err := s.Get(TypeThought, "v1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if obj.Fields["verification"] != true {
		t.Fatalf("verification = %v, want true", obj.Fields["verification"])
	}
	if _, leaked := obj.Fields["metadata"]; leaked {
		t.Fatal("the free-form metadata map must stay omitted")
	}
}
