package huly

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
)

// This file is the label and relation egress tools (NAVI-418). They are the two
// parts of Huly's model that are NOT ordinary attached documents:
//
//   - A **label** is Huly's tags model. A `tags:class:TagElement` is the label
//     definition — a plain document in the workspace space, with no parent or
//     collection — and a `tags:class:TagReference` attached to the issue's
//     `labels` collection is the assignment. Adding a label a workspace has
//     never used creates the definition first.
//   - A **relation** is not a document at all: it is an entry
//     `{_id, _class}` pushed into an issue's `blockedBy` or `relations` array
//     by an update transaction. `blocks` pushes the source into the target's
//     `blockedBy`, `is-blocked-by` pushes the target into the source's, and
//     `relates-to` pushes each into the other's `relations`.
//
// Both tools are execute-level: they mutate an external system, like every
// egress write (ADR 0020).

const (
	// ToolIssueLabel adds or removes a label on an issue.
	ToolIssueLabel = "huly_issue_label"
	// ToolIssueRelation adds or removes a relation between two issues.
	ToolIssueRelation = "huly_issue_relation"
)

// Huly's tags model, its workspace space, and the relation array fields.
const (
	classTagElement   = "tags:class:TagElement"
	classTagReference = "tags:class:TagReference"
	classTagCategory  = "tags:class:TagCategory"
	classTxRemoveDoc  = "core:class:TxRemoveDoc"
	spaceWorkspace    = "core:space:Workspace"
	collectionLabels  = "labels"
	// categoryOther is the workspace's default tag category for an issue, used
	// when the model does not answer with one (it does: `tracker:category:Other`).
	categoryOther  = "tracker:category:Other"
	fieldBlockedBy = "blockedBy"
	fieldRelations = "relations"
)

// ── tool definitions ───────────────────────────────────────────────────

