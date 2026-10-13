// Package conformance is the shared backend conformance suite: one suite
// every datastore backend runs, so a new backend is *provable* rather than
// plausible (ADR 0011; NAVI-376).
//
// It is part of the contract layer, not a test-private helper: a backend in
// its own module imports this package and runs it against its
// implementation. That property is the point — a suite only the in-repo
// backends can use would prove nothing about the contract. Consequently
// this package imports `datastore` (and `thought`) plus the standard
// library, and never an implementation package.
//
//	func TestBackend(t *testing.T) {
//		conformance.Run(t, func() (datastore.Backend, error) { return New(), nil })
//	}
//
// It asserts the contract's *behaviour*: round-trip per aggregate, the
// metadata/body split, sentinel errors instead of silent no-ops, caller
// memory isolation, and safe concurrent access. It does not assert how a
// backend stores anything — only what a consumer can observe through the
// views.
package conformance

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// OpenFunc opens a fresh, empty backend. Each call must return an
// independent backend: the suite opens one per check so a failure cannot
// be blamed on state a previous check left behind.
type OpenFunc func() (datastore.Backend, error)

// ReadReporter is the optional instrumentation a backend may implement so
// the suite can *prove* that metadata reads do not load transcript bodies
// (ADR 0011 decision 5 — the property that makes unbounded retention
// affordable). A backend that does not implement it is not failed: the
// suite reports the property as unverified instead of passing silently.
//
// BodyReads counts transcript body loads (a `LoadTranscript`, or whatever
// a storage layer does to fetch a conversation) since the last ResetReads.
// Counting a `TranscriptInfo` as a body read is exactly the defect this
// check exists to catch.
type ReadReporter interface {
	ResetReads()
	BodyReads() int
}

// Run runs the whole suite against backends opened by open.
func Run(t *testing.T, open OpenFunc) {
	t.Helper()
	t.Run("Thoughts", func(t *testing.T) { Thoughts(t, open) })
	t.Run("ToolState", func(t *testing.T) { ToolState(t, open) })
	t.Run("Memory", func(t *testing.T) { Memory(t, open) })
	t.Run("ThroughTheHandle", func(t *testing.T) { ThroughTheHandle(t, open) })
	t.Run("ConcurrentAccess", func(t *testing.T) { ConcurrentAccess(t, open) })
}

func mustOpen(t *testing.T, open OpenFunc) datastore.Backend {
	t.Helper()
	b, err := open()
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}
	if b == nil {
		t.Fatal("open returned a nil backend")
	}
	return b
}

// sameTime compares instants at second precision: the contract does not
// specify sub-second storage, and a text or column format may legitimately
// round. A backend that loses more than a second has lost real data.
func sameTime(a, b time.Time) bool {
	return a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}

// ── thought/transcript aggregate ────────────────────────────────────────

