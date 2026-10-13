package huly

import (
	"context"
	"strings"
	"testing"
)

// These tests pin the two shapes that are not ordinary attached documents: a
// label is a workspace TagElement plus a TagReference in the issue's `labels`
// collection, and a relation is an entry pushed into (or pulled from) an
// issue's `blockedBy` / `relations` array.

const (
	testIssueID   = "6acd168268c57585aac3fa3f"
	testIssue2ID  = "6acd168268c57585aac3fa40"
	testTagID     = "6b0000000000000000000001"
	testTagRefID  = "6b0000000000000000000002"
	testIssue2Ref = "NAVI-409"
)

// withIssues adds issues to the fake tracker under its lock, so a test can name
// a second issue without racing the server goroutine.
func withIssues(f *fakeHuly, issues ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues = append(f.issues, issues...)
}

// txsOf returns the recorded transactions under the lock.
func txsOf(f *fakeHuly) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.txs...)
}

// secondIssue is a plain second issue in the same project.
func secondIssue() map[string]any {
	return map[string]any{
		"_id": testIssue2ID, "identifier": testIssue2Ref, "title": "another issue",
		"space": "space-1", "status": "tracker:status:Backlog", "blockedBy": []any{},
	}
}

// TestIssueLabelAddsDefinitionAndReference pins the label write: a plain
// TagElement in the workspace space, then a TagReference attached to the
// issue's labels collection.
func TestIssueLabelAddsDefinitionAndReference(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueLabel(context.Background(), map[string]any{"issue": "NAVI-408", "label": "security"})
	if err != nil {
		t.Fatalf("issueLabel: %v", err)
	}
	if !strings.Contains(out, `"added":true`) {
		t.Errorf("result = %s, want added", out)
	}

	txs := txsOf(f)
	if len(txs) != 2 {
		t.Fatalf("posted %d transactions, want a definition and a reference", len(txs))
	}
	def := txs[0]
	if def["_class"] != classTxCreateDoc || def["objectClass"] != classTagElement || def["objectSpace"] != spaceWorkspace {
		t.Fatalf("definition tx = %v, want a TagElement in the workspace space", def)
	}
	// A definition is a plain document: no collection and no parent.
	if _, ok := def["collection"]; ok {
		t.Errorf("definition carries a collection: %v", def["collection"])
	}
	if _, ok := def["attachedTo"]; ok {
		t.Errorf("definition carries a parent: %v", def["attachedTo"])
	}
	defAttrs, _ := def["attributes"].(map[string]any)
	if defAttrs["title"] != "security" || defAttrs["targetClass"] != classIssue ||
		defAttrs["category"] != "tracker:category:Other" {
		t.Errorf("definition attributes = %v", defAttrs)
	}

	ref := txs[1]
	if ref["_class"] != classTxCreateDoc || ref["objectClass"] != classTagReference || ref["objectSpace"] != "space-1" {
		t.Fatalf("reference tx = %v, want a TagReference in the project space", ref)
	}
	if ref["collection"] != collectionLabels || ref["attachedTo"] != testIssueID || ref["attachedToClass"] != classIssue {
		t.Errorf("reference attachment = %v/%v/%v, want the issue's labels collection",
			ref["collection"], ref["attachedTo"], ref["attachedToClass"])
	}
	if ref["modifiedBy"] != "social-1" || ref["createdBy"] != "social-1" {
		t.Errorf("reference author = %v/%v, want the primary social id", ref["modifiedBy"], ref["createdBy"])
	}
	refAttrs, _ := ref["attributes"].(map[string]any)
	if refAttrs["title"] != "security" || refAttrs["tag"] != def["objectId"] {
		t.Errorf("reference attributes = %v, want the definition's id %v", refAttrs, def["objectId"])
	}
}

