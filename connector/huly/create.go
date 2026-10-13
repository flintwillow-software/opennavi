package huly

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
)

// This file is the create egress tool. Creating a Huly issue is three steps the
// platform itself makes: bump the project's sequence (which answers the new
// issue's number and identifier), place the issue after the project's last one
// with a LexoRank, and create it as a document attached to the project.
//
// A top-level issue is attached to `tracker:ids:NoParent` in the `subIssues`
// collection; a sub-issue is attached to its parent and carries the parent
// chain. The description is NOT set here: Huly keeps an issue's rich text on
// its collaborator service, not the transactor.

// ToolIssueCreate creates an issue.
const ToolIssueCreate = "huly_issue_create"

// Huly domain identifiers the create path needs.
const (
	classProject        = "tracker:class:Project"
	taskTypeIssue       = "tracker:taskTypes:Issue"
	noParent            = "tracker:ids:NoParent"
	collectionSubIssues = "subIssues"
	spaceModel          = "core:space:Space"
)

func issueCreateDef() tool.Def {
	return tool.Def{
		Name: ToolIssueCreate,
		Description: "Creates a Huly issue in a project, optionally as a sub-issue of another, and returns its " +
			"identifier (NAVI-419) and id. Status, assignee, and priority are optional; the credential is never an argument.",
		InputSchema:  parseSchema(issueCreateSchema),
		OutputFormat: "json",
		Access:       tool.AccessExecute,
		Timeout:      20 * time.Second,
		Recovery:     "capture_response",
		Mode:         tool.ToolModeSequential,
		Tags:         []string{"huly", "egress", "issue"},
		Version:      "1.0.0",
		PromptSnippet: "huly_issue_create: create a Huly issue in a project " +
			"(optionally a sub-issue, with status/assignee/priority)",
		PromptGuidelines: []string{
			"The project is its identifier (NAVI), not its name.",
			"Give a status by its display name; an unknown status is refused rather than defaulted silently.",
			"An issue's description is not set here — Huly keeps rich text on a separate service; put the detail in a comment after creating.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

const issueCreateSchema = `{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project identifier (e.g. \"NAVI\")."},
		"title": {"type": "string", "description": "Issue title."},
		"parent": {"type": "string", "description": "Parent issue identifier or id, to create a sub-issue."},
		"status": {"type": "string", "description": "Status display name; defaults to the project's default status."},
		"assignee": {"type": "string", "description": "Assignee display name or person id."},
		"priority": {"type": "string", "enum": ["urgent", "high", "medium", "low", "no-priority"], "description": "Priority; defaults to no-priority."}
	},
	"required": ["project", "title"],
	"additionalProperties": false
}`

// issueCreate creates an issue and returns its identifier.
func (c *Client) issueCreate(ctx context.Context, args map[string]any) (string, error) {
	projectRef := strings.TrimSpace(stringArg(args, "project"))
	title := strings.TrimSpace(stringArg(args, "title"))
	if projectRef == "" {
		return "", fmt.Errorf("huly: project is required")
	}
	if title == "" {
		return "", fmt.Errorf("huly: title is required")
	}
	project, err := c.findProject(ctx, projectRef)
	if err != nil {
		return "", err
	}

	// A top-level issue hangs off NoParent; a sub-issue off its parent, and
	// carries the parent chain so a nested issue knows its ancestry.
	attachedTo := noParent
	parents := []any{}
	if parentRef := strings.TrimSpace(stringArg(args, "parent")); parentRef != "" {
		parent, err := c.findIssue(ctx, parentRef)
		if err != nil {
			return "", err
		}
		attachedTo = stringField(parent, "_id")
		parents = append(parentsOf(parent), map[string]any{
			"parentId":    attachedTo,
			"identifier":  stringField(parent, "identifier"),
			"parentTitle": stringField(parent, "title"),
			"space":       stringField(project, "_id"),
		})
	}

	sequence, err := c.nextSequence(ctx, project)
	if err != nil {
		return "", err
	}
	rank, err := c.nextRank(ctx, project)
	if err != nil {
		return "", err
	}

	status := stringField(project, "defaultIssueStatus")
	if name := strings.TrimSpace(stringArg(args, "status")); name != "" {
		ref, err := c.statusRef(ctx, name)
		if err != nil {
			return "", err
		}
		if ref == "" {
			return "", fmt.Errorf("huly: no issue status named %q", name)
		}
		status = ref
	}
	assignee, err := c.personRef(ctx, stringArg(args, "assignee"))
	if err != nil {
		return "", err
	}
	priority := 0
	if name := strings.TrimSpace(stringArg(args, "priority")); name != "" {
		p, ok := priorityFromString(name)
		if !ok {
			return "", fmt.Errorf("huly: unknown priority %q (want urgent, high, medium, low, or no-priority)", name)
		}
		priority = p
	}

	s, err := c.session(ctx)
	if err != nil {
		return "", err
	}
	issueID := newID()
	identifier := fmt.Sprintf("%s-%d", stringField(project, "identifier"), sequence)
	issueData := map[string]any{
		"title": title, "description": nil, "status": status, "number": sequence,
		"kind": taskTypeIssue, "identifier": identifier, "priority": priority,
		"assignee": assignee, "component": nil, "estimation": 0,
		"remainingTime": 0, "reportedTime": 0, "reports": 0, "subIssues": 0,
		"parents": parents, "childInfo": []any{}, "dueDate": nil, "rank": rank,
	}
	transaction := map[string]any{
		"_id": newID(), "_class": classTxCreateDoc, "space": spaceTx,
		"objectId": issueID, "objectClass": classIssue,
		"objectSpace": stringField(project, "_id"),
		"modifiedOn":  time.Now().UnixMilli(), "modifiedBy": s.socialID, "createdBy": s.socialID,
		"attributes": issueData, "collection": collectionSubIssues,
		"attachedTo": attachedTo, "attachedToClass": classIssue,
	}
	if err := c.tx(ctx, transaction); err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"ok": true, "id": issueID, "identifier": identifier})
	if err != nil {
		return "", fmt.Errorf("huly: encode create result: %w", err)
	}
	return string(out), nil
}

// findProject resolves a project by its identifier (NAVI).
func (c *Client) findProject(ctx context.Context, identifier string) (map[string]any, error) {
	projects, err := c.findAll(ctx, classProject,
		map[string]any{"identifier": strings.ToUpper(strings.TrimSpace(identifier))},
		map[string]any{"limit": 1})
	if err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("huly: no project %q", identifier)
	}
	return projects[0], nil
}

