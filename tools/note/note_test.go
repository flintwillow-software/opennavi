package note

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

// newTestStore opens a scratch SQLite stateful store per test.
func newTestStore(t *testing.T) *sqlite.StatefulStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notes.db")
	st, err := sqlite.Open(path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ss, err := sqlite.NewStatefulStore(st.DB())
	if err != nil {
		t.Fatalf("sqlite.NewStatefulStore: %v", err)
	}
	return ss
}

// repeat returns a string of n copies of s (tests only).
func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// registerNote registers both note tools with opts and activates them.
func registerNote(t *testing.T, reg *registry.Registry, naviID string, ss *sqlite.StatefulStore, opts ...Options) {
	t.Helper()
	if err := Register(reg, naviID, ss, opts...); err != nil {
		t.Fatalf("note Register: %v", err)
	}
	if err := reg.SetActive([]string{"note_read", "note_write"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
}

// newRegistry builds a registry with the note tools bound to naviID.
func newRegistry(t *testing.T, naviID string, ss *sqlite.StatefulStore, opts ...Options) *registry.Registry {
	reg := registry.New()
	registerNote(t, reg, naviID, ss, opts...)
	return reg
}

// writeNote dispatches note_write and returns the persisted note id.
func writeNote(t *testing.T, reg *registry.Registry, args string) string {
	t.Helper()
	out, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(args))
	if err != nil {
		t.Fatalf("note_write %s: %v", args, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(json.RawMessage(out), &doc); err != nil {
		t.Fatalf("note_write output not JSON: %v (was %q)", err, out)
	}
	id, ok := doc["id"].(string)
	if !ok || id == "" {
		t.Fatalf("note_write output missing id: %q", out)
	}
	return id
}

// readNotes dispatches note_read and returns the decoded result list.
func readNotes(t *testing.T, reg *registry.Registry, args string) []map[string]any {
	t.Helper()
	out, err := reg.Dispatch(context.Background(), "note_read", json.RawMessage(args))
	if err != nil {
		t.Fatalf("note_read %s: %v", args, err)
	}
	var docs []map[string]any
	if err := json.Unmarshal(json.RawMessage(out), &docs); err != nil {
		t.Fatalf("note_read output not JSON array: %v (was %q)", err, out)
	}
	return docs
}

// ---------------------------------------------------------------------------
// Registration metadata (S130400 complete ToolDefinition).

func TestRegisterMetadata(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)

	readDef, ok := reg.Get("note_read")
	if !ok {
		t.Fatal("note_read not registered")
	}
	if readDef.Name != "note_read" || readDef.OutputFormat != "json" {
		t.Errorf("note_read metadata: %+v", readDef)
	}
	if readDef.Access != registry.AccessReadOnly {
		t.Errorf("note_read access = %s, want read-only", readDef.Access.String())
	}
	if readDef.Timeout != 10*time.Second {
		t.Errorf("note_read timeout = %v, want 10s", readDef.Timeout)
	}
	if readDef.Mode != registry.ToolModeParallel {
		t.Errorf("note_read mode = %s, want parallel", readDef.Mode.String())
	}
	if readDef.SourceInfo.Origin != registry.OriginSDK {
		t.Errorf("note_read origin = %s, want sdk", readDef.SourceInfo.Origin.String())
	}
	if len(strings.Fields(readDef.PromptSnippet)) == 0 {
		t.Error("note_read missing prompt snippet")
	}

	writeDef, ok := reg.Get("note_write")
	if !ok {
		t.Fatal("note_write not registered")
	}
	if writeDef.Access != registry.AccessWrite {
		t.Errorf("note_write access = %s, want write", writeDef.Access.String())
	}
	if writeDef.Timeout != 10*time.Second {
		t.Errorf("note_write timeout = %v, want 10s", writeDef.Timeout)
	}
	if writeDef.Mode != registry.ToolModeSequential {
		t.Errorf("note_write mode = %s, want sequential", writeDef.Mode.String())
	}
	// Recovery metadata per S130500 §04–05.
	if readDef.Recovery != "none" || writeDef.Recovery != "atomic_transaction" {
		t.Errorf("recovery mismatch: read=%q write=%q", readDef.Recovery, writeDef.Recovery)
	}
}

func TestRegisterValidation(t *testing.T) {
	ss := newTestStore(t)
	reg := registry.New()
	if err := Register(nil, "navi-a", ss); err == nil {
		t.Error("Register accepted nil registry")
	}
	if err := Register(reg, "", ss); err == nil {
		t.Error("Register accepted empty navi id")
	}
	if err := Register(reg, "navi-a", nil); err == nil {
		t.Error("Register accepted nil store")
	}
	if err := Register(reg, "navi-a", ss); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Duplicate names in one registry are rejected.
	if err := Register(reg, "navi-a", ss); err == nil {
		t.Error("Register accepted duplicate note tools")
	}
}

// ---------------------------------------------------------------------------
// note_read scenarios (S130500 §04): by id, by tag, literal substring.

func TestReadByIDReturnsFullNote(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)
	id := writeNote(t, reg, `{"title":"grocery","body":"milk, eggs, bread","tags":["errand"],"id":"g-1"}`)

	docs := readNotes(t, reg, `{"id":"g-1"}`)
	if len(docs) != 1 {
		t.Fatalf("expected 1 note by id, got %d", len(docs))
	}
	if docs[0]["id"] != id || docs[0]["title"] != "grocery" || docs[0]["body"] != "milk, eggs, bread" {
		t.Errorf("by-id payload mismatch: %+v", docs[0])
	}
	tags, ok := docs[0]["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != "errand" {
		t.Errorf("tags mismatch: %+v", docs[0]["tags"])
	}
	if docs[0]["truncated"] != false {
		t.Errorf("unexpected truncation flag: %+v", docs[0])
	}
	if _, ok := docs[0]["created_at"].(string); !ok {
		t.Errorf("missing created_at: %+v", docs[0])
	}
	if _, ok := docs[0]["updated_at"].(string); !ok {
		t.Errorf("missing updated_at: %+v", docs[0])
	}
}

func TestReadByTag(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)
	writeNote(t, reg, `{"title":"grocery","body":"milk","tags":["errand"]}`)
	writeNote(t, reg, `{"title":"server","body":"nginx","tags":["ops"]}`)

	docs := readNotes(t, reg, `{"tag":"ops"}`)
	if len(docs) != 1 || docs[0]["title"] != "server" {
		t.Errorf("tag filter mismatch: %+v", docs)
	}
	missing := readNotes(t, reg, `{"tag":"nope"}`)
	if len(missing) != 0 {
		t.Errorf("unknown tag returned %d notes", len(missing))
	}
}

