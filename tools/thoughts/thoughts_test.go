package thoughts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

// route builds a queue wired to the in-memory store for tool tests.
func routeQueue(t *testing.T) *thought.Queue {
	t.Helper()
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 3, SlotExhaustion: thought.PolicyQueue}, thought.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// toolRegistry registers all four tools bound to the given queue with
// the full active set.
func toolRegistry(t *testing.T, q *thought.Queue) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := Register(reg, q); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.SetActive([]string{"thoughts_read", "thoughts_create", "thoughts_update", "thoughts_delete"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	return reg
}

func dispatch(t *testing.T, reg *registry.Registry, name, args string) (string, error) {
	t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	return reg.Dispatch(context.Background(), name, raw)
}

func decodeList(t *testing.T, out string) []map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	return items
}

func field(t *testing.T, m map[string]any, key string) any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("output missing %q: %v", key, m)
	}
	return v
}

// ── definitions ──────────────────────────────────────────────────────

func TestDefinitionsComplete(t *testing.T) {
	want := map[string]struct {
		access registry.ToolAccess
		mode   registry.ToolMode
		format string
	}{
		"thoughts_read":   {registry.AccessReadOnly, registry.ToolModeParallel, "json"},
		"thoughts_create": {registry.AccessWrite, registry.ToolModeSequential, "json"},
		"thoughts_update": {registry.AccessWrite, registry.ToolModeSequential, "json"},
		"thoughts_delete": {registry.AccessWrite, registry.ToolModeSequential, "json"},
	}
	defs := Definitions()
	if len(defs) != 4 {
		t.Fatalf("Definitions() = %d defs, want 4", len(defs))
	}
	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Name] = true
		w := want[def.Name]
		if def.Access != w.access || def.Mode != w.mode || def.OutputFormat != w.format {
			t.Errorf("%s metadata mismatch: access=%v mode=%v format=%q", def.Name, def.Access, def.Mode, def.OutputFormat)
		}
		if def.Timeout <= 0 || def.Recovery == "" || def.PromptSnippet == "" {
			t.Errorf("%s incomplete metadata", def.Name)
		}
		if def.SourceInfo.Origin != registry.OriginSDK {
			t.Errorf("%s origin = %v, want sdk", def.Name, def.SourceInfo.Origin)
		}
		if def.Namespace() != "thoughts" {
			t.Errorf("%s namespace = %q", def.Name, def.Namespace())
		}
		if _, err := registry.ParseSchema(def.InputSchema.Raw()); err != nil {
			t.Errorf("%s schema invalid: %v", def.Name, err)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("Definitions() missing %s", name)
		}
	}
}

// ── thoughts_read ────────────────────────────────────────────

