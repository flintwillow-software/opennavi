package huly

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
)

// This file is the egress half of the connector (ADR 0020): the tools a navi
// calls to read and act in Huly. Each is an ordinary tool.Tool built with
// connector.SendTool, so it is added explicitly by the navi author and is
// absent from a binary whose author did not add it (ADR 0006).
//
// The Huly domain strings are the ones the platform itself uses: an issue is
// `tracker:class:Issue`, a status `tracker:class:IssueStatus`, a comment
// `chunter:class:ChatMessage` attached to the issue's `comments` collection,
// and a transaction lives in `core:space:Tx`.

const (
	// ToolIssueList lists issues, newest first.
	ToolIssueList = "huly_issue_list"
	// ToolIssueComment posts a comment on an issue.
	ToolIssueComment = "huly_issue_comment"
)

// Huly class, space, and collection identifiers.
const (
	classIssue         = "tracker:class:Issue"
	classIssueStatus   = "tracker:class:IssueStatus"
	classPerson        = "contact:class:Person"
	classChatMessage   = "chunter:class:ChatMessage"
	classTxCreateDoc   = "core:class:TxCreateDoc"
	spaceTx            = "core:space:Tx"
	collectionComments = "comments"
)

// ── tool definitions ───────────────────────────────────────────────────