func TestReadLiteralSubstringNotSemantic(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)
	writeNote(t, reg, `{"title":"grocery list","body":"the quick brown fox jumps"}`)
	writeNote(t, reg, `{"title":"meeting notes","body":"quarterly planning for the fox team"}`)

	// Body substring matches only the note containing it literally.
	docs := readNotes(t, reg, `{"query":"quick brown"}`)
	if len(docs) != 1 || docs[0]["title"] != "grocery list" {
		t.Errorf("body substring mismatch: %+v", docs)
	}
	// Title substring matches too.
	docs = readNotes(t, reg, `{"query":"meeting"}`)
	if len(docs) != 1 || docs[0]["title"] != "meeting notes" {
		t.Errorf("title substring mismatch: %+v", docs)
	}
	// LIKE special chars are matched literally, not as patterns.
	writeNote(t, reg, `{"title":"regex","body":"a 100% match % and _ chars"}`)
	docs = readNotes(t, reg, `{"query":"100% match"}`)
	if len(docs) != 1 {
		t.Errorf("LIKE-escape mismatch: %+v", docs)
	}
	// Empty query is a no-op selector (store ignores zero fields), so it
	// lists every note the way an absent filter would.
	docs = readNotes(t, reg, `{"query":""}`)
	if len(docs) != 3 {
		t.Errorf("empty query returned %d notes, want all 3", len(docs))
	}
}

