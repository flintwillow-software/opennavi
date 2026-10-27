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
	"github.com/flintwillow-software/opennavi/tool/registry"
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

// toolRegistry registers every thoughts tool bound to the given queue with
// the full active set.
func toolRegistry(t *testing.T, q *thought.Queue) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := Register(reg, q); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.SetActive([]string{"thoughts_read", "thoughts_search", "thoughts_create", "thoughts_update", "thoughts_delete"}); err != nil {
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
		"thoughts_search": {registry.AccessReadOnly, registry.ToolModeParallel, "json"},
		"thoughts_create": {registry.AccessWrite, registry.ToolModeSequential, "json"},
		"thoughts_update": {registry.AccessWrite, registry.ToolModeSequential, "json"},
		"thoughts_delete": {registry.AccessWrite, registry.ToolModeSequential, "json"},
	}
	defs := Definitions()
	if len(defs) != 5 {
		t.Fatalf("Definitions() = %d defs, want 5", len(defs))
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
	// Durable: persisted through the queue's datastore.
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

// TestThoughtsCreateDeclaresSkills covers the thought-declared skill path:
// a follow-up can name the skills it should load (ADR 0013 decision 5).
func TestThoughtsCreateDeclaresSkills(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)

	out, err := dispatch(t, reg, "thoughts_create", `{"prompt":"next","skills":["deploy_runbook","core_tools"]}`)
	if err != nil {
		t.Fatalf("thoughts_create: %v", err)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := q.GetThought(created["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 2 || got.Skills[0] != "deploy_runbook" || got.Skills[1] != "core_tools" {
		t.Fatalf("declared skills = %v", got.Skills)
	}

	if _, err := dispatch(t, reg, "thoughts_create", `{"prompt":"next","skills":"nope"}`); err == nil {
		t.Fatal("skills must be an array of strings")
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

// ── thoughts_search (NAVI-577) ───────────────────────────────────────

// searchQueue builds a queue with a mix of statuses, priorities, ages,
// metadata, and text, so a filter can be shown to select exactly one thing.
func searchQueue(t *testing.T) *thought.Queue {
	t.Helper()
	q := routeQueue(t)
	base := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	add := func(id, prompt, context string, prio int, created time.Time, meta map[string]string) {
		th := &thought.Thought{
			ID: id, Prompt: prompt, Context: context, Priority: prio,
			Status: thought.ThoughtPending, CreatedAt: created, UpdatedAt: created,
			Metadata: meta,
		}
		if err := q.Enqueue(th); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	add("urgent", "fix the outage", "prod is down", 0, base, map[string]string{"source": "huly"})
	add("old", "oldest planned work", "no hurry", 5, base.Add(time.Minute), nil)
	add("new", "newest planned work", "no hurry", 5, base.Add(time.Hour), nil)
	add("mid", "middle priority", "vendor question", 3, base.Add(2*time.Minute), map[string]string{"source": "github"})
	return q
}

func decodeSearch(t *testing.T, out string) (int, int, bool, []map[string]any) {
	t.Helper()
	var env struct {
		Total     int              `json:"total"`
		Returned  int              `json:"returned"`
		Truncated bool             `json:"truncated"`
		Thoughts  []map[string]any `json:"thoughts"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode search output %q: %v", out, err)
	}
	return env.Total, env.Returned, env.Truncated, env.Thoughts
}

func searchIDs(items []map[string]any) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it["id"].(string))
	}
	return ids
}

// TestThoughtsSearchFilters pins each filter selects exactly what it says.
func TestThoughtsSearchFilters(t *testing.T) {
	q := searchQueue(t)
	reg := toolRegistry(t, q)

	cases := []struct {
		name string
		args string
		want []string
	}{
		{"no filter returns everything in queue order", `{}`, []string{"urgent", "mid", "old", "new"}},
		{"priority_max asks what is more urgent", `{"priority_max":2}`, []string{"urgent"}},
		{"priority_max includes the boundary", `{"priority_max":3}`, []string{"urgent", "mid"}},
		{"priority_min is the least urgent included", `{"priority_min":3}`, []string{"mid", "old", "new"}},
		{"a priority band", `{"priority_min":3,"priority_max":5}`, []string{"mid", "old", "new"}},
		{"status", `{"status":["pending"]}`, []string{"urgent", "mid", "old", "new"}},
		{"status with no match", `{"status":["running"]}`, nil},
		{"metadata exact", `{"metadata":{"source":"huly"}}`, []string{"urgent"}},
		{"text matches the prompt", `{"text":"vendor"}`, []string{"mid"}},
		{"text matches the context", `{"text":"prod is down"}`, []string{"urgent"}},
		{"text is case-insensitive", `{"text":"OUTAGE"}`, []string{"urgent"}},
		{"created_after", `{"created_after":"2030-01-01T12:30:00Z"}`, []string{"new"}},
		{"created_before", `{"created_before":"2030-01-01T12:00:30Z"}`, []string{"urgent"}},
		{"filters combine", `{"priority_min":3,"metadata":{"source":"github"}}`, []string{"mid"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := dispatch(t, reg, "thoughts_search", tc.args)
			if err != nil {
				t.Fatalf("thoughts_search(%s): %v", tc.args, err)
			}
			_, _, _, items := decodeSearch(t, out)
			got := searchIDs(items)
			if len(got) != len(tc.want) {
				t.Fatalf("ids = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ids = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestThoughtsSearchSorts pins the three orders.
func TestThoughtsSearchSorts(t *testing.T) {
	q := searchQueue(t)
	reg := toolRegistry(t, q)

	cases := []struct {
		name string
		args string
		want []string
	}{
		{"default is the queue's own order", `{}`, []string{"urgent", "mid", "old", "new"}},
		{"age is oldest first", `{"sort":"age"}`, []string{"urgent", "old", "mid", "new"}},
		{"priority sorts by urgency then arrival", `{"sort":"priority"}`, []string{"urgent", "mid", "old", "new"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := dispatch(t, reg, "thoughts_search", tc.args)
			if err != nil {
				t.Fatalf("thoughts_search(%s): %v", tc.args, err)
			}
			_, _, _, items := decodeSearch(t, out)
			got := searchIDs(items)
			for i := range got {
				if i < len(tc.want) && got[i] != tc.want[i] {
					t.Fatalf("order = %v, want %v", got, tc.want)
				}
			}
		})
	}

	// "updated" is asserted separately: touch the oldest thought and confirm
	// it comes first.
	if err := q.BumpPriority("old", 5); err != nil {
		t.Fatal(err)
	}
	out, err := dispatch(t, reg, "thoughts_search", `{"sort":"updated"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, items := decodeSearch(t, out)
	if items[0]["id"] != "old" {
		t.Fatalf("sort=updated put %v first, want the most recently changed", searchIDs(items))
	}
}

// TestThoughtsSearchIsBounded is the acceptance criterion: the bound is part of
// the contract, and the cap is enforced even when the caller asks for more.
func TestThoughtsSearchIsBounded(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	base := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	// More thoughts than the cap.
	for i := 0; i < searchMaxLimit+25; i++ {
		th := &thought.Thought{
			ID: fmt.Sprintf("t%03d", i), Prompt: "work", Priority: 5,
			Status: thought.ThoughtPending,
			// Distinct arrival times so the order is deterministic.
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			UpdatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := q.Enqueue(th); err != nil {
			t.Fatal(err)
		}
	}

	// Default: the default limit, and the envelope says it was truncated.
	out, err := dispatch(t, reg, "thoughts_search", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	total, returned, truncated, items := decodeSearch(t, out)
	if returned != searchDefaultLimit || len(items) != searchDefaultLimit {
		t.Fatalf("default returned %d (envelope %d), want %d", len(items), returned, searchDefaultLimit)
	}
	if total != searchMaxLimit+25 {
		t.Fatalf("total = %d, want %d (what matched, before the bound)", total, searchMaxLimit+25)
	}
	if !truncated {
		t.Fatal("truncated should be true when the bound cut the result")
	}

	// Asking for more than the cap is honoured AT the cap, not refused and
	// not obeyed.
	out, err = dispatch(t, reg, "thoughts_search", `{"limit":10000}`)
	if err != nil {
		t.Fatal(err)
	}
	_, returned, _, items = decodeSearch(t, out)
	if returned != searchMaxLimit || len(items) != searchMaxLimit {
		t.Fatalf("limit=10000 returned %d (envelope %d), want the cap %d", len(items), returned, searchMaxLimit)
	}

	// A small limit is honoured, and the bound is not confused with "no more".
	out, err = dispatch(t, reg, "thoughts_search", `{"limit":3}`)
	if err != nil {
		t.Fatal(err)
	}
	_, returned, truncated, items = decodeSearch(t, out)
	if returned != 3 || len(items) != 3 || !truncated {
		t.Fatalf("limit=3 returned %d (truncated=%v), want 3 truncated", len(items), truncated)
	}

	// A result that fits the bound is NOT truncated: filter down to fewer
	// than the limit and confirm the envelope says so.
	out, err = dispatch(t, reg, "thoughts_search", `{"limit":5,"text":"t000"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, returned, truncated, _ = decodeSearch(t, out)
	if truncated {
		t.Fatalf("a result that fits the bound must not report truncated (returned=%d)", returned)
	}
}

// TestThoughtsSearchRejectsWhatItCannotHonour: a silently dropped filter
// returns a superset the caller believes is narrowed.
func TestThoughtsSearchRejectsWhatItCannotHonour(t *testing.T) {
	q := searchQueue(t)
	reg := toolRegistry(t, q)
	bad := []string{
		`{"priority_min":5,"priority_max":3}`, // inverted band
		`{"sort":"nonsense"}`,
		`{"created_after":"not-a-time"}`,
		`{"created_before":"not-a-time"}`,
		`{"status":"pending"}`,  // must be an array
		`{"metadata":{"k":1}}`,  // values must be strings
		`{"limit":"lots"}`,      // must be an integer
		`{"unknown_field":"x"}`, // schema rejects unknown keys
	}
	for _, args := range bad {
		if _, err := dispatch(t, reg, "thoughts_search", args); err == nil {
			t.Errorf("thoughts_search(%s) = nil error, want a rejection", args)
		}
	}
}

// TestThoughtsSearchIsReadOnly: a search must not mutate the queue.
func TestThoughtsSearchIsReadOnly(t *testing.T) {
	q := searchQueue(t)
	reg := toolRegistry(t, q)
	before, err := dispatch(t, reg, "thoughts_read", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch(t, reg, "thoughts_search", `{"text":"work","sort":"age"}`); err != nil {
		t.Fatal(err)
	}
	after, err := dispatch(t, reg, "thoughts_read", "")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("thoughts_search mutated the queue")
	}
}

// ── thoughts_update: park with a stated reason (NAVI-577) ────────────

// TestThoughtsUpdateParkWithReason is the acceptance criterion: a park can
// carry a stated reason, so a shelved thought is legible.
func TestThoughtsUpdateParkWithReason(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	if err := q.Enqueue(&thought.Thought{ID: "shelf", Prompt: "waiting", Priority: 5, Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}

	out, err := dispatch(t, reg, "thoughts_update", `{"id":"shelf","status":"pause","reason":"waiting on the vendor's reply"}`)
	if err != nil {
		t.Fatalf("thoughts_update: %v", err)
	}
	if !strings.Contains(out, "waiting on the vendor") {
		t.Fatalf("park note not in the response: %s", out)
	}
	got, err := q.GetThought("shelf")
	if err != nil {
		t.Fatal(err)
	}
	if got.ParkNote != "waiting on the vendor's reply" {
		t.Fatalf("ParkNote = %q, want the stated reason", got.ParkNote)
	}
	// A stated reason is a DELIBERATE park, never auto-resumed (ADR 0035 d8).
	if got.ParkReason != thought.ParkDeliberate {
		t.Fatalf("ParkReason = %q, want deliberate", got.ParkReason)
	}

	// The reason is visible in a read, so it is legible without a second call.
	read, err := dispatch(t, reg, "thoughts_read", `{"status":"paused"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, "waiting on the vendor") {
		t.Fatalf("park note not surfaced in thoughts_read: %s", read)
	}

	// Resuming clears it: the reason belonged to the park that ended.
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"shelf","status":"resume"}`); err != nil {
		t.Fatal(err)
	}
	got, _ = q.GetThought("shelf")
	if got.ParkNote != "" {
		t.Fatalf("ParkNote survived a resume: %q", got.ParkNote)
	}
}

// TestThoughtsUpdateReasonRequiresPause: a reason with no park would be
// recorded nowhere, so it is refused rather than silently dropped.
func TestThoughtsUpdateReasonRequiresPause(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	_ = q.Enqueue(&thought.Thought{ID: "t", Prompt: "x", Priority: 5, Status: thought.ThoughtPending})
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"t","reason":"because"}`); err == nil {
		t.Fatal("a reason without status=pause must be refused")
	}
}

// TestThoughtsUpdateMergesMetadata: metadata is merged, not replaced, and the
// origin safety rail is not writable.
func TestThoughtsUpdateMergesMetadata(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	_ = q.Enqueue(&thought.Thought{
		ID: "t", Prompt: "x", Priority: 5, Status: thought.ThoughtPending,
		Metadata: map[string]string{"origin": "tool", "keep": "me"},
	})

	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"t","metadata":{"shelf":"vendor","added":"yes"}}`); err != nil {
		t.Fatalf("thoughts_update: %v", err)
	}
	got, _ := q.GetThought("t")
	if got.Metadata["keep"] != "me" {
		t.Fatalf("merge dropped an existing key: %v", got.Metadata)
	}
	if got.Metadata["shelf"] != "vendor" || got.Metadata["added"] != "yes" {
		t.Fatalf("merge did not add keys: %v", got.Metadata)
	}
	if got.Metadata["origin"] != "tool" {
		t.Fatalf("origin tag changed: %v", got.Metadata)
	}

	// The origin tag cannot be rewritten through the tool (the safety rail
	// that keeps self-scheduled work clamped).
	if _, err := dispatch(t, reg, "thoughts_update", `{"id":"t","metadata":{"origin":"external"}}`); err != nil {
		t.Fatalf("a metadata update naming only origin should be a no-op, not an error: %v", err)
	}
	got, _ = q.GetThought("t")
	if got.Metadata["origin"] != "tool" {
		t.Fatalf("origin tag was rewritten through metadata: %v", got.Metadata)
	}
}

// TestThoughtsReadUsesTheQueueOrder pins the consistency fix: the view the
// model reasons about is the order the scheduler runs, not last-activity.
// seededQueue builds a queue whose thoughts carry the EXACT timestamps given,
// by persisting them and letting crash recovery load them. Enqueue stamps
// UpdatedAt with the wall clock, so a test that needs arrival order and
// last-activity order to disagree cannot go through Enqueue.
func seededQueue(t *testing.T, thoughts []*thought.Thought) *thought.Queue {
	t.Helper()
	store := thought.NewMemoryStore()
	for _, th := range thoughts {
		if err := store.SaveThought(th); err != nil {
			t.Fatalf("seed %s: %v", th.ID, err)
		}
	}
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 3, SlotExhaustion: thought.PolicyQueue}, store)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestThoughtsReadUsesTheQueueOrder(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)
	base := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	// Same priority. Arrival order is [older, younger] (older arrived first);
	// last-activity order is [younger, older] because the YOUNGER one is
	// touched last. The two orders must disagree, or the test proves nothing.
	// Arrival: older first. Last activity: younger touched last. The two
	// orders must DISAGREE or the test proves nothing.
	older := &thought.Thought{
		ID: "older", Prompt: "first", Priority: 5, Status: thought.ThoughtPending,
		CreatedAt: base, UpdatedAt: base,
	}
	younger := &thought.Thought{
		ID: "younger", Prompt: "second", Priority: 5, Status: thought.ThoughtPending,
		CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Hour),
	}
	// Enqueue stamps UpdatedAt with the wall clock, so both would read "now"
	// and the two orders would agree. Seed a store directly instead, so the
	// timestamps are exactly what the test needs.
	_ = older
	_ = younger
	q2 := seededQueue(t, []*thought.Thought{older, younger})
	reg = toolRegistry(t, q2)
	q = q2

	out, err := dispatch(t, reg, "thoughts_read", "")
	if err != nil {
		t.Fatal(err)
	}
	items := decodeList(t, out)
	if items[0]["id"] != "older" {
		t.Fatalf("thoughts_read order = %v, want arrival order (older first, despite younger being touched last)", searchIDs(items))
	}
}

// ── thoughts_search cost on a long queue (NAVI-577) ──────────────────

// BenchmarkThoughtsSearchCost measures what a triaging thought pays per call
// as the queue grows. The acceptance criterion is that the number is KNOWN,
// not that it is small: a navi's queue is a working set, so the question is
// whether the O(n) snapshot-and-clone dominates at realistic sizes.
func BenchmarkThoughtsSearchCost(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("queue=%d", n), func(b *testing.B) {
			q, err := thought.NewQueue(thought.Config{MaxConcurrent: 3, SlotExhaustion: thought.PolicyQueue}, thought.NewMemoryStore())
			if err != nil {
				b.Fatal(err)
			}
			base := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
			for i := 0; i < n; i++ {
				th := &thought.Thought{
					ID: fmt.Sprintf("t%05d", i), Prompt: "work item", Context: "some context",
					Priority: i % 11, Status: thought.ThoughtPending,
					CreatedAt: base.Add(time.Duration(i) * time.Second),
					UpdatedAt: base.Add(time.Duration(i) * time.Second),
					Metadata:  map[string]string{"source": "bench", "i": fmt.Sprint(i)},
				}
				if err := q.Enqueue(th); err != nil {
					b.Fatal(err)
				}
			}
			handle := registry.QueueHandle{Queue: q}
			args := map[string]any{"priority_max": float64(3), "limit": float64(searchDefaultLimit)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := handleSearch(context.Background(), args, handle); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkThoughtsReadCost is the baseline the search is measured against:
// thoughts_read already took the full snapshot, so the search's added cost is
// the filtering and sorting over it.
func BenchmarkThoughtsReadCost(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("queue=%d", n), func(b *testing.B) {
			q, err := thought.NewQueue(thought.Config{MaxConcurrent: 3, SlotExhaustion: thought.PolicyQueue}, thought.NewMemoryStore())
			if err != nil {
				b.Fatal(err)
			}
			base := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
			for i := 0; i < n; i++ {
				th := &thought.Thought{
					ID: fmt.Sprintf("t%05d", i), Prompt: "work item", Context: "some context",
					Priority: i % 11, Status: thought.ThoughtPending,
					CreatedAt: base.Add(time.Duration(i) * time.Second),
					UpdatedAt: base.Add(time.Duration(i) * time.Second),
					Metadata:  map[string]string{"source": "bench", "i": fmt.Sprint(i)},
				}
				if err := q.Enqueue(th); err != nil {
					b.Fatal(err)
				}
			}
			handle := registry.QueueHandle{Queue: q}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := handleRead(context.Background(), map[string]any{}, handle); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