func issueListDef() tool.Def {
	return tool.Def{
		Name: ToolIssueList,
		Description: "Lists Huly issues, newest first, optionally filtered by status name or a raw Huly query, " +
			"with status and assignee names resolved. Read-only; the credential is never an argument.",
		InputSchema:  parseSchema(issueListSchema),
		OutputFormat: "json",
		// A read observes state; it is not an execute-level capability, so a
		// read-only mode may still read the tracker.
		Access:   tool.AccessReadOnly,
		Timeout:  20 * time.Second,
		Recovery: "capture_response",
		Mode:     tool.ToolModeSequential,
		Tags:     []string{"huly", "egress", "issue"},
		Version:  "1.0.0",
		PromptSnippet: "huly_issue_list: list Huly issues (status, assignee, priority) " +
			"with optional status/query filters",
		PromptGuidelines: []string{
			"Prefer a status filter over fetching everything when you know the state you want.",
			"The result is bounded (limit, default 20, max 100); narrow the query rather than paging by hand.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func issueCommentDef() tool.Def {
	return tool.Def{
		Name: ToolIssueComment,
		Description: "Posts a comment on a Huly issue, addressed by identifier (NAVI-408) or id, and returns the " +
			"created comment's id. Requires the connector's credential; the token is never an argument.",
		InputSchema:  parseSchema(issueCommentSchema),
		OutputFormat: "json",
		// Egress reaches outside the process, so it is execute-level: a navi
		// that cannot execute cannot act in Huly at all.
		Access:        tool.AccessExecute,
		Timeout:       20 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"huly", "egress", "issue", "comment"},
		Version:       "1.0.0",
		PromptSnippet: "huly_issue_comment: post a comment on a Huly issue",
		PromptGuidelines: []string{
			"Write the comment as you would post it by hand — it is visible to the whole workspace.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

const issueListSchema = `{
	"type": "object",
	"properties": {
		"status": {"type": "string", "description": "Issue status name to filter by (e.g. \"In Progress\", \"Ready for Review\")."},
		"query": {"type": "object", "description": "A raw Huly query merged into the filter (e.g. {\"assignee\": \"<id>\"})."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Maximum issues to return (default 20)."}
	},
	"additionalProperties": false
}`

const issueCommentSchema = `{
	"type": "object",
	"properties": {
		"issue": {"type": "string", "description": "The issue identifier (NAVI-408) or its raw id."},
		"body": {"type": "string", "description": "The comment text."}
	},
	"required": ["issue", "body"],
	"additionalProperties": false
}`

// parseSchema parses a compiled-in schema literal. A failure is a programming
// error: SendTool refuses an empty schema anyway, and failing at composition
// time is the point.
func parseSchema(raw string) tool.Schema {
	s, err := tool.ParseSchema(json.RawMessage(strings.TrimSpace(raw)))
	if err != nil {
		panic(fmt.Sprintf("huly: tool schema: %v", err))
	}
	return s
}

// ── egress handlers ────────────────────────────────────────────────────

// issueList reads issues and resolves the status and assignee references to
// names, so the model reads a tracker row rather than a set of ids.
func (c *Client) issueList(ctx context.Context, args map[string]any) (string, error) {
	limit := intArg(args, "limit", 20)
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := map[string]any{}
	if raw, ok := args["query"].(map[string]any); ok {
		query = raw
	}
	if name := strings.TrimSpace(stringArg(args, "status")); name != "" {
		ref, err := c.statusRef(ctx, name)
		if err != nil {
			return "", err
		}
		if ref == "" {
			return "", fmt.Errorf("huly: no issue status named %q", name)
		}
		query["status"] = ref
	}

	issues, err := c.findAll(ctx, classIssue, query, map[string]any{
		"limit": limit,
		"sort":  map[string]any{"modifiedOn": -1},
	})
	if err != nil {
		return "", err
	}

	statuses, err := c.statusNames(ctx)
	if err != nil {
		return "", err
	}
	persons, err := c.personNames(ctx, assigneeIDs(issues))
	if err != nil {
		return "", err
	}

	rows := make([]map[string]any, 0, len(issues))
	for _, it := range issues {
		status := stringField(it, "status")
		assignee := stringField(it, "assignee")
		rows = append(rows, map[string]any{
			"id":           stringField(it, "_id"),
			"identifier":   stringField(it, "identifier"),
			"title":        stringField(it, "title"),
			"status":       status,
			"statusName":   statuses[status],
			"assignee":     assignee,
			"assigneeName": persons[assignee],
			"priority":     it["priority"],
			"number":       it["number"],
			"modifiedOn":   it["modifiedOn"],
		})
	}
	out, err := json.Marshal(map[string]any{"issues": rows, "count": len(rows)})
	if err != nil {
		return "", fmt.Errorf("huly: encode issues: %w", err)
	}
	return string(out), nil
}

// issueComment posts a comment on an issue, addressed by identifier or id.
func (c *Client) issueComment(ctx context.Context, args map[string]any) (string, error) {
	ref := strings.TrimSpace(stringArg(args, "issue"))
	body := strings.TrimSpace(stringArg(args, "body"))
	if ref == "" {
		return "", fmt.Errorf("huly: issue is required")
	}
	if body == "" {
		return "", fmt.Errorf("huly: body is required")
	}
	issue, err := c.findIssue(ctx, ref)
	if err != nil {
		return "", err
	}
	s, err := c.session(ctx)
	if err != nil {
		return "", err
	}
	commentID := newID()
	// A comment is an attached document: a create transaction carrying the
	// collection and its parent, which is exactly what Huly's own client
	// builds for addCollection.
	transaction := map[string]any{
		"_id":             newID(),
		"_class":          classTxCreateDoc,
		"space":           spaceTx,
		"objectId":        commentID,
		"objectClass":     classChatMessage,
		"objectSpace":     stringField(issue, "space"),
		"modifiedOn":      time.Now().UnixMilli(),
		"modifiedBy":      s.socialID,
		"createdBy":       s.socialID,
		"attributes":      map[string]any{"message": markup(body)},
		"collection":      collectionComments,
		"attachedTo":      stringField(issue, "_id"),
		"attachedToClass": classIssue,
	}
	if err := c.tx(ctx, transaction); err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{
		"ok": true, "id": commentID,
		"issue": stringField(issue, "identifier"),
	})
	if err != nil {
		return "", fmt.Errorf("huly: encode comment result: %w", err)
	}
	return string(out), nil
}

// findIssue resolves an issue by identifier (NAVI-408) or by raw id.
func (c *Client) findIssue(ctx context.Context, ref string) (map[string]any, error) {
	query := map[string]any{"_id": ref}
	if isIdentifier(ref) {
		query = map[string]any{"identifier": ref}
	}
	issues, err := c.findAll(ctx, classIssue, query, map[string]any{"limit": 1})
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, fmt.Errorf("huly: no issue %q", ref)
	}
	return issues[0], nil
}

// statusRef resolves a status name (case-insensitive) or a raw status id to the
// status reference a query filters on. It returns "" when nothing matches.
func (c *Client) statusRef(ctx context.Context, name string) (string, error) {
	statuses, err := c.findAll(ctx, classIssueStatus, nil, map[string]any{"limit": 200})
	if err != nil {
		return "", err
	}
	for _, s := range statuses {
		if strings.EqualFold(stringField(s, "name"), name) {
			return stringField(s, "_id"), nil
		}
	}
	return "", nil
}

// statusNames maps every status id to its display name.
func (c *Client) statusNames(ctx context.Context) (map[string]string, error) {
	statuses, err := c.findAll(ctx, classIssueStatus, nil, map[string]any{"limit": 200})
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(statuses))
	for _, s := range statuses {
		names[stringField(s, "_id")] = stringField(s, "name")
	}
	return names, nil
}

// personNames maps the given person ids to their display names.
func (c *Client) personNames(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	persons, err := c.findAll(ctx, classPerson,
		map[string]any{"_id": map[string]any{"$in": ids}},
		map[string]any{"limit": len(ids)})
	if err != nil {
		return nil, err
	}
	for _, p := range persons {
		names[stringField(p, "_id")] = stringField(p, "name")
	}
	return names, nil
}

// markup renders plain text as the ProseMirror document Huly stores in a
// message field: a single paragraph with one text node.
func markup(text string) string {
	doc := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": text, "marks": []any{}},
				},
			},
		},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// ── small readers ──────────────────────────────────────────────────────

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return def
}

// assigneeIDs collects the distinct assignee references across issues.
func assigneeIDs(issues []map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range issues {
		id := stringField(it, "assignee")
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// isIdentifier reports whether ref looks like a tracker identifier (NAVI-408)
// rather than a raw id.
func isIdentifier(ref string) bool {
	dash := strings.IndexByte(ref, '-')
	if dash <= 0 || dash == len(ref)-1 {
		return false
	}
	for _, r := range ref[:dash] {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') {
			return false
		}
	}
	for _, r := range ref[dash+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
