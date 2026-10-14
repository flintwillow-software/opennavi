package huly

// Live smoke test (NAVI-418). It is SKIPPED unless HULY_LIVE_SMOKE is set, and
// it writes to the real workspace: it creates two scratch issues, exercises the
// egress paths, and removes everything it created. Run it by hand after a wire
// shape changes — the fake-server tests cannot catch a wrong shape, which is how
// two live bugs got through: find-all's exclusion projection (the queried field
// is absent from the response) and the update tool's `$set` (Huly's assignment
// form is a plain field map, not an operator).
//
//	HULY_LIVE_SMOKE=1 HULY_TOKEN=... go test -run TestLiveSmoke -v ./connector/huly/
//
// It needs a token whose account can create and remove issues in NAVI, and it
// advances the project's sequence (Huly tolerates the gap).

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
)

const (
	smokeURL = "https://huly.flintwillow.software"
	smokeWS  = "flintwillowsoftware"
)

func smokeClient(t *testing.T) *Client {
	t.Helper()
	c := New(Config{URL: smokeURL, Workspace: smokeWS, Credential: "HULY_TOKEN"})
	if err := c.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cl, err := c.live.Get()
	if err != nil {
		t.Fatalf("live client: %v", err)
	}
	return cl
}

func smokeDecode(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return m
}

// smokeRemove removes one document, so the test leaves the workspace as it
// found it.
func smokeRemove(t *testing.T, c *Client, class, space, id string) {
	t.Helper()
	s, err := c.session(context.Background())
	if err != nil {
		t.Errorf("session: %v", err)
		return
	}
	tx := map[string]any{
		"_id": newID(), "_class": classTxRemoveDoc, "space": spaceTx,
		"objectId": id, "objectClass": class, "objectSpace": space,
		"modifiedOn": time.Now().UnixMilli(), "modifiedBy": s.socialID,
	}
	if err := c.tx(context.Background(), tx); err != nil {
		t.Errorf("remove %s %s: %v", class, id, err)
	}
}