// nextSequence bumps the project's sequence and returns the new value, which is
// the issue's number and the tail of its identifier. The increment is a
// transaction, so two creates cannot both read the old value; the answer
// carries the updated document, and a re-read is the fallback when it does not.
func (c *Client) nextSequence(ctx context.Context, project map[string]any) (int, error) {
	s, err := c.session(ctx)
	if err != nil {
		return 0, err
	}
	transaction := map[string]any{
		"_id": newID(), "_class": classTxUpdateDoc, "space": spaceTx,
		"objectId": stringField(project, "_id"), "objectClass": classProject,
		"objectSpace": spaceModel,
		"modifiedOn":  time.Now().UnixMilli(), "modifiedBy": s.socialID,
		"operations": map[string]any{"$inc": map[string]any{"sequence": 1}},
		"retrieve":   true,
	}
	body, err := c.txRaw(ctx, transaction)
	if err != nil {
		return 0, err
	}
	if seq := sequenceFrom(body); seq > 0 {
		return seq, nil
	}
	fresh, err := c.findProject(ctx, stringField(project, "identifier"))
	if err != nil {
		return 0, err
	}
	seq := intField(fresh, "sequence")
	if seq <= 0 {
		return 0, fmt.Errorf("huly: project %s reported no sequence", stringField(project, "identifier"))
	}
	return seq, nil
}

// nextRank places a new issue after the project's last one.
func (c *Client) nextRank(ctx context.Context, project map[string]any) (string, error) {
	issues, err := c.findAll(ctx, classIssue,
		map[string]any{"space": stringField(project, "_id")},
		map[string]any{"limit": 1, "sort": map[string]any{"rank": -1}})
	if err != nil {
		return "", err
	}
	if len(issues) == 0 {
		return rankMiddle, nil
	}
	return rankNext(stringField(issues[0], "rank"))
}

// parentsOf copies an issue's parent chain.
func parentsOf(issue map[string]any) []any {
	if p, ok := issue["parents"].([]any); ok {
		return append([]any(nil), p...)
	}
	return nil
}

// sequenceFrom finds a `sequence` in a transaction answer, wherever the server
// nested it. It returns 0 when there is none.
func sequenceFrom(body []byte) int {
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return 0
	}
	return findSequence(doc)
}

func findSequence(v any) int {
	switch t := v.(type) {
	case map[string]any:
		if i := toInt(t["sequence"]); i > 0 {
			return i
		}
		for _, key := range []string{"result", "value", "object"} {
			if child, ok := t[key]; ok {
				if i := findSequence(child); i > 0 {
					return i
				}
			}
		}
	case []any:
		for _, child := range t {
			if i := findSequence(child); i > 0 {
				return i
			}
		}
	}
	return 0
}

// intField reads an integer field from a decoded document.
func intField(m map[string]any, key string) int { return toInt(m[key]) }

// toInt reads a JSON number as an int.
func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}
