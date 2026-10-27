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

// newLongTerm builds the archive tier over the same in-memory backend.
func newLongTerm(t *testing.T, backend *inmem.Backend) *LongTerm {
	t.Helper()
	lt, err := NewLongTerm(backend.Memory(), principal.MustParse("navi:navi"))
	if err != nil {
		t.Fatal(err)
	}
	return lt
}

// toolNamed finds a tool by name in a Tools() result.
func toolNamed(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Def.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found in %v", name, toolNames(tools))
	return tool.Tool{}
}

func toolNames(tools []tool.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tl := range tools {
		out = append(out, tl.Def.Name)
	}
	return out
}

func TestSTMToolSurface(t *testing.T) {
	svc, backend := newService(t)
	tools, err := Tools(svc, newLongTerm(t, backend))
	if err != nil {
		t.Fatal(err)
	}
	// The surface is split by TIER, not by operation: the tier decides whether
	// a write costs context on every future thought, so it is in the tool name
	// rather than a scope argument a caller can get wrong.
	for _, want := range []string{"stm_write", "ltm_write", "ltm_search", "ltm_read", "ltm_prune"} {
		toolNamed(t, tools, want)
	}
	if got := toolNames(tools); len(got) != 5 {
		t.Fatalf("tool surface = %v, want exactly the five tier tools", got)
	}
	// memory_write is gone, not aliased: one name per thing.
	for _, tl := range tools {
		if tl.Def.Name == "memory_write" {
			t.Fatal("memory_write still exists; the tier split replaces it")
		}
	}
	// There is no stm_read: short-term memory is always in context, so there
	// is nothing to search for.
	for _, tl := range tools {
		if tl.Def.Name == "stm_read" || tl.Def.Name == "stm_search" {
			t.Fatalf("%s exists, but short-term memory is always injected and needs no read tool", tl.Def.Name)
		}
	}
}

// TestToolsReturnsOnlyDeclaredTiers: a navi with no archive gets no ltm_*
// surface rather than a tool that fails at call time.
func TestToolsReturnsOnlyDeclaredTiers(t *testing.T) {
	svc, _ := newService(t)
	stmOnly, err := Tools(svc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(stmOnly); len(got) != 1 || got[0] != "stm_write" {
		t.Fatalf("short-term-only surface = %v, want [stm_write]", got)
	}
	ltOnly, err := Tools(nil, newLongTerm(t, inmem.New()))
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range ltOnly {
		if tl.Def.Name == "stm_write" {
			t.Fatal("a long-term-only navi must not get stm_write")
		}
	}
}

func TestSTMToolSurfaceWrite(t *testing.T) {
	svc, backend := newService(t)
	tools, err := Tools(svc, newLongTerm(t, backend))
	if err != nil {
		t.Fatal(err)
	}
	write := toolNamed(t, tools, "stm_write")

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
	svc, backend := newService(t)
	tools, err := Tools(svc, newLongTerm(t, backend))
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		def := tl.Def
		if def.Access == tool.AccessAny || def.Mode == tool.ModeAny || def.SourceInfo.Origin == tool.OriginAny {
			t.Fatalf("%s missing required metadata: %+v", def.Name, def)
		}
		if len(def.InputSchema.Raw()) == 0 {
			t.Fatalf("%s missing an input schema", def.Name)
		}
		if def.PromptSnippet == "" {
			t.Fatalf("%s missing a prompt snippet", def.Name)
		}
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
