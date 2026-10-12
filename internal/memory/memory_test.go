package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/tool"
)

// newService builds a service over a fresh in-memory backend whose writes are
// applied inline, so tests can inspect the sink without a writer.
func newService(t *testing.T) (*Service, *inmem.Backend) {
	t.Helper()
	backend := inmem.New()
	svc, err := New(Config{
		Memory:  backend.Memory(),
		Submit:  func(w datastore.Write) { _ = w.Apply(backend) },
		Subject: principal.MustParse("navi:navi"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, backend
}

func TestInjectIsEmptyWhenNothingKept(t *testing.T) {
	svc, _ := newService(t)
	if got := svc.Inject(); got != "" {
		t.Fatalf("empty memory must inject nothing, got %q", got)
	}
}

func TestHydrateAtConstruction(t *testing.T) {
	backend := inmem.New()
	if _, err := backend.Memory().Remember(datastore.MemoryRecord{
		Scope:   datastore.ScopeUser,
		Subject: principal.MustParse("navi:navi"),
		Content: "prefers terse answers",
	}); err != nil {
		t.Fatal(err)
	}

	svc, err := New(Config{
		Memory:  backend.Memory(),
		Submit:  func(datastore.Write) {},
		Subject: principal.MustParse("navi:navi"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if block := svc.Inject(); !strings.Contains(block, "prefers terse answers") {
		t.Fatalf("hydrated note missing from injection: %q", block)
	}
}

func TestWriteIsVisibleImmediatelyAndPersisted(t *testing.T) {
	svc, backend := newService(t)

	rec, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "step 1 done"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == "" {
		t.Fatal("write must assign an id")
	}
	if block := svc.Inject(); !strings.Contains(block, "step 1 done") {
		t.Fatalf("write not visible in injection: %q", block)
	}

	persisted, err := backend.Memory().Recall(datastore.RecallQuery{Scope: datastore.ScopeSession, Subject: principal.MustParse("navi:navi")})
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].Content != "step 1 done" {
		t.Fatalf("write not staged to the sink: %+v", persisted)
	}
}

func TestWriteUpdatesExistingEntry(t *testing.T) {
	svc, _ := newService(t)
	rec, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Write(datastore.MemoryRecord{ID: rec.ID, Scope: datastore.ScopeSession, Content: "final"}); err != nil {
		t.Fatal(err)
	}

	got := svc.Records()
	if len(got) != 1 || got[0].Content != "final" {
		t.Fatalf("update produced %+v, want one entry reading final", got)
	}
	if !got[0].CreatedAt.Equal(rec.CreatedAt) {
		t.Fatalf("update must preserve CreatedAt: %v -> %v", rec.CreatedAt, got[0].CreatedAt)
	}
}

func TestRemove(t *testing.T) {
	svc, _ := newService(t)
	rec, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "temporary"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(rec.ID); err != nil {
		t.Fatal(err)
	}
	if block := svc.Inject(); block != "" {
		t.Fatalf("removed note still injected: %q", block)
	}
	if err := svc.Remove(rec.ID); !errors.Is(err, datastore.ErrMemoryNotFound) {
		t.Fatalf("removing again = %v, want ErrMemoryNotFound", err)
	}
}

func TestWriteRejectsNonShortTermScope(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "archive"}); err == nil {
		t.Fatal("archived memory is long-term, not short-term; Write must reject it")
	}
}

func TestInjectSeparatesScopes(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeUser, Content: "about the human"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "about the task"}); err != nil {
		t.Fatal(err)
	}
	block := svc.Inject()
	userAt := strings.Index(block, "### user")
	sessionAt := strings.Index(block, "### session")
	if userAt < 0 || sessionAt < 0 {
		t.Fatalf("both scopes must be rendered: %q", block)
	}
	if !strings.Contains(block[userAt:sessionAt], "about the human") {
		t.Fatalf("user note under the wrong section: %q", block)
	}
	if !strings.Contains(block[sessionAt:], "about the task") {
		t.Fatalf("session note under the wrong section: %q", block)
	}
}

func TestMemoryWriteTool(t *testing.T) {
	svc, _ := newService(t)
	tools, err := Tools(svc)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Def.Name != "memory_write" {
		t.Fatalf("tools = %+v", tools)
	}
	write := tools[0]

	out, err := write.Handler(context.Background(), map[string]any{
		"content": "remember this",
		"scope":   "session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "remembered") {
		t.Fatalf("handler output = %q", out)
	}
	if block := svc.Inject(); !strings.Contains(block, "remember this") {
		t.Fatalf("tool write not visible: %q", block)
	}

	// The tool removes by id, and rejects a remove without one.
	records := svc.Records()
	if _, err := write.Handler(context.Background(), map[string]any{"remove": true, "id": records[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := write.Handler(context.Background(), map[string]any{"remove": true}); err == nil {
		t.Fatal("remove without an id must fail")
	}
	// And rejects a write with no content.
	if _, err := write.Handler(context.Background(), map[string]any{"content": "  "}); err == nil {
		t.Fatal("empty content must fail")
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New must require a memory view, a submitter, and a subject")
	}
	backend := inmem.New()
	if _, err := New(Config{Memory: backend.Memory(), Submit: func(datastore.Write) {}}); err == nil {
		t.Fatal("New must require a subject")
	}
}

// The tool def must satisfy the same shape validation the catalogue applies.
func TestToolDefIsValid(t *testing.T) {
	svc, _ := newService(t)
	tools, err := Tools(svc)
	if err != nil {
		t.Fatal(err)
	}
	def := tools[0].Def
	if def.Access == tool.AccessAny || def.Mode == tool.ModeAny || def.SourceInfo.Origin == tool.OriginAny {
		t.Fatalf("tool def missing required metadata: %+v", def)
	}
	if len(def.InputSchema.Raw()) == 0 {
		t.Fatal("tool def missing an input schema")
	}
}

// TestOnChangeReportsPersistedMutations pins the observer the typed-object
// surface's live path rides (ADR 0017 decision 6): every mutation is reported
// once, classified, and only AFTER it reached the backend — so a delta never
// names a record a read cannot yet see.
func TestOnChangeReportsPersistedMutations(t *testing.T) {
	backend := inmem.New()
	type observation struct {
		change  Change
		id      string
		content string
	}
	var seen []observation
	svc, err := New(Config{
		Memory:  backend.Memory(),
		Submit:  func(w datastore.Write) { _ = w.Apply(backend) },
		Subject: principal.MustParse("navi:navi"),
		OnChange: func(rec datastore.MemoryRecord, change Change) {
			// The write must already be visible to a backend read.
			persisted, err := backend.Memory().Recall(datastore.RecallQuery{})
			if err != nil {
				t.Errorf("recall in observer: %v", err)
			}
			var found bool
			for _, r := range persisted {
				if r.ID == rec.ID {
					found = true
				}
			}
			switch change {
			case ChangeCreated, ChangeUpdated:
				if !found {
					t.Errorf("%s observer fired before the record was persisted", change)
				}
			case ChangeRemoved:
				if found {
					t.Errorf("delete observer fired before the record was forgotten")
				}
			}
			seen = append(seen, observation{change: change, id: rec.ID, content: rec.Content})
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Write(datastore.MemoryRecord{ID: rec.ID, Scope: datastore.ScopeSession, Content: "second"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(rec.ID); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 3 {
		t.Fatalf("observer saw %d mutations, want 3: %+v", len(seen), seen)
	}
	if seen[0].change != ChangeCreated || seen[1].change != ChangeUpdated || seen[2].change != ChangeRemoved {
		t.Fatalf("changes = %s/%s/%s, want create/update/delete", seen[0].change, seen[1].change, seen[2].change)
	}
	if seen[1].content != "second" || seen[0].id != seen[2].id {
		t.Fatalf("observer saw %+v, want the same id across the mutation", seen)
	}
}