// Thoughts asserts the thought-row and transcript behaviour.
func Thoughts(t *testing.T, open OpenFunc) {
	t.Run("ThoughtRoundTrip", func(t *testing.T) {
		v := mustOpen(t, open).Thoughts()

		due := time.Now().UTC().Add(time.Hour)
		created := time.Now().UTC().Add(-2 * time.Hour)
		updated := time.Now().UTC()
		want := &thought.Thought{
			ID:        "t-round-trip",
			Prompt:    "deploy the thing",
			Context:   "steering notes",
			Result:    "done",
			Metadata:  map[string]string{"origin": "self", "role": "verification"},
			Priority:  2,
			Status:    thought.ThoughtPending,
			Provider:  "ollama",
			Model:     "qwen3:8b",
			Mode:      "execute",
			Skills:    []string{"deploy_runbook", "core_tools"},
			DependsOn: []string{"t-predecessor"},
			Origin:    principal.MustParse("user:alice"),
			Lineage:   []string{"t-parent", "t-grandparent"},
			OriginInfo: thought.OriginInfo{
				Transport:  "lan",
				Channel:    "console",
				RemoteAddr: "10.0.0.7:51234",
				At:         created,
			},
			Assembled:  "## Skill: core_thoughts\n…",
			DeferUntil: due,
			LogPath:    "/var/log/t.log",
			Checkpoint: []byte("navicp1:[]"),
			CreatedAt:  created,
			UpdatedAt:  updated,
		}
		if err := v.SaveThought(want); err != nil {
			t.Fatalf("SaveThought: %v", err)
		}

		got, err := v.LoadThought(want.ID)
		if err != nil {
			t.Fatalf("LoadThought: %v", err)
		}
		for _, d := range diffThought(want, got) {
			t.Errorf("round-trip lost %s", d)
		}

		all, err := v.LoadThoughts()
		if err != nil {
			t.Fatalf("LoadThoughts: %v", err)
		}
		if len(all) != 1 || all[0].ID != want.ID {
			t.Fatalf("LoadThoughts = %d thoughts, want the one saved", len(all))
		}

		// An update is an upsert, not a duplicate.
		want.Result = "done again"
		if err := v.SaveThought(want); err != nil {
			t.Fatalf("SaveThought (update): %v", err)
		}
		if all, err := v.LoadThoughts(); err != nil || len(all) != 1 {
			t.Fatalf("update produced %d rows (%v), want 1", len(all), err)
		}

		if err := v.DeleteThought(want.ID); err != nil {
			t.Fatalf("DeleteThought: %v", err)
		}
		if _, err := v.LoadThought(want.ID); !errors.Is(err, thought.ErrThoughtNotFound) {
			t.Fatalf("LoadThought after delete = %v, want ErrThoughtNotFound", err)
		}
	})

	t.Run("ThoughtMemoryIsNotAliased", func(t *testing.T) {
		v := mustOpen(t, open).Thoughts()

		th := &thought.Thought{
			ID:         "t-alias",
			Prompt:     "original",
			Metadata:   map[string]string{"k": "v"},
			Skills:     []string{"a"},
			DependsOn:  []string{"b"},
			Checkpoint: []byte("one"),
		}
		if err := v.SaveThought(th); err != nil {
			t.Fatalf("SaveThought: %v", err)
		}
		// Mutate everything the caller owns after the write.
		th.Prompt = "mutated"
		th.Metadata["k"] = "mutated"
		th.Skills[0] = "mutated"
		th.DependsOn[0] = "mutated"
		th.Checkpoint[0] = 'X'

		got, err := v.LoadThought(th.ID)
		if err != nil {
			t.Fatalf("LoadThought: %v", err)
		}
		if got.Prompt != "original" {
			t.Errorf("prompt = %q, want the stored value (backend aliased caller memory)", got.Prompt)
		}
		if got.Metadata["k"] != "v" {
			t.Errorf("metadata = %v, want the stored value", got.Metadata)
		}
		if len(got.Skills) != 1 || got.Skills[0] != "a" {
			t.Errorf("skills = %v, want the stored value", got.Skills)
		}
		if len(got.DependsOn) != 1 || got.DependsOn[0] != "b" {
			t.Errorf("depends_on = %v, want the stored value", got.DependsOn)
		}
		if string(got.Checkpoint) != "one" {
			t.Errorf("checkpoint = %q, want the stored value", got.Checkpoint)
		}

		// A returned copy must not be a live handle either.
		got.Metadata["k"] = "re-mutated"
		again, err := v.LoadThought(th.ID)
		if err != nil {
			t.Fatalf("LoadThought: %v", err)
		}
		if again.Metadata["k"] != "v" {
			t.Errorf("metadata = %v after mutating a loaded copy, want the stored value", again.Metadata)
		}
	})

	t.Run("TranscriptRoundTrip", func(t *testing.T) {
		v := mustOpen(t, open).Thoughts()

		base := time.Now().UTC().Add(-time.Minute)
		entries := []datastore.Entry{
			{ID: "e1", Time: base, Role: datastore.RoleSystem, Message: "assembled context"},
			{ID: "e2", Time: base.Add(time.Second), Role: datastore.RoleUser, Message: "hi"},
			{
				ID: "e3", Time: base.Add(2 * time.Second), Role: datastore.RoleAssistant,
				Message: "calling a tool", ToolCallID: "call-1", ToolName: "shell_exec",
				ToolInput: []byte(`{"command":"true"}`),
			},
		}
		if err := v.SaveTranscript("t-transcript", entries); err != nil {
			t.Fatalf("SaveTranscript: %v", err)
		}

		got, err := v.LoadTranscript("t-transcript")
		if err != nil {
			t.Fatalf("LoadTranscript: %v", err)
		}
		if len(got) != len(entries) {
			t.Fatalf("transcript has %d entries, want %d", len(got), len(entries))
		}
		for i := range entries {
			if !reflect.DeepEqual(entries[i], got[i]) {
				t.Errorf("entry %d: got %+v, want %+v (order and fields must round-trip)", i, got[i], entries[i])
			}
		}

		info, err := v.TranscriptInfo("t-transcript")
		if err != nil {
			t.Fatalf("TranscriptInfo: %v", err)
		}
		if info.ThoughtID != "t-transcript" {
			t.Errorf("TranscriptInfo.ThoughtID = %q", info.ThoughtID)
		}
		if info.MessageCount != len(entries) {
			t.Errorf("TranscriptInfo.MessageCount = %d, want %d (it must agree with the body)", info.MessageCount, len(entries))
		}
		if !sameTime(info.UpdatedAt, entries[len(entries)-1].Time) {
			t.Errorf("TranscriptInfo.UpdatedAt = %v, want the latest entry time %v", info.UpdatedAt, entries[len(entries)-1].Time)
		}

		// A transcript is written as a unit: saving again replaces it.
		if err := v.SaveTranscript("t-transcript", entries[:2]); err != nil {
			t.Fatalf("SaveTranscript (replace): %v", err)
		}
		if got, err := v.LoadTranscript("t-transcript"); err != nil || len(got) != 2 {
			t.Fatalf("after replace: %d entries (%v), want 2", len(got), err)
		}

		// Missing transcripts are a sentinel, not an empty success.
		if _, err := v.LoadTranscript("t-absent"); !errors.Is(err, datastore.ErrTranscriptNotFound) {
			t.Errorf("LoadTranscript(missing) = %v, want ErrTranscriptNotFound", err)
		}
		if _, err := v.TranscriptInfo("t-absent"); !errors.Is(err, datastore.ErrTranscriptNotFound) {
			t.Errorf("TranscriptInfo(missing) = %v, want ErrTranscriptNotFound", err)
		}

		if err := v.DeleteTranscript("t-transcript"); err != nil {
			t.Fatalf("DeleteTranscript: %v", err)
		}
		if _, err := v.LoadTranscript("t-transcript"); !errors.Is(err, datastore.ErrTranscriptNotFound) {
			t.Errorf("LoadTranscript after delete = %v, want ErrTranscriptNotFound", err)
		}
	})

	t.Run("MetadataReadsNoBodies", func(t *testing.T) {
		backend := mustOpen(t, open)
		reporter, ok := backend.(ReadReporter)
		if !ok {
			t.Skip("backend does not implement conformance.ReadReporter: metadata-without-bodies NOT VERIFIED")
		}
		v := backend.Thoughts()

		// A transcript with a real body to load, plus a thought row.
		entries := make([]datastore.Entry, 0, 50)
		for i := 0; i < 50; i++ {
			entries = append(entries, datastore.Entry{
				ID:      fmt.Sprintf("e%d", i),
				Time:    time.Now().UTC(),
				Role:    datastore.RoleAssistant,
				Message: strings.Repeat("body ", 200),
			})
		}
		if err := v.SaveTranscript("t-metadata", entries); err != nil {
			t.Fatalf("SaveTranscript: %v", err)
		}
		if err := v.SaveThought(&thought.Thought{ID: "t-metadata", Prompt: "p", Status: thought.ThoughtPending}); err != nil {
			t.Fatalf("SaveThought: %v", err)
		}

		reporter.ResetReads()
		if _, err := v.TranscriptInfo("t-metadata"); err != nil {
			t.Fatalf("TranscriptInfo: %v", err)
		}
		if _, err := v.LoadThoughts(); err != nil {
			t.Fatalf("LoadThoughts: %v", err)
		}
		if _, err := v.LoadThought("t-metadata"); err != nil {
			t.Fatalf("LoadThought: %v", err)
		}
		if n := reporter.BodyReads(); n != 0 {
			t.Errorf("metadata reads loaded %d transcript body/bodies: enumeration must not touch bodies (ADR 0011 decision 5)", n)
		}

		// The counter must actually be wired: a body read is reported.
		reporter.ResetReads()
		if _, err := v.LoadTranscript("t-metadata"); err != nil {
			t.Fatalf("LoadTranscript: %v", err)
		}
		if n := reporter.BodyReads(); n == 0 {
			t.Error("LoadTranscript reported no body read: the ReadReporter instrumentation is not wired")
		}
	})

	t.Run("TranscriptInfosEnumeratesMetadata", func(t *testing.T) {
		backend := mustOpen(t, open)
		v := backend.Thoughts()

		// Three transcripts with bodies, one thought row without a
		// transcript: the enumeration answers for transcripts only, without
		// loading any of them.
		base := time.Now().UTC().Add(-time.Minute)
		for i, id := range []string{"t-bulk-a", "t-bulk-b", "t-bulk-c"} {
			entries := []datastore.Entry{
				{ID: id + "-e1", Time: base, Role: datastore.RoleUser, Message: strings.Repeat("body ", 100)},
				{ID: id + "-e2", Time: base.Add(time.Duration(i) * time.Second), Role: datastore.RoleAssistant, Message: "reply"},
			}
			if err := v.SaveTranscript(id, entries); err != nil {
				t.Fatalf("SaveTranscript(%s): %v", id, err)
			}
		}
		if err := v.SaveThought(&thought.Thought{ID: "t-bulk-row", Prompt: "p", Status: thought.ThoughtPending}); err != nil {
			t.Fatalf("SaveThought: %v", err)
		}

		reporter, instrumented := backend.(ReadReporter)
		if instrumented {
			reporter.ResetReads()
		}
		infos, err := v.TranscriptInfos()
		if err != nil {
			t.Fatalf("TranscriptInfos: %v", err)
		}
		if instrumented && reporter.BodyReads() != 0 {
			t.Errorf("TranscriptInfos loaded %d transcript body/bodies: cold-start enumeration must be metadata-only (ADR 0011 decision 5)", reporter.BodyReads())
		}

		got := map[string]datastore.TranscriptInfo{}
		for _, info := range infos {
			got[info.ThoughtID] = info
		}
		for _, id := range []string{"t-bulk-a", "t-bulk-b", "t-bulk-c"} {
			info, ok := got[id]
			if !ok {
				t.Errorf("TranscriptInfos is missing %q", id)
				continue
			}
			if info.MessageCount != 2 {
				t.Errorf("%s: MessageCount = %d, want 2", id, info.MessageCount)
			}
		}
		if _, ok := got["t-bulk-row"]; ok {
			t.Error("TranscriptInfos reported a thought with no transcript")
		}
	})
}