func TestLiveSmoke(t *testing.T) {
	if os.Getenv("HULY_LIVE_SMOKE") == "" {
		t.Skip("set HULY_LIVE_SMOKE=1 to run the live Huly smoke test (it writes to the real workspace)")
	}
	ctx := context.Background()
	c := smokeClient(t)

	// Two scratch issues; their ids are the cleanup handles.
	mk := func(title string) (id, space, identifier string) {
		out, err := c.issueCreate(ctx, map[string]any{"project": "NAVI", "title": title, "status": "Backlog"})
		if err != nil {
			t.Fatalf("issueCreate %q: %v", title, err)
		}
		res := smokeDecode(t, out)
		id, _ = res["id"].(string)
		identifier, _ = res["identifier"].(string)
		if !strings.HasPrefix(identifier, "NAVI-") {
			t.Fatalf("identifier = %q, want a NAVI- prefix (find-all lost the project identifier)", identifier)
		}
		issue, err := c.findIssue(ctx, id)
		if err != nil {
			t.Fatalf("findIssue %s: %v", id, err)
		}
		space = stringField(issue, "space")
		t.Logf("created %s (%s) space=%s", identifier, id, space)
		return id, space, identifier
	}
	idA, spaceA, identA := mk("smoke: labels/relations A — delete me")
	defer func() {
		smokeRemove(t, c, classIssue, spaceA, idA)
		t.Logf("removed %s", identA)
	}()
	idB, spaceB, identB := mk("smoke: labels/relations B — delete me")
	defer func() {
		smokeRemove(t, c, classIssue, spaceB, idB)
		t.Logf("removed %s", identB)
	}()

	// ── comment ────────────────────────────────────────────────────────
	commentOut, err := c.issueComment(ctx, map[string]any{"issue": idA, "body": "smoke comment — delete me"})
	if err != nil {
		t.Fatalf("issueComment: %v", err)
	}
	commentID, _ := smokeDecode(t, commentOut)["id"].(string)
	if commentID == "" {
		t.Fatal("comment returned no id")
	}
	defer smokeRemove(t, c, classChatMessage, spaceA, commentID)
	comments, err := c.findAll(ctx, classChatMessage, map[string]any{"attachedTo": idA}, map[string]any{"limit": 50})
	if err != nil {
		t.Fatalf("find comments: %v", err)
	}
	if len(comments) != 1 || stringField(comments[0], "_id") != commentID {
		t.Fatalf("comments = %v, want the posted comment attached to %s", comments, identA)
	}
	t.Logf("comment %s attached", commentID)

	// ── update ─────────────────────────────────────────────────────────
	if _, err := c.issueUpdate(ctx, map[string]any{"issue": idA, "priority": "low"}); err != nil {
		t.Fatalf("issueUpdate: %v", err)
	}
	fresh, err := c.findIssue(ctx, idA)
	if err != nil {
		t.Fatalf("findIssue after update: %v", err)
	}
	if intField(fresh, "priority") != 4 {
		t.Fatalf("priority after update = %v, want 4 (low)", fresh["priority"])
	}
	t.Log("update priority=low applied")

	// ── description (the collaborator service) ─────────────────────────
	if _, err := c.issueDescriptionSet(ctx, map[string]any{
		"issue": idA, "text": "smoke description line one\nsmoke description line two",
	}); err != nil {
		t.Fatalf("issueDescriptionSet: %v", err)
	}
	descOut, err := c.issueDescription(ctx, map[string]any{"issue": idA})
	if err != nil {
		t.Fatalf("issueDescription: %v", err)
	}
	desc, _ := smokeDecode(t, descOut)["description"].(string)
	if !strings.Contains(desc, "line one") || !strings.Contains(desc, "line two") {
		t.Fatalf("description = %q, want both lines", desc)
	}
	// A second write updates the document in place.
	if _, err := c.issueDescriptionSet(ctx, map[string]any{
		"issue": idA, "text": "smoke description replaced",
	}); err != nil {
		t.Fatalf("issueDescriptionSet (update): %v", err)
	}
	descOut, err = c.issueDescription(ctx, map[string]any{"issue": idA})
	if err != nil {
		t.Fatalf("issueDescription (after update): %v", err)
	}
	desc, _ = smokeDecode(t, descOut)["description"].(string)
	if strings.TrimSpace(desc) != "smoke description replaced" {
		t.Fatalf("description after update = %q, want the replacement", desc)
	}
	t.Log("description write/read/update applied")

	// ── label ──────────────────────────────────────────────────────────
	labelOut, err := c.issueLabel(ctx, map[string]any{"issue": idA, "label": "smoke-label"})
	if err != nil {
		t.Fatalf("issueLabel add: %v", err)
	}
	if !smokeDecode(t, labelOut)["added"].(bool) {
		t.Fatalf("label not added: %s", labelOut)
	}
	// Clean up the definition, then verify and clean up the assignment.
	elements, err := c.findAll(ctx, classTagElement, map[string]any{"targetClass": classIssue}, map[string]any{"limit": 200})
	if err != nil {
		t.Fatalf("find TagElement: %v", err)
	}
	for _, el := range elements {
		if stringField(el, "title") == "smoke-label" {
			tagID := stringField(el, "_id")
			defer smokeRemove(t, c, classTagElement, spaceWorkspace, tagID)
			t.Logf("label definition %s created", tagID)
		}
	}
	refs, err := c.findAll(ctx, classTagReference, map[string]any{"attachedTo": idA}, map[string]any{"limit": 50})
	if err != nil {
		t.Fatalf("find TagReference: %v", err)
	}
	if len(refs) != 1 || stringField(refs[0], "title") != "smoke-label" {
		t.Fatalf("label refs = %v, want one smoke-label", refs)
	}
	defer smokeRemove(t, c, classTagReference, spaceA, stringField(refs[0], "_id"))
	t.Logf("label reference %s attached", stringField(refs[0], "_id"))

	// Adding it again is a no-op.
	againOut, err := c.issueLabel(ctx, map[string]any{"issue": idA, "label": "smoke-label"})
	if err != nil {
		t.Fatalf("issueLabel re-add: %v", err)
	}
	if smokeDecode(t, againOut)["added"].(bool) {
		t.Error("re-add reported added:true, want a no-op")
	}

	// ── relations ──────────────────────────────────────────────────────
	if _, err := c.issueRelation(ctx, map[string]any{"issue": idA, "target": idB, "type": "blocks"}); err != nil {
		t.Fatalf("issueRelation blocks: %v", err)
	}
	if _, err := c.issueRelation(ctx, map[string]any{"issue": idA, "target": idB, "type": "relates-to"}); err != nil {
		t.Fatalf("issueRelation relates-to: %v", err)
	}
	b, err := c.findIssue(ctx, idB)
	if err != nil {
		t.Fatalf("findIssue B: %v", err)
	}
	a, err := c.findIssue(ctx, idA)
	if err != nil {
		t.Fatalf("findIssue A: %v", err)
	}
	if !hasRelated(b, fieldBlockedBy, idA) {
		t.Fatalf("B.blockedBy = %v, want A", b[fieldBlockedBy])
	}
	if !hasRelated(a, fieldRelations, idB) || !hasRelated(b, fieldRelations, idA) {
		t.Fatalf("relates-to not bidirectional: A.relations=%v B.relations=%v", a[fieldRelations], b[fieldRelations])
	}
	t.Logf("relations ok: B.blockedBy=%v A.relations=%v B.relations=%v", b[fieldBlockedBy], a[fieldRelations], b[fieldRelations])

	// Removing a relation.
	removeOut, err := c.issueRelation(ctx, map[string]any{"issue": idA, "target": idB, "type": "blocks", "action": "remove"})
	if err != nil {
		t.Fatalf("issueRelation remove: %v", err)
	}
	if !smokeDecode(t, removeOut)["changed"].(bool) {
		t.Error("relation remove reported changed:false")
	}
	b2, err := c.findIssue(ctx, idB)
	if err != nil {
		t.Fatalf("findIssue B after remove: %v", err)
	}
	if hasRelated(b2, fieldBlockedBy, idA) {
		t.Fatalf("B.blockedBy = %v, want A removed", b2[fieldBlockedBy])
	}
	t.Log("relation remove applied")
}