func issueLabelDef() tool.Def {
	return tool.Def{
		Name: ToolIssueLabel,
		Description: "Adds or removes a label on a Huly issue, addressed by identifier (NAVI-408) or id. " +
			"A label the workspace has never used is created; the credential is never an argument.",
		InputSchema:  parseSchema(issueLabelSchema),
		OutputFormat: "json",
		Access:       tool.AccessExecute,
		Timeout:      20 * time.Second,
		Recovery:     "capture_response",
		Mode:         tool.ToolModeSequential,
		Tags:         []string{"huly", "egress", "issue", "label"},
		Version:      "1.0.0",
		PromptSnippet: "huly_issue_label: add or remove a label on a Huly issue " +
			"(a missing label is created)",
		PromptGuidelines: []string{
			"A label is a title, matched case-insensitively; an existing label is reused rather than duplicated.",
			"Adding a label the issue already carries is a no-op, not an error.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func issueRelationDef() tool.Def {
	return tool.Def{
		Name: ToolIssueRelation,
		Description: "Adds or removes a relation between two Huly issues. 'blocks' makes this issue block the " +
			"target, 'is-blocked-by' makes the target block this issue, and 'relates-to' is a bidirectional link. " +
			"The target may be in another project; the credential is never an argument.",
		InputSchema:  parseSchema(issueRelationSchema),
		OutputFormat: "json",
		Access:       tool.AccessExecute,
		Timeout:      20 * time.Second,
		Recovery:     "capture_response",
		Mode:         tool.ToolModeSequential,
		Tags:         []string{"huly", "egress", "issue", "relation"},
		Version:      "1.0.0",
		PromptSnippet: "huly_issue_relation: link two Huly issues " +
			"(blocks / is-blocked-by / relates-to)",
		PromptGuidelines: []string{
			"Read the relation from the subject's point of view: 'issue blocks target', not the other way round.",
			"Adding a relation that already exists is a no-op, not an error.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

const issueLabelSchema = `{
	"type": "object",
	"properties": {
		"issue": {"type": "string", "description": "The issue identifier (NAVI-408) or its raw id."},
		"label": {"type": "string", "description": "The label title (a missing title creates the label), or a tag id."},
		"action": {"type": "string", "enum": ["add", "remove"], "description": "add (default) attaches the label; remove detaches it."}
	},
	"required": ["issue", "label"],
	"additionalProperties": false
}`

const issueRelationSchema = `{
	"type": "object",
	"properties": {
		"issue": {"type": "string", "description": "The issue the relation is stated from, by identifier or raw id."},
		"target": {"type": "string", "description": "The other issue, by identifier or raw id (may be in another project)."},
		"type": {"type": "string", "enum": ["blocks", "is-blocked-by", "relates-to"], "description": "blocks: this issue blocks the target; is-blocked-by: the target blocks this issue; relates-to: a bidirectional link."},
		"action": {"type": "string", "enum": ["add", "remove"], "description": "add (default) or remove."}
	},
	"required": ["issue", "target", "type"],
	"additionalProperties": false
}`

// ── label handler ──────────────────────────────────────────────────────

// issueLabel attaches or detaches a label. Adding resolves the label title to
// its definition (creating one when the workspace has never used it) and skips
// an assignment the issue already carries; removing detaches every matching
// assignment.
func (c *Client) issueLabel(ctx context.Context, args map[string]any) (string, error) {
	ref := strings.TrimSpace(stringArg(args, "issue"))
	label := strings.TrimSpace(stringArg(args, "label"))
	if ref == "" {
		return "", fmt.Errorf("huly: issue is required")
	}
	if label == "" {
		return "", fmt.Errorf("huly: label is required")
	}
	remove, err := actionOf(args)
	if err != nil {
		return "", err
	}
	issue, err := c.findIssue(ctx, ref)
	if err != nil {
		return "", err
	}
	if remove {
		removed, err := c.detachLabel(ctx, issue, label)
		if err != nil {
			return "", err
		}
		return encodeResult(map[string]any{
			"ok": true, "issue": stringField(issue, "identifier"),
			"label": label, "removed": removed,
		})
	}
	element, err := c.ensureTagElement(ctx, label)
	if err != nil {
		return "", err
	}
	refs, err := c.labelRefs(ctx, issue)
	if err != nil {
		return "", err
	}
	if labelAttached(refs, element) {
		return encodeResult(map[string]any{
			"ok": true, "issue": stringField(issue, "identifier"),
			"label": stringField(element, "title"), "added": false,
		})
	}
	s, err := c.session(ctx)
	if err != nil {
		return "", err
	}
	referenceID := newID()
	// A TagReference is an attached document: the definition is the workspace
	// document, and the assignment lives in the issue's `labels` collection.
	transaction := map[string]any{
		"_id": newID(), "_class": classTxCreateDoc, "space": spaceTx,
		"objectId": referenceID, "objectClass": classTagReference,
		"objectSpace": stringField(issue, "space"),
		"modifiedOn":  time.Now().UnixMilli(), "modifiedBy": s.socialID, "createdBy": s.socialID,
		"attributes": map[string]any{
			"title": stringField(element, "title"),
			"color": intField(element, "color"),
			"tag":   stringField(element, "_id"),
		},
		"collection":      collectionLabels,
		"attachedTo":      stringField(issue, "_id"),
		"attachedToClass": classIssue,
	}
	if err := c.tx(ctx, transaction); err != nil {
		return "", err
	}
	return encodeResult(map[string]any{
		"ok": true, "issue": stringField(issue, "identifier"),
		"label": stringField(element, "title"), "added": true, "id": referenceID,
	})
}

// ensureTagElement resolves a label title (or a tag id) to its definition,
// creating the definition when the workspace has never used the label.
func (c *Client) ensureTagElement(ctx context.Context, label string) (map[string]any, error) {
	if isRawID(label) {
		found, err := c.findAll(ctx, classTagElement, map[string]any{"_id": label}, map[string]any{"limit": 1})
		if err != nil {
			return nil, err
		}
		if len(found) > 0 {
			return found[0], nil
		}
	}
	elements, err := c.findAll(ctx, classTagElement,
		map[string]any{"targetClass": classIssue}, map[string]any{"limit": 200})
	if err != nil {
		return nil, err
	}
	for _, el := range elements {
		if strings.EqualFold(stringField(el, "title"), label) {
			return el, nil
		}
	}
	category, err := c.defaultTagCategory(ctx)
	if err != nil {
		return nil, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	tagID := newID()
	attributes := map[string]any{
		"title": label, "description": "", "targetClass": classIssue,
		"color": 0, "category": category,
	}
	// A label definition is a PLAIN document in the workspace space: no
	// collection and no parent.
	transaction := map[string]any{
		"_id": newID(), "_class": classTxCreateDoc, "space": spaceTx,
		"objectId": tagID, "objectClass": classTagElement,
		"objectSpace": spaceWorkspace,
		"modifiedOn":  time.Now().UnixMilli(), "modifiedBy": s.socialID, "createdBy": s.socialID,
		"attributes": attributes,
	}
	if err := c.tx(ctx, transaction); err != nil {
		return nil, err
	}
	return map[string]any{
		"_id": tagID, "title": label, "targetClass": classIssue,
		"color": 0, "category": category,
	}, nil
}

// defaultTagCategory returns the workspace's default tag category for an issue
// (`tracker:category:Other`), falling back to the constant when the model does
// not answer.
func (c *Client) defaultTagCategory(ctx context.Context) (string, error) {
	categories, err := c.findAll(ctx, classTagCategory,
		map[string]any{"targetClass": classIssue, "default": true},
		map[string]any{"limit": 1})
	if err != nil {
		return "", err
	}
	if len(categories) > 0 {
		if id := stringField(categories[0], "_id"); id != "" {
			return id, nil
		}
	}
	return categoryOther, nil
}

// labelRefs reads the label assignments the issue already carries.
func (c *Client) labelRefs(ctx context.Context, issue map[string]any) ([]map[string]any, error) {
	return c.findAll(ctx, classTagReference, map[string]any{
		"attachedTo":      stringField(issue, "_id"),
		"attachedToClass": classIssue,
		"space":           stringField(issue, "space"),
		"collection":      collectionLabels,
	}, map[string]any{"limit": 200})
}

// labelAttached reports whether an assignment for the definition is already
// present, matching by tag id or by title (case-insensitively).
func labelAttached(refs []map[string]any, element map[string]any) bool {
	id := stringField(element, "_id")
	title := strings.ToLower(strings.TrimSpace(stringField(element, "title")))
	for _, r := range refs {
		if id != "" && stringField(r, "tag") == id {
			return true
		}
		if title != "" && strings.ToLower(strings.TrimSpace(stringField(r, "title"))) == title {
			return true
		}
	}
	return false
}

// detachLabel removes every assignment matching the title or tag id. It
// reports whether anything was removed; removing a label the issue does not
// carry is a no-op, not an error.
func (c *Client) detachLabel(ctx context.Context, issue map[string]any, label string) (bool, error) {
	refs, err := c.labelRefs(ctx, issue)
	if err != nil {
		return false, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return false, err
	}
	lower := strings.ToLower(label)
	removed := false
	for _, r := range refs {
		title := strings.ToLower(strings.TrimSpace(stringField(r, "title")))
		if stringField(r, "tag") != label && title != lower {
			continue
		}
		transaction := map[string]any{
			"_id": newID(), "_class": classTxRemoveDoc, "space": spaceTx,
			"objectId": stringField(r, "_id"), "objectClass": classTagReference,
			"objectSpace": stringField(issue, "space"),
			"modifiedOn":  time.Now().UnixMilli(), "modifiedBy": s.socialID,
		}
		if err := c.tx(ctx, transaction); err != nil {
			return removed, err
		}
		removed = true
	}
	return removed, nil
}

// ── relation handler ───────────────────────────────────────────────────

// issueRelation adds or removes a relation. `blocks` records the source in the
// target's `blockedBy`; `is-blocked-by` records the target in the source's;
// `relates-to` records each in the other's `relations`.
func (c *Client) issueRelation(ctx context.Context, args map[string]any) (string, error) {
	ref := strings.TrimSpace(stringArg(args, "issue"))
	targetRef := strings.TrimSpace(stringArg(args, "target"))
	relType := strings.ToLower(strings.TrimSpace(stringArg(args, "type")))
	if ref == "" {
		return "", fmt.Errorf("huly: issue is required")
	}
	if targetRef == "" {
		return "", fmt.Errorf("huly: target is required")
	}
	switch relType {
	case "blocks", "is-blocked-by", "relates-to":
	default:
		return "", fmt.Errorf("huly: unknown relation type %q (want blocks, is-blocked-by, or relates-to)", relType)
	}
	remove, err := actionOf(args)
	if err != nil {
		return "", err
	}
	source, err := c.findIssue(ctx, ref)
	if err != nil {
		return "", err
	}
	target, err := c.findIssue(ctx, targetRef)
	if err != nil {
		return "", err
	}
	if stringField(source, "_id") == stringField(target, "_id") {
		return "", fmt.Errorf("huly: an issue cannot relate to itself")
	}

	var changed bool
	switch relType {
	case "blocks":
		changed, err = c.mutateRelation(ctx, target, fieldBlockedBy, stringField(source, "_id"), remove)
	case "is-blocked-by":
		changed, err = c.mutateRelation(ctx, source, fieldBlockedBy, stringField(target, "_id"), remove)
	case "relates-to":
		var forward bool
		forward, err = c.mutateRelation(ctx, source, fieldRelations, stringField(target, "_id"), remove)
		if err == nil {
			var reverse bool
			reverse, err = c.mutateRelation(ctx, target, fieldRelations, stringField(source, "_id"), remove)
			changed = forward || reverse
		}
	}
	if err != nil {
		return "", err
	}
	return encodeResult(map[string]any{
		"ok": true, "issue": stringField(source, "identifier"),
		"target": stringField(target, "identifier"), "relation": relType,
		"changed": changed,
	})
}

// mutateRelation pushes (or pulls) one relation entry on one issue. It reads
// the issue's current array first, so an add that already exists and a remove
// that is absent are no-ops that post no transaction.
func (c *Client) mutateRelation(ctx context.Context, issue map[string]any, field, id string, remove bool) (bool, error) {
	present := hasRelated(issue, field, id)
	if remove && !present {
		return false, nil
	}
	if !remove && present {
		return false, nil
	}
	s, err := c.session(ctx)
	if err != nil {
		return false, err
	}
	// A push carries the full reference; Huly's own client pulls by `_id`
	// alone, so a remove matches the stored entry.
	operations := map[string]any{"$push": map[string]any{field: map[string]any{"_id": id, "_class": classIssue}}}
	if remove {
		operations = map[string]any{"$pull": map[string]any{field: map[string]any{"_id": id}}}
	}
	transaction := map[string]any{
		"_id": newID(), "_class": classTxUpdateDoc, "space": spaceTx,
		"objectId": stringField(issue, "_id"), "objectClass": classIssue,
		"objectSpace": stringField(issue, "space"),
		"modifiedOn":  time.Now().UnixMilli(), "modifiedBy": s.socialID,
		"operations": operations, "retrieve": false,
	}
	if err := c.tx(ctx, transaction); err != nil {
		return false, err
	}
	return true, nil
}

// hasRelated reports whether an issue's relation array already names id. The
// array comes back as entries `{_id, _class}`; a bare id is tolerated too.
func hasRelated(issue map[string]any, field, id string) bool {
	for _, got := range relatedIDs(issue[field]) {
		if got == id {
			return true
		}
	}
	return false
}

// relatedIDs extracts the ids from an issue's relation array.
func relatedIDs(v any) []string {
	var out []string
	switch entries := v.(type) {
	case []any:
		for _, item := range entries {
			switch e := item.(type) {
			case map[string]any:
				if id := stringField(e, "_id"); id != "" {
					out = append(out, id)
				}
			case string:
				if e != "" {
					out = append(out, e)
				}
			}
		}
	case []map[string]any:
		for _, e := range entries {
			if id := stringField(e, "_id"); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// ── small readers ──────────────────────────────────────────────────────

// actionOf reads the optional `action` argument: add (the default) or remove.
func actionOf(args map[string]any) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(stringArg(args, "action"))) {
	case "", "add":
		return false, nil
	case "remove":
		return true, nil
	default:
		return false, fmt.Errorf("huly: unknown action %q (want add or remove)", stringArg(args, "action"))
	}
}

// encodeResult marshals a tool result, so each handler's success path does not
// repeat the same error wrap.
func encodeResult(v map[string]any) (string, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("huly: encode result: %w", err)
	}
	return string(out), nil
}