// diffThought lists the durable fields whose round-trip was lost. Runtime
// bookkeeping (slot/worker assignment, preemption requests) and
// `Foreground` are deliberately not checked: they are scheduler state a
// persistence layer is not required to carry.
func diffThought(want, got *thought.Thought) []string {
	var out []string
	add := func(field string, ok bool) {
		if !ok {
			out = append(out, field)
		}
	}
	add("ID", want.ID == got.ID)
	add("Prompt", want.Prompt == got.Prompt)
	add("Context", want.Context == got.Context)
	add("Result", want.Result == got.Result)
	add("Metadata", reflect.DeepEqual(want.Metadata, got.Metadata))
	add("Priority", want.Priority == got.Priority)
	add("Status", want.Status == got.Status)
	add("Provider", want.Provider == got.Provider)
	add("Model", want.Model == got.Model)
	add("Mode", want.Mode == got.Mode)
	add("Skills", reflect.DeepEqual(want.Skills, got.Skills))
	add("DependsOn", reflect.DeepEqual(want.DependsOn, got.DependsOn))
	add("Origin", want.Origin == got.Origin)
	add("Lineage", reflect.DeepEqual(want.Lineage, got.Lineage))
	add("OriginInfo.Transport", want.OriginInfo.Transport == got.OriginInfo.Transport)
	add("OriginInfo.Channel", want.OriginInfo.Channel == got.OriginInfo.Channel)
	add("OriginInfo.RemoteAddr", want.OriginInfo.RemoteAddr == got.OriginInfo.RemoteAddr)
	add("OriginInfo.At", sameTime(want.OriginInfo.At, got.OriginInfo.At))
	add("Assembled", want.Assembled == got.Assembled)
	add("DeferUntil", sameTime(want.DeferUntil, got.DeferUntil))
	add("LogPath", want.LogPath == got.LogPath)
	add("Checkpoint", reflect.DeepEqual(want.Checkpoint, got.Checkpoint))
	add("CreatedAt", sameTime(want.CreatedAt, got.CreatedAt))
	add("UpdatedAt", sameTime(want.UpdatedAt, got.UpdatedAt))
	return out
}