func TestThoughtsReadSnapshotAndFilter(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	// Mixed states and priorities: pz (prio 1) is claimed and paused;
	// p2 (prio 2) runs; p3/p1 stay pending.
	_ = q.Enqueue(&thought.Thought{ID: "p1", Prompt: "low", Priority: 6, Status: thought.ThoughtPending})
	_ = q.Enqueue(&thought.Thought{ID: "p3", Prompt: "mid", Priority: 4, Status: thought.ThoughtPending})
	_ = q.Enqueue(&thought.Thought{ID: "p2", Prompt: "high", Priority: 2, Status: thought.ThoughtPending, Metadata: map[string]string{"origin": "tool"}})
	if err := q.Enqueue(&thought.Thought{ID: "pz", Prompt: "parked", Priority: 1, Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok { // pz claims → running
		t.Fatal("claim failed")
	}
	if err := q.PauseWithCheckpoint("pz", []byte("cp")); err != nil { // pz → paused
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok { // p2 claims → running
		t.Fatal("second claim failed")
	}

	// Full snapshot: ordered by priority then recency — 4 thoughts
	// across pending/running/paused.
	out, err := dispatch(t, reg, "thoughts_read", "")
	if err != nil {
		t.Fatalf("thoughts_read: %v", err)
	}
	items := decodeList(t, out)
	if len(items) != 4 {
		t.Fatalf("expected 4 thoughts, got %d: %s", len(items), out)
	}
	if items[0]["id"] != "pz" {
		t.Fatalf("ordering by priority broken: %s", out)
	}
	for _, item := range items {
		for _, key := range []string{"id", "status", "priority", "prompt", "created_at", "updated_at"} {
			if _, ok := item[key]; !ok {
				t.Fatalf("item missing %q: %s", key, out)
			}
		}
	}

	// Status filter: paused only.
	out, err = dispatch(t, reg, "thoughts_read", `{"status":"paused"}`)
	if err != nil {
		t.Fatalf("thoughts_read(filter): %v", err)
	}
	items = decodeList(t, out)
	if len(items) != 1 || items[0]["id"] != "pz" || items[0]["status"] != "paused" {
		t.Fatalf("status filter wrong: %s", out)
	}

	// Empty result encodes as [] not null.
	out, err = dispatch(t, reg, "thoughts_read", `{"status":"cancelled"}`)
	if err != nil {
		t.Fatalf("thoughts_read(empty): %v", err)
	}
	if !strings.Contains(out, "[]") {
		t.Fatalf("empty filter should encode as [], got %s", out)
	}
}

// (explicit enqueues above; helper removed)

// ── thoughts_create ──────────────────────────────────────────────

func TestThoughtsCreateFollowUpWithOriginTag(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)

	out, err := dispatch(t, reg, "thoughts_create", `{"prompt":"write follow-up report","context":"from thought abc","priority":2}`)
	if err != nil {
		t.Fatalf("thoughts_create: %v", err)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id := created["id"].(string)
	if id == "" || !strings.HasPrefix(id, "self-") {
		t.Fatalf("created id = %v, want self- prefix", created["id"])
	}

	// origin tag + priority clamp at Enqueue.
	got, _ := q.GetThought(id)
	if got.Metadata["origin"] != thought.OriginTool {
		t.Fatalf("origin metadata = %v, want tool", got.Metadata)
	}
	if got.Priority != thought.SelfScheduledHighBound {
		t.Fatalf("priority = %d, want clamped to %d (self-scheduled band)", got.Priority, thought.SelfScheduledHighBound)
	}
	// Durable: persisted through the queue's store.
	if got.Status != thought.ThoughtPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}

	// Caller metadata merges around the tag but MUST NOT override it
	// (the safety rail's origin tag is not overridable).
	out2, err := dispatch(t, reg, "thoughts_create", `{"prompt":"second","metadata":{"origin":"self","topic":"reports"}}`)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	var second map[string]any
	_ = json.Unmarshal([]byte(out2), &second)
	got2, _ := q.GetThought(second["id"].(string))
	if got2.Metadata["origin"] != thought.OriginTool {
		t.Fatalf("origin overridable via metadata: %v", got2.Metadata)
	}
	if got2.Metadata["topic"] != "reports" {
		t.Fatalf("caller metadata not merged: %v", got2.Metadata)
	}
}

func TestThoughtsCreateUnclampedWhenInBand(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	out, err := dispatch(t, reg, "thoughts_create", `{"prompt":"normal work","priority":7}`)
	if err != nil {
		t.Fatalf("thoughts_create: %v", err)
	}
	var created map[string]any
	_ = json.Unmarshal([]byte(out), &created)
	got, _ := q.GetThought(created["id"].(string))
	if got.Priority != 7 {
		t.Fatalf("in-band priority = %d, want 7 (no clamp)", got.Priority)
	}
}

func TestThoughtsCreateDeferUntilDurable(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	deferUntil := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	out, err := dispatch(t, reg, "thoughts_create", fmt.Sprintf(`{"prompt":"deferred work","defer_until":%q}`, deferUntil))
	if err != nil {
		t.Fatalf("thoughts_create(deferred): %v", err)
	}
	var created map[string]any
	_ = json.Unmarshal([]byte(out), &created)
	got, _ := q.GetThought(created["id"].(string))
	if got.DeferUntil.IsZero() {
		t.Fatal("defer_until not persisted on the thought")
	}
	if !got.DeferUntil.UTC().Equal(timeMustParse(deferUntil)) {
		t.Fatalf("defer_until = %v, want %v", got.DeferUntil, deferUntil)
	}

	// The deferred thought is pending but NOT claimable until due —
	// it is the only queued work, so Dequeue finds nothing claimable.
	if claimed, ok := q.Dequeue(); ok {
		t.Fatalf("deferred thought was claimable before defer_until: %+v", claimed)
	}
}

func timeMustParse(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// ── thoughts_update ──────────────────────────────────────────

func TestThoughtsUpdatePriorityClamped(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	out, _ := dispatch(t, reg, "thoughts_create", `{"prompt":"self work","priority":4}`)
	var created map[string]any
	_ = json.Unmarshal([]byte(out), &created)
	id := created["id"].(string)

	// Update raises priority — clamped to the self-scheduled band.
	if _, err := dispatch(t, reg, "thoughts_update", fmt.Sprintf(`{"id":%q,"priority":1}`, id)); err != nil {
		t.Fatalf("thoughts_update(priority): %v", err)
	}
	got, _ := q.GetThought(id)
	if got.Priority != thought.SelfScheduledHighBound {
		t.Fatalf("updated priority = %d, want clamped %d", got.Priority, thought.SelfScheduledHighBound)
	}
}

func TestThoughtsUpdatePauseResumeCancel(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	// Enqueue + claim to get a running thought.
	if err := q.Enqueue(&thought.Thought{ID: "r1", Prompt: "long", Priority: 4, Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}

	// Pause: park-with-checkpoint.
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"r1","status":"pause"}`); err != nil {
		t.Fatalf("pause: %v", err)
	}
	got, _ := q.GetThought("r1")
	if got.Status != thought.ThoughtPaused {
		t.Fatalf("status after pause = %s, want paused", got.Status)
	}

	// Resume: paused → pending.
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"r1","status":"resume"}`); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, _ = q.GetThought("r1")
	if got.Status != thought.ThoughtPending {
		t.Fatalf("status after resume = %s, want pending", got.Status)
	}

	// Cancel: non-terminal.
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"r1","status":"cancel"}`); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, _ = q.GetThought("r1")
	if got.Status != thought.ThoughtCancelled {
		t.Fatalf("status after cancel = %s, want cancelled", got.Status)
	}

	// Running thoughts cannot cancel in place (must pause first).
	if err := q.Enqueue(&thought.Thought{ID: "r2", Prompt: "x", Priority: 4, Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"r2","status":"cancel"}`); err == nil {
		t.Fatal("cancel of a running thought should be rejected (pause first)")
	}
}

