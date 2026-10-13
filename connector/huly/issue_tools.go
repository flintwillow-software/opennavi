package huly

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
)

// This file is the update and search egress tools. Create, labels, and
// relations need more than the transactor (a sequence bump plus a LexoRank for
// create, the tags model for labels, relation collections for links), and the
// rich-text description lives on Huly's collaborator service rather than the
// transactor — those are tracked separately.

// ToolIssueUpdate updates an issue's status, assignee, or priority.
const ToolIssueUpdate = "huly_issue_update"

// ToolIssueSearch searches the workspace full-text.
const ToolIssueSearch = "huly_issue_search"

// Huly update-transaction class.
const classTxUpdateDoc = "core:class:TxUpdateDoc"

func issueUpdateDef() tool.Def {
	return tool.Def{
		Name: ToolIssueUpdate,
		Description: "Updates a Huly issue's status, assignee, or priority, addressed by identifier (NAVI-408) or id. " +
			"Only the fields given are changed; the credential is never an argument.",
		InputSchema:  parseSchema(issueUpdateSchema),
		OutputFormat: "json",
		// A mutation of an external system: execute-level, like every egress
		// write.
		Access:        tool.AccessExecute,
		Timeout:       20 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"huly", "egress", "issue"},
		Version:       "1.0.0",
		PromptSnippet: "huly_issue_update: change a Huly issue's status, assignee, or priority",
		PromptGuidelines: []string{
			"Give a status by its display name (\"In Progress\", \"Ready for Review\") and an assignee by display name or id.",
			"An empty assignee clears it.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func issueSearchDef() tool.Def {
	return tool.Def{
		Name:         ToolIssueSearch,
		Description:  "Searches Huly full-text across the workspace and returns matching documents (issues and others) with their identifier, title, and class. Read-only.",
		InputSchema:  parseSchema(issueSearchSchema),
		OutputFormat: "json",
		Access:       tool.AccessReadOnly,
		Timeout:      20 * time.Second,
		Recovery:     "capture_response",
		Mode:         tool.ToolModeSequential,
		Tags:         []string{"huly", "egress", "search"},
		Version:      "1.0.0",
		PromptSnippet: "huly_issue_search: full-text search the Huly workspace " +
			"(issues and other documents)",
		PromptGuidelines: []string{
			"Use this to find an issue by its content when you do not know its identifier.",
			"The result is bounded (limit, default 20, max 50).",
		},
	}
}

const issueUpdateSchema = `{
	"type": "object",
	"properties": {
		"issue": {"type": "string", "description": "The issue identifier (NAVI-408) or its raw id."},
		"status": {"type": "string", "description": "New status display name (e.g. \"In Progress\")."},
		"assignee": {"type": "string", "description": "New assignee display name or person id; an empty string clears it."},
		"priority": {"type": "string", "enum": ["urgent", "high", "medium", "low", "no-priority"], "description": "New priority."}
	},
	"required": ["issue"],
	"additionalProperties": false
}`

const issueSearchSchema = `{
	"type": "object",
	"properties": {
		"query": {"type": "string", "description": "The full-text query."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum results (default 20)."}
	},
	"required": ["query"],
	"additionalProperties": false
}`

// issueUpdate applies a status, assignee, or priority change. Only the fields
// given are touched, and the change is one transaction.
func (c *Client) issueUpdate(ctx context.Context, args map[string]any) (string, error) {
	ref := strings.TrimSpace(stringArg(args, "issue"))
	if ref == "" {
		return "", fmt.Errorf("huly: issue is required")
	}
	issue, err := c.findIssue(ctx, ref)
	if err != nil {
		return "", err
	}

	set := map[string]any{}
	var changed []string
	if raw, ok := args["status"]; ok {
		name := strings.TrimSpace(asString(raw))
		if name == "" {
			return "", fmt.Errorf("huly: status cannot be empty")
		}
		status, err := c.statusRef(ctx, name)
		if err != nil {
			return "", err
		}
		if status == "" {
			return "", fmt.Errorf("huly: no issue status named %q", name)
		}
		set["status"] = status
		changed = append(changed, "status")
	}
	if raw, ok := args["assignee"]; ok {
		assignee, err := c.personRef(ctx, asString(raw))
		if err != nil {
			return "", err
		}
		set["assignee"] = assignee
		changed = append(changed, "assignee")
	}
	if raw, ok := args["priority"]; ok {
		priority, ok := priorityFromString(asString(raw))
		if !ok {
			return "", fmt.Errorf("huly: unknown priority %q (want urgent, high, medium, low, or no-priority)", asString(raw))
		}
		set["priority"] = priority
		changed = append(changed, "priority")
	}
	if len(set) == 0 {
		return "", fmt.Errorf("huly: nothing to update (give a status, assignee, or priority)")
	}

	s, err := c.session(ctx)
	if err != nil {
		return "", err
	}
	transaction := map[string]any{
		"_id":         newID(),
		"_class":      classTxUpdateDoc,
		"space":       spaceTx,
		"objectId":    stringField(issue, "_id"),
		"objectClass": classIssue,
		"objectSpace": stringField(issue, "space"),
		"modifiedOn":  time.Now().UnixMilli(),
		"modifiedBy":  s.account,
		"operations":  map[string]any{"$set": set},
		"retrieve":    false,
	}
	if err := c.tx(ctx, transaction); err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{
		"ok": true, "issue": stringField(issue, "identifier"), "updated": changed,
	})
	if err != nil {
		return "", fmt.Errorf("huly: encode update result: %w", err)
	}
	return string(out), nil
}