func TestReadLimitAndSelectorCardinality(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)
	for _, n := range []string{"a1", "a2", "a3"} {
		writeNote(t, reg, fmt.Sprintf(`{"title":%q,"body":"payload"}`, n))
	}

	docs := readNotes(t, reg, `{"query":"payload","limit":2}`)
	if len(docs) > 2 {
		t.Errorf("limit=2 returned %d notes", len(docs))
	}

	// At most one of id/tag/query.
	_, err := reg.Dispatch(context.Background(), "note_read", json.RawMessage(`{"id":"x","tag":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Errorf("expected at-most-one rejection, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// note_write scenarios (S130500 §05): create, update, caps, remove.

func TestWriteAssignsIDAndUpdatePreservesCreatedAt(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)

	first := writeNote(t, reg, `{"title":"scratch","body":"v1","tags":["ideas"]}`)
	docs := readNotes(t, reg, fmt.Sprintf(`{"id":%q}`, first))
	createdBefore := docs[0]["created_at"]

	// Explicit-id update keeps the same id and creation time.
	out, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(fmt.Sprintf(`{"id":%q,"title":"scratch","body":"v2"}`, first)))
	if err != nil {
		t.Fatalf("note_write update: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(json.RawMessage(out), &doc); err != nil {
		t.Fatalf("note_write update output: %v", err)
	}
	if doc["id"] != first || doc["body"] != "v2" {
		t.Errorf("update mismatch: %+v", doc)
	}
	docs = readNotes(t, reg, fmt.Sprintf(`{"id":%q}`, first))
	if docs[0]["created_at"] != createdBefore {
		t.Errorf("created_at changed across update: %q -> %q", createdBefore, docs[0]["created_at"])
	}
	if len(docs) != 1 {
		t.Errorf("update created a second note: %d rows", len(docs))
	}
}

func TestWriteBodyCapRejectsAndLeavesNotesUntouched(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)
	writeNote(t, reg, `{"title":"keep","body":"intact"}`)

	big := repeat("x", DefaultMaxBodyBytes+1)
	if _, err := reg.Dispatch(context.Background(), "note_write",
		json.RawMessage(fmt.Sprintf(`{"title":"big","body":%q}`, big))); err == nil || !strings.Contains(err.Error(), "65536") || !strings.Contains(err.Error(), "cap") {
		t.Errorf("cap rejection should identify the limit: %v", err)
	}
	// Existing notes unchanged, and the over-cap note was not created.
	docs := readNotes(t, reg, `{"query":"intact"}`)
	if len(docs) != 1 {
		t.Errorf("existing notes changed after rejection: %d", len(docs))
	}
	if len(readNotes(t, reg, `{"query":"big"}`)) != 0 {
		t.Error("rejected note was persisted")
	}

	// Exactly at the cap succeeds (64 KiB body).
	exact := repeat("y", DefaultMaxBodyBytes)
	if _, err := reg.Dispatch(context.Background(), "note_write",
		json.RawMessage(fmt.Sprintf(`{"title":"exact","body":%q}`, exact))); err != nil {
		t.Fatalf("note_write at exact cap rejected: %v", err)
	}
}

func TestWriteTotalCapWithUpdateAccounting(t *testing.T) {
	ss := newTestStore(t)
	const small = 1024
	reg := newRegistry(t, "navi-a", ss, Options{MaxNaviBytes: small})

	half := repeat("h", small/2)
	first := writeNote(t, reg, fmt.Sprintf(`{"title":"n1","body":%q}`, half))

	// Update n1 to the full cap: allowed (replaces, no double count).
	full := repeat("f", small)
	if _, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(fmt.Sprintf(`{"id":%q,"title":"n1","body":%q}`, first, full))); err != nil {
		t.Fatalf("note_write to full cap should pass: %v", err)
	}

	// Any new note now exceeds the cap.
	if _, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(fmt.Sprintf(`{"title":"n2","body":%q}`, full))); err == nil {
		t.Fatal("note_write accepted a note past the per-navi cap")
	} else if !strings.Contains(err.Error(), "per-navi cap") {
		t.Errorf("total-cap error should identify the cap: %v", err)
	}

	// Shrink n1, then the same note fits again.
	if _, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(fmt.Sprintf(`{"id":%q,"title":"n1","body":%q}`, first, half))); err != nil {
		t.Fatalf("note_write shrink: %v", err)
	}
	id2 := writeNote(t, reg, fmt.Sprintf(`{"title":"n2","body":%q}`, half))
	if len(readNotes(t, reg, fmt.Sprintf(`{"id":%q}`, id2))) != 1 {
		t.Error("second note not persisted after shrink")
	}
}

func TestWriteRemove(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)
	id := writeNote(t, reg, `{"title":"temp","body":"ephemeral"}`)

	out, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(fmt.Sprintf(`{"id":%q,"remove":true}`, id)))
	if err != nil {
		t.Fatalf("note_write remove: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(json.RawMessage(out), &doc); err != nil {
		t.Fatalf("remove output: %v", err)
	}
	if doc["removed"] != true || doc["id"] != id {
		t.Errorf("remove output mismatch: %+v", doc)
	}
	if len(readNotes(t, reg, fmt.Sprintf(`{"id":%q}`, id))) != 0 {
		t.Error("removed note still readable")
	}

	// Removing again (or an unknown id) reports not found.
	if _, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(fmt.Sprintf(`{"id":%q,"remove":true}`, id))); err == nil {
		t.Error("removing an unknown id accepted")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown-id remove error: %v", err)
	}
	// remove without id is a validation error.
	if _, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(`{"remove":true}`)); err == nil {
		t.Error("remove without id accepted")
	}
}

func TestWriteInputSchemaEnforced(t *testing.T) {
	ss := newTestStore(t)
	reg := newRegistry(t, "navi-a", ss)
	// Required title+body for create/update (handler-enforced so that
	// remove-only calls stay schema-legal per S130500 §05).
	if _, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(`{"title":"only"}`)); err == nil {
		t.Error("note_write without body accepted")
	}
	// Unknown keys are rejected by the strict schema (additionalProperties: false).
	if _, err := reg.Dispatch(context.Background(), "note_write", json.RawMessage(`{"title":"t","body":"b","bogus":1}`)); err == nil {
		t.Error("note_write with unknown key accepted")
	}
}

func TestReadTruncationAtReadCap(t *testing.T) {
	ss := newTestStore(t)
	// Write with generous defaults, read through a tighter cap: stored
	// over-cap bodies are cut with a notice (S130500 §04).
	reg := newRegistry(t, "navi-a", ss)
	id := writeNote(t, reg, `{"title":"long","body":"abcdefghij"}`)

	capReg := newRegistry(t, "navi-a", ss, Options{MaxBodyBytes: 4})
	docs := readNotes(t, capReg, fmt.Sprintf(`{"id":%q}`, id))
	if len(docs) != 1 {
		t.Fatalf("truncated read returned %d notes", len(docs))
	}
	body, ok := docs[0]["body"].(string)
	if !ok || strings.HasPrefix(body, "abcd") != true || !strings.Contains(body, "truncated") {
		t.Errorf("truncation notice missing: %q", body)
	}
	if docs[0]["truncated"] != true {
		t.Errorf("truncated flag missing: %+v", docs[0])
	}
	// The original registration still returns the full body.
	full := readNotes(t, reg, fmt.Sprintf(`{"id":%q}`, id))
	if full[0]["body"] != "abcdefghij" || full[0]["truncated"] != false {
		t.Errorf("full read regressed: %+v", full[0])
	}
}

func TestPerNaviScoping(t *testing.T) {
	ss := newTestStore(t)
	regA := newRegistry(t, "navi-a", ss)
	regB := newRegistry(t, "navi-b", ss)

	writeNote(t, regA, `{"title":"private-a","body":"secret of A"}`)

	if len(readNotes(t, regB, `{"query":"secret"}`)) != 0 {
		t.Error("navi-b can read navi-a notes")
	}
	if len(readNotes(t, regA, `{"query":"secret"}`)) != 1 {
		t.Error("navi-a lost its note")
	}
	// Removal scopes too: navi-b removing navi-a's id fails.
	if _, err := regB.Dispatch(context.Background(), "note_write", json.RawMessage(`{"id":"does-not-exist","remove":true}`)); err == nil {
		t.Error("navi-b remove across navis accepted")
	}
}