func TestThoughtsUpdateContextAppend(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	if err := q.Enqueue(&thought.Thought{ID: "c1", Prompt: "work", Priority: 4, Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"c1","context_append":"new constraint"}`); err != nil {
		t.Fatalf("context append: %v", err)
	}
	got, _ := q.GetThought("c1")
	if !strings.Contains(got.Context, "new constraint") {
		t.Fatalf("context not appended: %q", got.Context)
	}
	// No-op update rejected (at least one field required).
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"c1"}`); err == nil {
		t.Fatal("no-op update should be rejected")
	}
}

// ── thoughts_delete ──────────────────────────────────────────

func TestThoughtsDeleteDeferredAndPending(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	out, _ := dispatch(t, reg, "thoughts_create", `{"prompt":"doomed work"}`)
	var created map[string]any
	_ = json.Unmarshal([]byte(out), &created)
	id := created["id"].(string)

	if _, err := dispatch(t, reg, "thoughts_delete", fmt.Sprintf(`{"id":%q}`, id)); err != nil {
		t.Fatalf("thoughts_delete: %v", err)
	}
	if _, err := q.GetThought(id); !errors.Is(err, thought.ErrThoughtNotFound) {
		t.Fatalf("thought still present after delete: %v", err)
	}
}

func TestThoughtsDeleteRunningProtected(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	if err := q.Enqueue(&thought.Thought{ID: "run-1", Prompt: "in flight", Priority: 4, Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if _, err := dispatch(t, reg, "thoughts_delete", `{"id":"run-1"}`); err == nil {
		t.Fatal("running thought deletion should be rejected")
	}
	got, _ := q.GetThought("run-1")
	if got.Status != thought.ThoughtRunning {
		t.Fatalf("running thought disturbed by delete: %s", got.Status)
	}
}

// ── validation + registration ────────────────────────────────────────

func TestSchemaValidation(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	cases := []struct {
		tool string
		args string
	}{
		{"thoughts_read", `{"status":"archived"}`},
		{"thoughts_read", `{"limit":0}`},
		{"thoughts_create", `{}`},
		{"thoughts_create", `{"prompt":""}`},
		{"thoughts_create", `{"prompt":"x","defer_until":"not-a-time"}`},
		{"thoughts_update", `{}`},
		{"thoughts_update", `{"id":"x","status":"explode"}`},
		{"thoughts_update", `{"id":"x","remove":true}`},
		{"thoughts_delete", `{}`},
	}
	for _, tc := range cases {
		_, err := dispatch(t, reg, tc.tool, tc.args)
		if err == nil {
			t.Errorf("%s %s: expected rejection", tc.tool, tc.args)
		}
	}
}

func TestRegisterValidation(t *testing.T) {
	reg := registry.New()
	if err := Register(nil, routeQueue(t)); err == nil {
		t.Fatal("nil registry should fail")
	}
	if err := Register(registry.New(), nil); err == nil {
		t.Fatal("nil queue should fail")
	}
	// Duplicate registration rejected by the registry.
	r2 := registry.New()
	if err := Register(reg, routeQueue(t)); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := Register(reg, routeQueue(t)); !errors.Is(err, registry.ErrDuplicateTool) {
		t.Fatalf("duplicate err = %v, want ErrDuplicateTool", err)
	}
	_ = r2
}

// TestBridgeFriendlyDefinitions: tools surface through the standard
// registry surface — active + permitted only (verified here by the
// active-set gate; bridge binding is NAVI-272's path).
func TestToolsInactiveUntilActive(t *testing.T) {
	q := routeQueue(t)
	reg := registry.New()
	if err := Register(reg, q); err != nil {
		t.Fatal(err)
	}
	// Not active → dispatch refused even though registered.
	if _, err := dispatch(t, reg, "thoughts_read", ""); err == nil {
		t.Fatal("inactive tool must not dispatch")
	}
	if err := reg.SetActive([]string{"thoughts_read"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch(t, reg, "thoughts_read", ""); err != nil {
		t.Fatalf("active read dispatch: %v", err)
	}
	// Writers still inactive.
	if _, err := dispatch(t, reg, "thoughts_create", `{"prompt":"x"}`); err == nil {
		t.Fatal("inactive create must not dispatch")
	}
}