// ── tool state aggregate ────────────────────────────────────────────────

// ToolState asserts durable todo and note behaviour, including the
// per-navi scoping the interface requires.
func ToolState(t *testing.T, open OpenFunc) {
	t.Run("TodoReplaceAndRead", func(t *testing.T) {
		v := mustOpen(t, open).ToolState()

		items, err := v.ReplaceTodos("navi-a", []datastore.Todo{
			{Text: "second", Priority: 5},
			{Text: "first", Priority: 1, Status: datastore.TodoInProgress},
		})
		if err != nil {
			t.Fatalf("ReplaceTodos: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("ReplaceTodos returned %d items, want 2", len(items))
		}
		for i, it := range items {
			if it.ID == "" {
				t.Errorf("item %d has no assigned id", i)
			}
			if it.NaviID != "navi-a" {
				t.Errorf("item %d NaviID = %q, want the scoping key", i, it.NaviID)
			}
			if it.CreatedAt.IsZero() || it.UpdatedAt.IsZero() {
				t.Errorf("item %d has no store-managed timestamps", i)
			}
		}

		// An unset status takes the pending default rather than an
		// invalid zero value.
		for _, it := range items {
			if it.Text == "second" && it.Status != datastore.TodoPending {
				t.Errorf("unset status = %q, want %q", it.Status, datastore.TodoPending)
			}
		}

		listed, err := v.ListTodos("navi-a", "", 0)
		if err != nil {
			t.Fatalf("ListTodos: %v", err)
		}
		if len(listed) != 2 || listed[0].Text != "first" {
			t.Fatalf("ListTodos = %+v, want priority order with 'first' leading", listed)
		}
		if filtered, err := v.ListTodos("navi-a", datastore.TodoInProgress, 0); err != nil || len(filtered) != 1 {
			t.Fatalf("status filter = %d items (%v), want 1", len(filtered), err)
		}
		if limited, err := v.ListTodos("navi-a", "", 1); err != nil || len(limited) != 1 {
			t.Fatalf("limit = %d items (%v), want 1", len(limited), err)
		}

		// An invalid status is an error, never a silent success.
		if _, err := v.ReplaceTodos("navi-a", []datastore.Todo{{Text: "x", Status: "nonsense"}}); err == nil {
			t.Error("ReplaceTodos accepted an invalid status")
		}
	})

	t.Run("TodoUpdateIsAtomic", func(t *testing.T) {
		v := mustOpen(t, open).ToolState()

		items, err := v.ReplaceTodos("navi-a", []datastore.Todo{
			{Text: "keep", Priority: 1},
			{Text: "change me", Priority: 2},
		})
		if err != nil {
			t.Fatalf("ReplaceTodos: %v", err)
		}
		keepID, changeID := items[0].ID, items[1].ID

		// One valid update and one unknown id: nothing may land.
		text := "changed"
		_, err = v.UpdateTodos("navi-a", []datastore.TodoUpdate{
			{ID: changeID, Text: &text},
			{ID: "no-such-todo", Remove: true},
		})
		if !errors.Is(err, datastore.ErrTodoNotFound) {
			t.Fatalf("UpdateTodos with an unknown id = %v, want ErrTodoNotFound", err)
		}
		after, err := v.ListTodos("navi-a", "", 0)
		if err != nil {
			t.Fatalf("ListTodos: %v", err)
		}
		if len(after) != 2 {
			t.Fatalf("rejected batch removed items: %d remain, want 2", len(after))
		}
		for _, it := range after {
			if it.ID == changeID && it.Text != "change me" {
				t.Errorf("rejected batch partially applied: text = %q", it.Text)
			}
		}

		// A valid batch lands, including removal.
		status := datastore.TodoDone
		updated, err := v.UpdateTodos("navi-a", []datastore.TodoUpdate{
			{ID: changeID, Text: &text},
			{ID: keepID, Status: &status},
		})
		if err != nil {
			t.Fatalf("UpdateTodos: %v", err)
		}
		if len(updated) != 2 {
			t.Fatalf("UpdateTodos returned %d items, want 2", len(updated))
		}
		if _, err := v.UpdateTodos("navi-a", []datastore.TodoUpdate{{ID: changeID, Remove: true}}); err != nil {
			t.Fatalf("UpdateTodos (remove): %v", err)
		}
		if after, err := v.ListTodos("navi-a", "", 0); err != nil || len(after) != 1 || after[0].ID != keepID {
			t.Fatalf("after removal: %+v (%v), want only the kept item", after, err)
		}
	})

	t.Run("NoteCRUD", func(t *testing.T) {
		v := mustOpen(t, open).ToolState()

		note, err := v.UpsertNote("navi-a", datastore.Note{Title: "runbook", Body: "how to deploy", Tags: []string{"ops", "deploy"}})
		if err != nil {
			t.Fatalf("UpsertNote: %v", err)
		}
		if note.ID == "" {
			t.Fatal("UpsertNote did not assign an id")
		}
		if note.CreatedAt.IsZero() || note.UpdatedAt.IsZero() {
			t.Error("UpsertNote did not set timestamps")
		}

		if byID, err := v.ReadNotes("navi-a", datastore.NoteQuery{ID: note.ID}); err != nil || len(byID) != 1 {
			t.Fatalf("read by id = %d notes (%v), want 1", len(byID), err)
		}
		if byTag, err := v.ReadNotes("navi-a", datastore.NoteQuery{Tag: "deploy"}); err != nil || len(byTag) != 1 {
			t.Fatalf("read by tag = %d notes (%v), want 1", len(byTag), err)
		}
		if byText, err := v.ReadNotes("navi-a", datastore.NoteQuery{Query: "depl"}); err != nil || len(byText) != 1 {
			t.Fatalf("read by substring = %d notes (%v), want 1", len(byText), err)
		}
		if none, err := v.ReadNotes("navi-a", datastore.NoteQuery{Query: "absent"}); err != nil || len(none) != 0 {
			t.Fatalf("read with no match = %d notes (%v), want 0", len(none), err)
		}

		// Updating keeps the creation time and moves the update time.
		updated, err := v.UpsertNote("navi-a", datastore.Note{ID: note.ID, Title: "runbook", Body: "how to deploy v2"})
		if err != nil {
			t.Fatalf("UpsertNote (update): %v", err)
		}
		if !sameTime(updated.CreatedAt, note.CreatedAt) {
			t.Errorf("CreatedAt moved on update: %v -> %v", note.CreatedAt, updated.CreatedAt)
		}
		if updated.Body != "how to deploy v2" {
			t.Errorf("body = %q, want the updated value", updated.Body)
		}
		if byID, err := v.ReadNotes("navi-a", datastore.NoteQuery{ID: note.ID}); err != nil || len(byID) != 1 {
			t.Fatalf("update duplicated the note: %d notes (%v)", len(byID), err)
		}

		if err := v.RemoveNote("navi-a", note.ID); err != nil {
			t.Fatalf("RemoveNote: %v", err)
		}
		if err := v.RemoveNote("navi-a", note.ID); !errors.Is(err, datastore.ErrNoteNotFound) {
			t.Errorf("RemoveNote(unknown) = %v, want ErrNoteNotFound", err)
		}
	})

	t.Run("PerNaviScoping", func(t *testing.T) {
		v := mustOpen(t, open).ToolState()

		if _, err := v.ReplaceTodos("navi-a", []datastore.Todo{{Text: "a's todo", Priority: 1}}); err != nil {
			t.Fatalf("ReplaceTodos(navi-a): %v", err)
		}
		if _, err := v.ReplaceTodos("navi-b", []datastore.Todo{{Text: "b's todo", Priority: 1}}); err != nil {
			t.Fatalf("ReplaceTodos(navi-b): %v", err)
		}
		noteA, err := v.UpsertNote("navi-a", datastore.Note{Title: "a's note", Body: "secret a"})
		if err != nil {
			t.Fatalf("UpsertNote(navi-a): %v", err)
		}

		if b, err := v.ListTodos("navi-b", "", 0); err != nil || len(b) != 1 || b[0].Text != "b's todo" {
			t.Fatalf("navi-b sees %+v (%v), want only its own todo", b, err)
		}
		if b, err := v.ReadNotes("navi-b", datastore.NoteQuery{}); err != nil || len(b) != 0 {
			t.Fatalf("navi-b sees %d of navi-a's notes (%v), want 0", len(b), err)
		}
		if err := v.RemoveNote("navi-b", noteA.ID); !errors.Is(err, datastore.ErrNoteNotFound) {
			t.Fatalf("navi-b removed navi-a's note: %v (want ErrNoteNotFound)", err)
		}
		if _, err := v.UpdateTodos("navi-b", []datastore.TodoUpdate{{ID: "whatever", Remove: true}}); !errors.Is(err, datastore.ErrTodoNotFound) {
			t.Fatalf("cross-navi update = %v, want ErrTodoNotFound", err)
		}
	})
}

// ── memory aggregate ───────────────────────────────────────────────────

// Memory asserts remember/recall/forget behaviour, scoping, and that
// expiry is carried rather than interpreted.
func Memory(t *testing.T, open OpenFunc) {
	t.Run("RememberRecallForget", func(t *testing.T) {
		v := mustOpen(t, open).Memory()

		rec, err := v.Remember(datastore.MemoryRecord{
			Scope: datastore.ScopeUser, Subject: principal.MustParse("user:flintwillow"), Content: "prefers terse output",
			Tags: []string{"style"}, Importance: 0.4,
		})
		if err != nil {
			t.Fatalf("Remember: %v", err)
		}
		if rec.ID == "" {
			t.Fatal("Remember did not assign an id")
		}
		if rec.CreatedAt.IsZero() || rec.UpdatedAt.IsZero() {
			t.Error("Remember did not set timestamps")
		}
		if _, err := v.Remember(datastore.MemoryRecord{
			Scope: datastore.ScopeNavi, Subject: principal.MustParse("navi:other"), Content: "deploys on fridays",
			Tags: []string{"ops"}, Importance: 0.9,
		}); err != nil {
			t.Fatalf("Remember: %v", err)
		}

		if got, err := v.Recall(datastore.RecallQuery{Scope: datastore.ScopeUser}); err != nil || len(got) != 1 || got[0].ID != rec.ID {
			t.Fatalf("recall by scope = %+v (%v), want the user record", got, err)
		}
		if got, err := v.Recall(datastore.RecallQuery{Subject: principal.MustParse("navi:other")}); err != nil || len(got) != 1 {
			t.Fatalf("recall by subject = %d records (%v), want 1", len(got), err)
		}
		if got, err := v.Recall(datastore.RecallQuery{Tags: []string{"ops"}}); err != nil || len(got) != 1 {
			t.Fatalf("recall by tag = %d records (%v), want 1", len(got), err)
		}
		if got, err := v.Recall(datastore.RecallQuery{Tags: []string{"ops", "nope"}}); err != nil || len(got) != 0 {
			t.Fatalf("recall requires every tag: got %d records (%v)", len(got), err)
		}
		if got, err := v.Recall(datastore.RecallQuery{Query: "TERSE"}); err != nil || len(got) != 1 {
			t.Fatalf("recall by substring = %d records (%v), want 1 (matching must not be case-sensitive on the body)", len(got), err)
		}
		// Most relevant first.
		if got, err := v.Recall(datastore.RecallQuery{}); err != nil || len(got) != 2 || got[0].Importance < got[1].Importance {
			t.Fatalf("recall order = %+v (%v), want most important first", got, err)
		}
		if got, err := v.Recall(datastore.RecallQuery{Limit: 1}); err != nil || len(got) != 1 {
			t.Fatalf("recall limit = %d records (%v), want 1", len(got), err)
		}

		if err := v.Forget(rec.ID); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if got, err := v.Recall(datastore.RecallQuery{Scope: datastore.ScopeUser}); err != nil || len(got) != 0 {
			t.Fatalf("recall after forget = %d records (%v), want 0", len(got), err)
		}
		if err := v.Forget(rec.ID); !errors.Is(err, datastore.ErrMemoryNotFound) {
			t.Errorf("Forget(unknown) = %v, want ErrMemoryNotFound", err)
		}
	})

	t.Run("UpdatePreservesCreationTime", func(t *testing.T) {
		v := mustOpen(t, open).Memory()

		first, err := v.Remember(datastore.MemoryRecord{ID: "mem-1", Scope: datastore.ScopeSession, Content: "v1"})
		if err != nil {
			t.Fatalf("Remember: %v", err)
		}
		second, err := v.Remember(datastore.MemoryRecord{ID: "mem-1", Scope: datastore.ScopeSession, Content: "v2"})
		if err != nil {
			t.Fatalf("Remember (update): %v", err)
		}
		if !sameTime(second.CreatedAt, first.CreatedAt) {
			t.Errorf("CreatedAt moved on update: %v -> %v", first.CreatedAt, second.CreatedAt)
		}
		if second.Content != "v2" {
			t.Errorf("content = %q, want the updated value", second.Content)
		}
		if got, err := v.Recall(datastore.RecallQuery{Scope: datastore.ScopeSession}); err != nil || len(got) != 1 {
			t.Fatalf("update duplicated the record: %d (%v)", len(got), err)
		}
	})

	t.Run("ExpiryIsCarriedNotInterpreted", func(t *testing.T) {
		v := mustOpen(t, open).Memory()

		expires := time.Now().UTC().Add(48 * time.Hour)
		if _, err := v.Remember(datastore.MemoryRecord{
			ID: "mem-exp", Scope: datastore.ScopeSession, Content: "short-lived", ExpiresAt: expires,
		}); err != nil {
			t.Fatalf("Remember: %v", err)
		}
		got, err := v.Recall(datastore.RecallQuery{Scope: datastore.ScopeSession})
		if err != nil || len(got) != 1 {
			t.Fatalf("recall = %d records (%v), want 1", len(got), err)
		}
		if !sameTime(got[0].ExpiresAt, expires) {
			t.Errorf("ExpiresAt = %v, want %v carried through storage (the datastore never prunes, NAVI-363 does)", got[0].ExpiresAt, expires)
		}

		// A past expiry is still returned: retention is a separate
		// mechanism, not a read-time filter.
		if _, err := v.Remember(datastore.MemoryRecord{
			ID: "mem-past", Scope: datastore.ScopeSession, Content: "already expired",
			ExpiresAt: time.Now().UTC().Add(-time.Hour),
		}); err != nil {
			t.Fatalf("Remember: %v", err)
		}
		if got, err := v.Recall(datastore.RecallQuery{Scope: datastore.ScopeSession}); err != nil || len(got) != 2 {
			t.Fatalf("recall = %d records (%v), want 2: the backend must not interpret expiry", len(got), err)
		}
	})

	t.Run("TagsAndContentAreNotAliased", func(t *testing.T) {
		v := mustOpen(t, open).Memory()

		rec := datastore.MemoryRecord{ID: "mem-alias", Scope: datastore.ScopeSession, Content: "original", Tags: []string{"t"}}
		if _, err := v.Remember(rec); err != nil {
			t.Fatalf("Remember: %v", err)
		}
		rec.Tags[0] = "mutated"

		got, err := v.Recall(datastore.RecallQuery{Scope: datastore.ScopeSession})
		if err != nil || len(got) != 1 {
			t.Fatalf("recall = %d records (%v), want 1", len(got), err)
		}
		if got[0].Tags[0] != "t" {
			t.Errorf("tags = %v, want the stored value (backend aliased caller memory)", got[0].Tags)
		}
	})
}

// ── the runtime's write path ────────────────────────────────────────────

// ThroughTheHandle drives the backend the way the runtime does — staged on
// the datastore's ordered writer and read back through the handle's views —
// so a backend that passes the view checks above is also proven to work on
// the path that actually persists a thought.
func ThroughTheHandle(t *testing.T, open OpenFunc) {
	b := mustOpen(t, open)
	d := datastore.NewDatastore()
	d.Bind(b)
	if !d.Bound() {
		t.Fatal("handle reports unbound after Bind")
	}

	th := &thought.Thought{ID: "handle-1", Prompt: "via the writer", Status: thought.ThoughtPending}
	if err := d.SubmitSync(datastore.Write{
		Aggregate: datastore.AggregateThoughts,
		Key:       "thoughts/handle-1",
		Apply:     func(b datastore.Backend) error { return b.Thoughts().SaveThought(th) },
	}); err != nil {
		t.Fatalf("SubmitSync: %v", err)
	}
	if got, err := d.Thoughts().LoadThought("handle-1"); err != nil || got.Prompt != "via the writer" {
		t.Fatalf("thought staged on the writer is not readable: %+v (%v)", got, err)
	}

	entries := []datastore.Entry{
		datastore.NewEntry(datastore.RoleUser, "hello"),
		datastore.NewEntry(datastore.RoleAssistant, "world"),
	}
	d.SubmitTranscript("handle-1", entries)
	if err := d.FlushWrites(); err != nil {
		t.Fatalf("FlushWrites: %v", err)
	}
	got, err := d.Thoughts().LoadTranscript("handle-1")
	if err != nil {
		t.Fatalf("LoadTranscript after flush: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("transcript through the handle has %d entries, want %d", len(got), len(entries))
	}
	info, err := d.Thoughts().TranscriptInfo("handle-1")
	if err != nil || info.MessageCount != len(entries) {
		t.Fatalf("TranscriptInfo through the handle = %+v (%v), want %d messages", info, err, len(entries))
	}
	if _, err := d.Memory().Remember(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "through the handle"}); err != nil {
		t.Fatalf("remember through the handle: %v", err)
	}
}

// ── concurrency ─────────────────────────────────────────────────────────

// ConcurrentAccess asserts that readers and writers may overlap: a
// consumer must never be blocked or failed by an in-flight write (ADR 0011
// decision 6 — a slow backend may not block a run or a component read).
// Run under -race, this also covers the backend's own locking.
func ConcurrentAccess(t *testing.T, open OpenFunc) {
	b := mustOpen(t, open)

	const writers, readers = 4, 4
	var wg sync.WaitGroup
	errs := make(chan error, writers*readers)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := fmt.Sprintf("t-conc-%d", w)
			for i := 0; i < 20; i++ {
				if err := b.Thoughts().SaveThought(&thought.Thought{
					ID: id, Prompt: "concurrent", Status: thought.ThoughtPending,
					Metadata: map[string]string{"i": fmt.Sprint(i)},
				}); err != nil {
					errs <- fmt.Errorf("SaveThought: %w", err)
					return
				}
				if err := b.Thoughts().SaveTranscript(id, []datastore.Entry{
					{ID: fmt.Sprintf("e%d", i), Time: time.Now().UTC(), Role: datastore.RoleUser, Message: strings.Repeat("x", 64)},
				}); err != nil {
					errs <- fmt.Errorf("SaveTranscript: %w", err)
					return
				}
				if _, err := b.ToolState().ReplaceTodos("navi-conc", []datastore.Todo{{Text: fmt.Sprint(i), Priority: i}}); err != nil {
					errs <- fmt.Errorf("ReplaceTodos: %w", err)
					return
				}
				if _, err := b.Memory().Remember(datastore.MemoryRecord{
					ID: fmt.Sprintf("m-%d", w), Scope: datastore.ScopeSession, Content: fmt.Sprint(i),
				}); err != nil {
					errs <- fmt.Errorf("Remember: %w", err)
					return
				}
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if _, err := b.Thoughts().LoadThoughts(); err != nil {
					errs <- fmt.Errorf("LoadThoughts: %w", err)
					return
				}
				// A transcript may legitimately not be written yet: that is
				// the documented sentinel, not a failure.
				if _, err := b.Thoughts().LoadTranscript(fmt.Sprintf("t-conc-%d", r%writers)); err != nil &&
					!errors.Is(err, datastore.ErrTranscriptNotFound) {
					errs <- fmt.Errorf("LoadTranscript: %w", err)
					return
				}
				if _, err := b.Thoughts().TranscriptInfo(fmt.Sprintf("t-conc-%d", r%writers)); err != nil &&
					!errors.Is(err, datastore.ErrTranscriptNotFound) {
					errs <- fmt.Errorf("TranscriptInfo: %w", err)
					return
				}
				if _, err := b.ToolState().ListTodos("navi-conc", "", 0); err != nil {
					errs <- fmt.Errorf("ListTodos: %w", err)
					return
				}
				if _, err := b.Memory().Recall(datastore.RecallQuery{Scope: datastore.ScopeSession}); err != nil {
					errs <- fmt.Errorf("Recall: %w", err)
					return
				}
			}
		}(r)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent access: %v", err)
	}

	// Everything the writers finished is readable afterwards.
	for w := 0; w < writers; w++ {
		id := fmt.Sprintf("t-conc-%d", w)
		if _, err := b.Thoughts().LoadThought(id); err != nil {
			t.Errorf("after concurrent writes, LoadThought(%s): %v", id, err)
		}
	}
	if _, err := b.Memory().Recall(datastore.RecallQuery{Scope: datastore.ScopeSession}); err != nil {
		t.Errorf("after concurrent writes, Recall: %v", err)
	}
}