// TestIssueLabelReusesAnExistingDefinition pins case-insensitive reuse: a label
// the workspace already has is not created twice.
func TestIssueLabelReusesAnExistingDefinition(t *testing.T) {
	f := newFakeHuly(t)
	f.tags = []map[string]any{{"_id": testTagID, "title": "Security", "targetClass": classIssue, "color": 3}}
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	if _, err := client.issueLabel(context.Background(), map[string]any{"issue": "NAVI-408", "label": "security"}); err != nil {
		t.Fatalf("issueLabel: %v", err)
	}
	txs := txsOf(f)
	if len(txs) != 1 {
		t.Fatalf("posted %d transactions, want only the reference", len(txs))
	}
	attrs, _ := txs[0]["attributes"].(map[string]any)
	if attrs["tag"] != testTagID || attrs["title"] != "Security" {
		t.Errorf("reference attributes = %v, want the existing definition %s", attrs, testTagID)
	}
}

// TestIssueLabelAddIsIdempotent pins the skip: an issue that already carries
// the label is not given a second reference.
func TestIssueLabelAddIsIdempotent(t *testing.T) {
	f := newFakeHuly(t)
	f.tags = []map[string]any{{"_id": testTagID, "title": "Security", "targetClass": classIssue, "color": 3}}
	f.tagRefs = []map[string]any{{"_id": testTagRefID, "tag": testTagID, "title": "Security",
		"attachedTo": testIssueID, "attachedToClass": classIssue, "space": "space-1", "collection": collectionLabels}}
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueLabel(context.Background(), map[string]any{"issue": "NAVI-408", "label": "SECURITY"})
	if err != nil {
		t.Fatalf("issueLabel: %v", err)
	}
	if !strings.Contains(out, `"added":false`) {
		t.Errorf("result = %s, want added:false", out)
	}
	if txs := txsOf(f); len(txs) != 0 {
		t.Errorf("posted %d transactions for a label already present, want 0", len(txs))
	}
}

// TestIssueLabelRemoveDetaches pins the remove path: a remove transaction on
// the TagReference, not on the definition.
func TestIssueLabelRemoveDetaches(t *testing.T) {
	f := newFakeHuly(t)
	f.tagRefs = []map[string]any{{"_id": testTagRefID, "tag": testTagID, "title": "Security",
		"attachedTo": testIssueID, "attachedToClass": classIssue, "space": "space-1", "collection": collectionLabels}}
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueLabel(context.Background(), map[string]any{
		"issue": "NAVI-408", "label": "security", "action": "remove",
	})
	if err != nil {
		t.Fatalf("issueLabel: %v", err)
	}
	if !strings.Contains(out, `"removed":true`) {
		t.Errorf("result = %s, want removed:true", out)
	}
	txs := txsOf(f)
	if len(txs) != 1 {
		t.Fatalf("posted %d transactions, want one remove", len(txs))
	}
	tx := txs[0]
	if tx["_class"] != classTxRemoveDoc || tx["objectId"] != testTagRefID ||
		tx["objectClass"] != classTagReference || tx["objectSpace"] != "space-1" {
		t.Errorf("remove tx = %v, want a TagReference remove in the project space", tx)
	}
}

// TestIssueLabelRemoveAbsentIsANoOp pins that removing a label the issue does
// not carry is not an error and posts nothing.
func TestIssueLabelRemoveAbsentIsANoOp(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueLabel(context.Background(), map[string]any{
		"issue": "NAVI-408", "label": "absent", "action": "remove",
	})
	if err != nil {
		t.Fatalf("issueLabel: %v", err)
	}
	if !strings.Contains(out, `"removed":false`) {
		t.Errorf("result = %s, want removed:false", out)
	}
	if txs := txsOf(f); len(txs) != 0 {
		t.Errorf("posted %d transactions, want 0", len(txs))
	}
}

// TestIssueLabelRefusesAnUnknownAction pins the fail-closed action.
func TestIssueLabelRefusesAnUnknownAction(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()
	if _, err := client.issueLabel(context.Background(), map[string]any{
		"issue": "NAVI-408", "label": "x", "action": "toggle",
	}); err == nil {
		t.Fatal("an unknown action was accepted, want a refusal")
	}
}