// issueSearch runs a full-text search and returns the matching documents. Huly
// answers `{"docs": [...], "total": n}`; each doc carries the object id, a
// title, a short title (an issue's identifier), and its class.
func (c *Client) issueSearch(ctx context.Context, args map[string]any) (string, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return "", fmt.Errorf("huly: query is required")
	}
	limit := intArg(args, "limit", 20)
	if limit < 1 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	s, err := c.session(ctx)
	if err != nil {
		return "", err
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("limit", fmt.Sprintf("%d", limit))
	body, err := c.getRaw(ctx, fmt.Sprintf("%s/api/v1/search-fulltext/%s?%s",
		strings.TrimRight(s.endpoint, "/"), url.PathEscape(s.workspace), params.Encode()), s.token)
	if err != nil {
		return "", err
	}
	var envelope struct {
		Docs []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			ShortTitle string `json:"shortTitle"`
			Score      any    `json:"score"`
			Doc        struct {
				Class string `json:"_class"`
			} `json:"doc"`
		} `json:"docs"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("huly: decode search response: %w", err)
	}
	results := make([]map[string]any, 0, len(envelope.Docs))
	for _, d := range envelope.Docs {
		results = append(results, map[string]any{
			"id": d.ID, "title": d.Title, "identifier": d.ShortTitle,
			"class": d.Doc.Class, "score": d.Score,
		})
	}
	out, err := json.Marshal(map[string]any{"results": results, "count": len(results)})
	if err != nil {
		return "", fmt.Errorf("huly: encode search result: %w", err)
	}
	return string(out), nil
}

// personRef resolves an assignee argument: an empty value clears the assignee,
// a raw id is used as-is, and anything else is matched against person display
// names (exact, case-insensitive).
func (c *Client) personRef(ctx context.Context, value string) (any, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil, nil
	}
	if isRawID(v) {
		return v, nil
	}
	persons, err := c.findAll(ctx, classPerson, nil, map[string]any{"limit": 500})
	if err != nil {
		return nil, err
	}
	for _, p := range persons {
		if strings.EqualFold(stringField(p, "name"), v) {
			return stringField(p, "_id"), nil
		}
	}
	return nil, fmt.Errorf("huly: no person named %q", v)
}

// Huly's IssuePriority scale: no-priority is 0, and the rest descend so that a
// LOWER number is a HIGHER priority (urgent is 1, low is 4).
var priorityByName = map[string]int{
	"no-priority": 0,
	"urgent":      1,
	"high":        2,
	"medium":      3,
	"low":         4,
}

// priorityFromString maps a priority name to Huly's number.
func priorityFromString(name string) (int, bool) {
	v, ok := priorityByName[strings.ToLower(strings.TrimSpace(name))]
	return v, ok
}

// priorityToString maps Huly's number back to a name.
func priorityToString(v int) string {
	for name, n := range priorityByName {
		if n == v {
			return name
		}
	}
	return ""
}

// asString renders an argument as a string (JSON numbers arrive as float64).
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return fmt.Sprintf("%g", t)
	default:
		return ""
	}
}

// isRawID reports whether v is a Huly document id (24 lowercase hex).
func isRawID(v string) bool {
	if len(v) != 24 {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