// TestIssueRelationBlocksPushesIntoTheTarget pins `blocks`: the SOURCE goes
// into the TARGET's blockedBy.
func TestIssueRelationBlocksPushesIntoTheTarget(t *testing.T) {
	f := newFakeHuly(t)
	withIssues(f, secondIssue())
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueRelation(context.Background(), map[string]any{
		"issue": "NAVI-408", "target": testIssue2Ref, "type": "blocks",
	})
	if err != nil {
		t.Fatalf("issueRelation: %v", err)
	}
	if !strings.Contains(out, `"changed":true`) {
		t.Errorf("result = %s, want changed:true", out)
	}
	txs := txsOf(f)
	if len(txs) != 1 {
		t.Fatalf("posted %d transactions, want one", len(txs))
	}
	tx := txs[0]
	if tx["_class"] != classTxUpdateDoc || tx["objectId"] != testIssue2ID ||
		tx["objectClass"] != classIssue || tx["objectSpace"] != "space-1" {
		t.Fatalf("tx = %v, want an update on the target issue", tx)
	}
	entry := pushedEntry(t, tx, fieldBlockedBy)
	if entry["_id"] != testIssueID || entry["_class"] != classIssue {
		t.Errorf("entry = %v, want the source issue reference", entry)
	}
}

// TestIssueRelationIsBlockedByPushesIntoTheSource pins the mirror: the TARGET
// goes into the SOURCE's blockedBy.
func TestIssueRelationIsBlockedByPushesIntoTheSource(t *testing.T) {
	f := newFakeHuly(t)
	withIssues(f, secondIssue())
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	if _, err := client.issueRelation(context.Background(), map[string]any{
		"issue": "NAVI-408", "target": testIssue2Ref, "type": "is-blocked-by",
	}); err != nil {
		t.Fatalf("issueRelation: %v", err)
	}
	txs := txsOf(f)
	if len(txs) != 1 {
		t.Fatalf("posted %d transactions, want one", len(txs))
	}
	if txs[0]["objectId"] != testIssueID {
		t.Errorf("updated %v, want the source issue", txs[0]["objectId"])
	}
	entry := pushedEntry(t, txs[0], fieldBlockedBy)
	if entry["_id"] != testIssue2ID {
		t.Errorf("entry = %v, want the target issue reference", entry)
	}
}

// TestIssueRelationRelatesToUpdatesBothSides pins the bidirectional link.
func TestIssueRelationRelatesToUpdatesBothSides(t *testing.T) {
	f := newFakeHuly(t)
	withIssues(f, secondIssue())
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	if _, err := client.issueRelation(context.Background(), map[string]any{
		"issue": "NAVI-408", "target": testIssue2Ref, "type": "relates-to",
	}); err != nil {
		t.Fatalf("issueRelation: %v", err)
	}
	txs := txsOf(f)
	if len(txs) != 2 {
		t.Fatalf("posted %d transactions, want one per side", len(txs))
	}
	byObject := map[string]map[string]any{}
	for _, tx := range txs {
		id, _ := tx["objectId"].(string)
		byObject[id] = tx
	}
	forward, ok := byObject[testIssueID]
	if !ok {
		t.Fatalf("no update on the source issue: %v", txs)
	}
	if entry := pushedEntry(t, forward, fieldRelations); entry["_id"] != testIssue2ID {
		t.Errorf("source relation entry = %v, want the target", entry)
	}
	reverse, ok := byObject[testIssue2ID]
	if !ok {
		t.Fatalf("no update on the target issue: %v", txs)
	}
	if entry := pushedEntry(t, reverse, fieldRelations); entry["_id"] != testIssueID {
		t.Errorf("target relation entry = %v, want the source", entry)
	}
}

// TestIssueRelationSkipsAnExistingLink pins the no-op: a link already present
// posts nothing.
func TestIssueRelationSkipsAnExistingLink(t *testing.T) {
	f := newFakeHuly(t)
	issue := secondIssue()
	issue["blockedBy"] = []any{map[string]any{"_id": testIssueID, "_class": classIssue}}
	withIssues(f, issue)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueRelation(context.Background(), map[string]any{
		"issue": "NAVI-408", "target": testIssue2Ref, "type": "blocks",
	})
	if err != nil {
		t.Fatalf("issueRelation: %v", err)
	}
	if !strings.Contains(out, `"changed":false`) {
		t.Errorf("result = %s, want changed:false", out)
	}
	if txs := txsOf(f); len(txs) != 0 {
		t.Errorf("posted %d transactions for an existing link, want 0", len(txs))
	}
}

// TestIssueRelationRemovePullsById pins the remove shape: Huly's own client
// pulls by `_id` alone.
func TestIssueRelationRemovePullsById(t *testing.T) {
	f := newFakeHuly(t)
	issue := secondIssue()
	issue["blockedBy"] = []any{map[string]any{"_id": testIssueID, "_class": classIssue}}
	withIssues(f, issue)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueRelation(context.Background(), map[string]any{
		"issue": "NAVI-408", "target": testIssue2Ref, "type": "blocks", "action": "remove",
	})
	if err != nil {
		t.Fatalf("issueRelation: %v", err)
	}
	if !strings.Contains(out, `"changed":true`) {
		t.Errorf("result = %s, want changed:true", out)
	}
	txs := txsOf(f)
	if len(txs) != 1 {
		t.Fatalf("posted %d transactions, want one", len(txs))
	}
	ops, _ := txs[0]["operations"].(map[string]any)
	pull, ok := ops["$pull"].(map[string]any)
	if !ok {
		t.Fatalf("operations = %v, want a $pull", ops)
	}
	entry, _ := pull[fieldBlockedBy].(map[string]any)
	if entry["_id"] != testIssueID {
		t.Errorf("pull entry = %v, want the source id", entry)
	}
	if _, ok := entry["_class"]; ok {
		t.Errorf("pull entry carries a class: %v", entry)
	}
}

// TestIssueRelationRemoveAbsentIsANoOp pins that removing a link that is not
// there posts nothing.
func TestIssueRelationRemoveAbsentIsANoOp(t *testing.T) {
	f := newFakeHuly(t)
	withIssues(f, secondIssue())
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueRelation(context.Background(), map[string]any{
		"issue": "NAVI-408", "target": testIssue2Ref, "type": "blocks", "action": "remove",
	})
	if err != nil {
		t.Fatalf("issueRelation: %v", err)
	}
	if !strings.Contains(out, `"changed":false`) {
		t.Errorf("result = %s, want changed:false", out)
	}
	if txs := txsOf(f); len(txs) != 0 {
		t.Errorf("posted %d transactions, want 0", len(txs))
	}
}

// TestIssueRelationRefusesBadInput pins the guards: an unknown type, a missing
// target, and a self-link are all refused before any transaction.
func TestIssueRelationRefusesBadInput(t *testing.T) {
	f := newFakeHuly(t)
	withIssues(f, secondIssue())
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	cases := []map[string]any{
		{"issue": "NAVI-408", "target": testIssue2Ref, "type": "duplicates"},
		{"issue": "NAVI-408", "target": "", "type": "blocks"},
		{"issue": "NAVI-408", "target": "NAVI-408", "type": "blocks"},
	}
	for _, args := range cases {
		if _, err := client.issueRelation(context.Background(), args); err == nil {
			t.Errorf("issueRelation(%v) succeeded, want a refusal", args)
		}
	}
	if txs := txsOf(f); len(txs) != 0 {
		t.Errorf("posted %d transactions for refused input, want 0", len(txs))
	}
}

// pushedEntry reads the entry a `$push` transaction records under field.
func pushedEntry(t *testing.T, tx map[string]any, field string) map[string]any {
	t.Helper()
	ops, _ := tx["operations"].(map[string]any)
	push, ok := ops["$push"].(map[string]any)
	if !ok {
		t.Fatalf("operations = %v, want a $push", ops)
	}
	entry, ok := push[field].(map[string]any)
	if !ok {
		t.Fatalf("push = %v, want an entry under %s", push, field)
	}
	return entry
}
